package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/4q4r/teleparse/internal/config"
	"github.com/4q4r/teleparse/internal/tg"
)

// maxCredsAttempts bounds re-asks per field in the interactive flow before
// the actionable error takes over.
const maxCredsAttempts = 3

// Sentinel errors for exhausted interactive retries.
var (
	errCredsIDExhausted   = errors.New("no valid api id entered")
	errCredsHashExhausted = errors.New("no valid api hash entered")
)

// credsHelpError is the single actionable error surfaced whenever credentials
// are missing or invalid, on auth login and the dl-family alike: it explains
// where to get the pair, the exact exports, and the save-once alternative.
func credsHelpError(cause error) error {
	return fmt.Errorf(`telegram api credentials are missing or invalid: %w

How to get them:
  1. open https://my.telegram.org and log in
  2. open "API development tools" (any app title works)

Then either export the variables (shell profile, systemd unit, docker env):
  export TELEPARSE_API_ID=123456
  export TELEPARSE_API_HASH=0123456789abcdef0123456789abcdef

or run "teleparse auth login" once in a terminal and answer the prompts to
save them to %s`, cause, config.CredentialsPath())
}

// resolveCreds resolves the api credentials through the config chain
// (environment first, credentials file second), wrapping any failure into
// the actionable help error.
func (a *App) resolveCreds() (tg.Creds, error) {
	apiID, apiHash, _, err := config.Credentials()
	if err != nil {
		return tg.Creds{}, credsHelpError(err)
	}

	return tg.Creds{APIID: apiID, APIHash: apiHash}, nil
}

// resolveCredsInteractive resolves credentials for auth login: an unresolved
// pair falls into the interactive interview when stdin is a terminal,
// otherwise into the actionable help error.
func resolveCredsInteractive(stdinTTY bool, ask credsPrompter) (tg.Creds, error) {
	apiID, apiHash, _, err := config.Credentials()
	if err == nil {
		return tg.Creds{APIID: apiID, APIHash: apiHash}, nil
	}

	if !stdinTTY {
		return tg.Creds{}, credsHelpError(err)
	}

	return promptCreds(ask)
}

// credsPrompter is the interactive seam of the credential interview: Line
// and Hidden mirror tg.Prompter, Notice prints validation feedback between
// attempts.
type credsPrompter interface {
	Line(prompt string) (string, error)
	Hidden(prompt string) (string, error)
	Notice(line string) error
}

// notifyingPrompter extends stdPrompter with plain feedback lines.
type notifyingPrompter struct {
	stdPrompter
}

// Notice prints one feedback line to the prompter's output.
func (p notifyingPrompter) Notice(line string) error {
	return printPlain(p.out, line)
}

// promptCreds interviews the user for the api_id and api_hash (up to
// maxCredsAttempts each, with validation feedback between tries), then
// offers to save the pair to the credentials file for future runs.
func promptCreds(ask credsPrompter) (tg.Creds, error) {
	apiID, err := askAPIID(ask)
	if err != nil {
		return tg.Creds{}, err
	}

	apiHash, err := askAPIHash(ask)
	if err != nil {
		return tg.Creds{}, err
	}

	if err := offerSaveCreds(ask, apiID, apiHash); err != nil {
		return tg.Creds{}, err
	}

	return tg.Creds{APIID: apiID, APIHash: apiHash}, nil
}

// askAPIID asks for a numeric api id, re-asking on invalid input.
func askAPIID(ask credsPrompter) (int64, error) {
	for attempt := 1; attempt <= maxCredsAttempts; attempt++ {
		answer, err := ask.Line("API id")
		if err != nil {
			return 0, fmt.Errorf("read api id: %w", err)
		}

		apiID, err := config.ParseAPIID(answer)
		if err == nil {
			return apiID, nil
		}

		if noticeErr := ask.Notice("invalid api id (" + err.Error() + "), try again"); noticeErr != nil {
			return 0, fmt.Errorf("print notice: %w", noticeErr)
		}
	}

	return 0, credsHelpError(fmt.Errorf("%d attempts: %w", maxCredsAttempts, errCredsIDExhausted))
}

// askAPIHash asks for the hidden api hash, re-asking on invalid input.
func askAPIHash(ask credsPrompter) (string, error) {
	for attempt := 1; attempt <= maxCredsAttempts; attempt++ {
		answer, err := ask.Hidden("API hash")
		if err != nil {
			return "", fmt.Errorf("read api hash: %w", err)
		}

		validationErr := config.ValidateAPIHash(answer)
		if validationErr == nil {
			return answer, nil
		}

		if noticeErr := ask.Notice("invalid api hash (" + validationErr.Error() + "), try again"); noticeErr != nil {
			return "", fmt.Errorf("print notice: %w", noticeErr)
		}
	}

	return "", credsHelpError(fmt.Errorf("%d attempts: %w", maxCredsAttempts, errCredsHashExhausted))
}

// offerSaveCreds asks whether to persist the pair (default yes) and writes
// the credentials file when the user agrees.
func offerSaveCreds(ask credsPrompter, apiID int64, apiHash string) error {
	answer, err := ask.Line("Save to " + config.CredentialsPath() + " for future runs? [Y/n]")
	if err != nil {
		return fmt.Errorf("read save choice: %w", err)
	}

	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer != "" && answer != "y" && answer != "yes" {
		return nil
	}

	if err := config.SaveCredentials(apiID, apiHash); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}

	if err := ask.Notice("saved to " + config.CredentialsPath()); err != nil {
		return fmt.Errorf("print save notice: %w", err)
	}

	return nil
}
