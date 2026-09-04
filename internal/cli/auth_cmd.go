package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"teleparse/internal/tg"

	"github.com/gotd/td/telegram"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// errNotAuthorized marks a missing session without aborting other output.
var errNotAuthorized = errors.New("account is not logged in")

// stdPrompter asks on stdout/stdin, hiding secrets when the terminal allows.
type stdPrompter struct {
	out *os.File
	in  *os.File
}

func newStdPrompter() stdPrompter {
	return stdPrompter{out: os.Stdout, in: os.Stdin}
}

func (p stdPrompter) Line(prompt string) (string, error) {
	if err := printPlain(p.out, prompt+":"); err != nil {
		return "", err
	}

	reader := bufio.NewReader(p.in)

	answer, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read answer: %w", err)
	}

	return strings.TrimSpace(answer), nil
}

func (p stdPrompter) Hidden(prompt string) (string, error) {
	if err := printPlain(p.out, prompt+":"); err != nil {
		return "", err
	}

	if term.IsTerminal(int(p.in.Fd())) {
		secret, err := term.ReadPassword(int(p.in.Fd()))
		if err != nil {
			return "", fmt.Errorf("read secret: %w", err)
		}

		if err := printPlain(p.out, ""); err != nil {
			return "", err
		}

		return strings.TrimSpace(string(secret)), nil
	}

	return p.Line("")
}

// printPlain writes one line to an os.File with a checked error.
func printPlain(file *os.File, line string) error {
	if _, err := fmt.Fprintln(file, line); err != nil {
		return fmt.Errorf("write prompt: %w", err)
	}

	return nil
}

func authCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage account sessions (login, logout, status)",
	}

	cmd.AddCommand(
		authLoginCmd(app),
		authLogoutCmd(app),
		authStatusCmd(app),
		authListCmd(app),
		authExportCmd(app),
	)

	return cmd
}

func authLoginCmd(app *App) *cobra.Command {
	var (
		phone      string
		importPath string
	)

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Interactive login: phone, code, optional 2FA password",
		Example: "  teleparse auth login\n" +
			"  teleparse auth login --phone +15551234567\n" +
			"  teleparse auth login --import-telethon old.session.sqlite",
		RunE: func(cmd *cobra.Command, _ []string) error {
			account := app.cfg.Auth.Account

			if importPath != "" {
				storage, err := tg.NewAccountManager(app.paths.AccountsDir).Storage(account)
				if err != nil {
					return fail(cmd, err)
				}

				if err := tg.TelethonSessionImport(cmd.Context(), importPath, storage); err != nil {
					return fail(cmd, err)
				}

				if err := printLine(cmd, "imported telethon session for account %s\n", account); err != nil {
					return fail(cmd, err)
				}
			}

			if err := tg.Login(cmd.Context(), account, phone, newStdPrompter(), app.cfg, app.paths); err != nil {
				return fail(cmd, err)
			}

			return printLine(cmd, "account %s is logged in\n", account)
		},
	}
	cmd.Flags().StringVar(&phone, "phone", "", "phone number in +E.164 format (asked interactively when empty)")
	cmd.Flags().StringVar(&importPath, "import-telethon", "", "import a Telethon .session SQLite file before login")

	return cmd
}

func authLogoutCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "logout",
		Short:   "Log out and delete the local session",
		Example: "  teleparse auth logout --account work",
		RunE: func(cmd *cobra.Command, _ []string) error {
			account := app.cfg.Auth.Account

			if err := tg.Logout(cmd.Context(), account, app.cfg, app.paths); err != nil {
				return fail(cmd, err)
			}

			return printLine(cmd, "account %s logged out\n", account)
		},
	}
}

func authStatusCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "status",
		Short:   "Show who am I, session age, DC",
		Example: "  teleparse auth status --account work",
		RunE: func(cmd *cobra.Command, _ []string) error {
			account := app.cfg.Auth.Account

			manager := tg.NewAccountManager(app.paths.AccountsDir)

			storage, err := manager.Storage(account)
			if err != nil {
				return fail(cmd, err)
			}

			err = tg.Run(cmd.Context(), account, app.cfg, app.paths, func(
				ctx context.Context,
				client *telegram.Client,
			) error {
				info, err := tg.WhoAmI(ctx, client, storage)
				if err != nil {
					return fmt.Errorf("whoami: %w", err)
				}

				if !info.Authorized {
					return errNotAuthorized
				}

				premium := manager.AccountPremium(ctx, account, client.Self)

				detail := fmt.Sprintf("account:  %s\nuser id:  %d\nname:     %s\nphone:    %s\npremium:  %s (source: %s)\n",
					account, info.ID, strings.TrimSpace(info.FirstName+" "+info.LastName), info.Phone,
					yesNo(premium.Premium), premium.Source)

				if info.Username != "" {
					detail += fmt.Sprintf("username: @%s\n", info.Username)
				}

				if info.DC != 0 {
					detail += fmt.Sprintf("dc:       %d\n", info.DC)
				}

				return printLine(cmd, "%s", detail)
			})
			if errors.Is(err, errNotAuthorized) {
				return printLine(cmd, "account %s is NOT logged in\n", account)
			}

			if err != nil {
				return fail(cmd, err)
			}

			return nil
		},
	}
}

func authListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List known local sessions",
		Example: "  teleparse auth list --format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			names, err := tg.NewAccountManager(app.paths.AccountsDir).List()
			if err != nil {
				return fail(cmd, err)
			}

			switch app.outputFormat() {
			case FormatJSON:
				if len(names) == 0 {
					names = []string{}
				}

				return printJSON(cmd, names)
			case FormatPlain:
				if len(names) == 0 {
					return printLine(cmd, "no accounts; run: teleparse auth login\n")
				}

				for _, name := range names {
					if err := printLine(cmd, "account: %s\n", name); err != nil {
						return fail(cmd, err)
					}
				}

				return nil
			default:
				if len(names) == 0 {
					return printLine(cmd, "no accounts; run: teleparse auth login\n")
				}

				for _, name := range names {
					marker := ""

					if name == app.cfg.Auth.Account {
						marker = " (default)"
					}

					if err := printLine(cmd, "%s%s\n", name, marker); err != nil {
						return fail(cmd, err)
					}
				}

				return nil
			}
		},
	}
}

func authExportCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "export",
		Short:   "Export the raw session JSON (treat as a secret)",
		Example: "  teleparse auth export > session.json",
		Long: "Export the stored session as JSON. NOTE: a portable string-session\n" +
			"format (--string) is not available: gotd/contrib v0.25.0 ships no encoder\n" +
			"(session.TelethonSession decodes only).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			storage, err := tg.NewAccountManager(app.paths.AccountsDir).Storage(app.cfg.Auth.Account)
			if err != nil {
				return fail(cmd, err)
			}

			raw, err := os.ReadFile(storage.Path)
			if err != nil {
				return fail(cmd, fmt.Errorf("read session: %w", err))
			}

			var pretty map[string]any
			if err := json.Unmarshal(raw, &pretty); err != nil {
				return fail(cmd, fmt.Errorf("parse session: %w", err))
			}

			blob, err := json.MarshalIndent(pretty, "", "  ")
			if err != nil {
				return fail(cmd, fmt.Errorf("encode session: %w", err))
			}

			const exportWarning = "WARNING: this output grants full account access; store it encrypted."

			if _, err := fmt.Fprintln(cmd.ErrOrStderr(), exportWarning); err != nil {
				return fail(cmd, fmt.Errorf("print warning: %w", err))
			}

			if _, err := cmd.OutOrStdout().Write(append(blob, '\n')); err != nil {
				return fail(cmd, fmt.Errorf("print session: %w", err))
			}

			return nil
		},
	}
}
