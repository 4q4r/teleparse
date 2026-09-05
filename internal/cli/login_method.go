package cli

import (
	"errors"
	"fmt"
	"strings"
)

// errBadLoginChoice rejects unknown interactive menu choices.
var errBadLoginChoice = errors.New("unknown login method choice")

// errLoginPathRequired rejects empty import paths picked interactively.
var errLoginPathRequired = errors.New("path is required")

// loginMenuBody lists the interactive login methods; choice 1 is the default.
const loginMenuBody = `Select login method:
  1) Phone number + code (default)
  2) QR code (scan from another device)
  3) Import a Telethon session file
  4) Import Telegram Desktop tdata`

// loginMenuHint is the trailing choice prompt of the login menu.
const loginMenuHint = "Choice [1]"

// loginMenuText renders the full login menu: the numbered methods stay
// plain, the choice hint is de-emphasized.
func loginMenuText(styler Styler) string {
	return loginMenuBody + "\n" + styler.Dim(loginMenuHint)
}

// loginPlan is the resolved single-run login configuration.
type loginPlan struct {
	Phone        string
	QR           bool
	TelethonPath string
	TDataDir     string
}

// maxMenuAttempts bounds how many times an invalid menu choice re-asks.
const maxMenuAttempts = 3

// resolveLoginMethod decides how this login runs: explicit flags win and
// skip the menu entirely; with no flags and an interactive terminal the
// user picks a method from menu (default phone, invalid choices re-ask up
// to maxMenuAttempts times); non-interactive sessions fall back to the
// phone flow so scripted logins keep working.
func resolveLoginMethod(menu string, qr bool, phone, telethon, tdata string, interactive bool,
	ask func(string) (string, error),
) (loginPlan, error) {
	plan := loginPlan{Phone: phone, QR: qr, TelethonPath: telethon, TDataDir: tdata}

	explicit := qr || phone != "" || telethon != "" || tdata != ""
	if explicit || !interactive {
		return plan, nil
	}

	prompt := menu

	for range maxMenuAttempts {
		answer, err := ask(prompt)
		if err != nil {
			return loginPlan{}, fmt.Errorf("read login method: %w", err)
		}

		if err := plan.applyChoice(answer, ask); err != nil {
			if !errors.Is(err, errBadLoginChoice) {
				return loginPlan{}, err
			}

			prompt = "unknown login method choice (1-4), try again\n" + menu

			continue
		}

		return plan, nil
	}

	return loginPlan{}, fmt.Errorf("%d invalid choices: %w", maxMenuAttempts, errBadLoginChoice)
}

func (p *loginPlan) applyChoice(choice string, ask func(string) (string, error)) error {
	switch strings.TrimSpace(choice) {
	case "", "1":
		return nil
	case "2":
		p.QR = true

		return nil

	case "3":
		path, err := askPath("Telethon .session file", ask)
		if err != nil {
			return err
		}

		p.TelethonPath = path

		return nil
	case "4":
		path, err := askPath("tdata directory", ask)
		if err != nil {
			return err
		}

		p.TDataDir = path

		return nil
	default:
		return fmt.Errorf("%q: %w (1-4)", choice, errBadLoginChoice)
	}
}

func askPath(what string, ask func(string) (string, error)) (string, error) {
	answer, err := ask("Path to " + what)
	if err != nil {
		return "", fmt.Errorf("read %s path: %w", what, err)
	}

	if answer == "" {
		return "", fmt.Errorf("%s: %w", what, errLoginPathRequired)
	}

	return answer, nil
}
