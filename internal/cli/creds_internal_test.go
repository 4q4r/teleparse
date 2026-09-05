package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/4q4r/teleparse/internal/config"
	"github.com/4q4r/teleparse/internal/tg"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validHash32 is a syntactically valid api_hash for the prompt flow.
const validHash32 = "0123456789abcdef0123456789abcdef"

// scriptedCredsPrompter feeds queued answers and records prompts and notices.
type scriptedCredsPrompter struct {
	answers []string
	prompts []string
	notices []string
}

func (p *scriptedCredsPrompter) Line(prompt string) (string, error) {
	return p.next(prompt)
}

func (p *scriptedCredsPrompter) Hidden(prompt string) (string, error) {
	return p.next(prompt)
}

func (p *scriptedCredsPrompter) Notice(line string) error {
	p.notices = append(p.notices, line)

	return nil
}

func (p *scriptedCredsPrompter) next(prompt string) (string, error) {
	p.prompts = append(p.prompts, prompt)

	if len(p.answers) == 0 {
		return "", errors.New("script exhausted")
	}

	answer := p.answers[0]
	p.answers = p.answers[1:]

	return answer, nil
}

// redirectCreds redirects the credentials file into a temp XDG home.
func redirectCreds(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
	t.Setenv("TELEPARSE_API_ID", "")
	t.Setenv("TELEPARSE_API_HASH", "")

	return filepath.Join(root, "xdg", "teleparse", "credentials.toml")
}

func TestCredsHelpErrorContainsActionableMarkers(t *testing.T) {
	t.Parallel()

	err := credsHelpError(errors.New("boom"))

	require.Error(t, err)
	message := err.Error()

	assert.Contains(t, message, "TELEPARSE_API_ID=")
	assert.Contains(t, message, "TELEPARSE_API_HASH=")
	assert.Contains(t, message, "my.telegram.org")
	assert.Contains(t, message, "auth login")
	assert.Contains(t, message, "boom")
}

func TestResolveCredsPassesEnvPairDown(t *testing.T) {
	t.Setenv("TELEPARSE_API_ID", "123456")
	t.Setenv("TELEPARSE_API_HASH", validHash32)

	app := &App{}

	creds, err := app.resolveCreds()

	require.NoError(t, err)
	assert.Equal(t, int64(123456), creds.APIID)
	assert.Equal(t, validHash32, creds.APIHash)
}

func TestResolveCredsUnresolvedWrapsHelp(t *testing.T) {
	redirectCreds(t)

	app := &App{}

	creds, err := app.resolveCreds()

	require.Error(t, err)
	assert.Equal(t, tg.Creds{}, creds)
	assert.Contains(t, err.Error(), "TELEPARSE_API_ID=")
	assert.Contains(t, err.Error(), "my.telegram.org")
	assert.Contains(t, err.Error(), "auth login")
}

func TestPromptCredsSaveYesWrites0600File(t *testing.T) {
	path := redirectCreds(t)

	ask := &scriptedCredsPrompter{answers: []string{"123456", validHash32, ""}}

	creds, err := promptCreds(ask)

	require.NoError(t, err)
	assert.Equal(t, int64(123456), creds.APIID)
	assert.Equal(t, validHash32, creds.APIHash)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "123456")
	assert.Contains(t, string(raw), validHash32)
	assert.Contains(t, string(raw), "my.telegram.org")

	assert.Equal(t, []string{"API id", "API hash"}, ask.prompts[:2])
	assert.Contains(t, ask.prompts[2], "Save to")
	assert.NotEmpty(t, ask.notices, "the save must be confirmed with a notice")
}

func TestPromptCredsSaveNoKeepsFileAbsent(t *testing.T) {
	path := redirectCreds(t)

	ask := &scriptedCredsPrompter{answers: []string{"123456", validHash32, "n"}}

	creds, err := promptCreds(ask)

	require.NoError(t, err)
	assert.Equal(t, int64(123456), creds.APIID)

	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "answering n must not write the file")
}

func TestPromptCredsThreeStrikesOnIDStopsAsking(t *testing.T) {
	redirectCreds(t)

	ask := &scriptedCredsPrompter{answers: []string{"abc", "-1", "0", "999", validHash32}}

	_, err := promptCreds(ask)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "TELEPARSE_API_ID=")
	assert.Contains(t, err.Error(), "my.telegram.org")
	assert.Len(t, ask.prompts, maxCredsAttempts, "only the api id may be asked")
	assert.Len(t, ask.notices, maxCredsAttempts)
}

func TestPromptCredsThreeStrikesOnHashStopsAsking(t *testing.T) {
	redirectCreds(t)

	ask := &scriptedCredsPrompter{answers: []string{"123456", "short", "short", "short"}}

	_, err := promptCreds(ask)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "api hash")
	assert.Contains(t, err.Error(), "TELEPARSE_API_HASH=")
	assert.Len(t, ask.prompts, 1+maxCredsAttempts, "id once, then three hash attempts")
}

func TestPromptCredsRecoversAfterBadAttempts(t *testing.T) {
	redirectCreds(t)

	ask := &scriptedCredsPrompter{answers: []string{"no", "123456", "bad", validHash32, "y"}}

	creds, err := promptCreds(ask)

	require.NoError(t, err)
	assert.Equal(t, int64(123456), creds.APIID)
	assert.Equal(t, validHash32, creds.APIHash)
	assert.Len(t, ask.notices, 3, "one notice per bad attempt plus the save confirmation")
}

func TestResolveCredsFileFallback(t *testing.T) {
	redirectCreds(t)

	require.NoError(t, config.SaveCredentials(777777, validHash32))

	app := &App{}

	creds, err := app.resolveCreds()

	require.NoError(t, err)
	assert.Equal(t, int64(777777), creds.APIID)
	assert.Equal(t, validHash32, creds.APIHash)
}
