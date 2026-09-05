package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/4q4r/teleparse/internal/tg"

	"github.com/gotd/td/telegram"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// errNotAuthorized marks a missing session without aborting other output.
var errNotAuthorized = errors.New("account is not logged in")

// errQRExclusive rejects combining the QR method with the other login paths.
var errQRExclusive = errors.New("--qr cannot be combined with --phone, --import-telethon or --import-tdesktop")

// errQRTimeoutMin rejects --timeout values below the 30s floor.
var errQRTimeoutMin = errors.New("--timeout must be at least 30s")

// errQRTimeoutNeedsQR rejects --timeout without the QR login it bounds.
var errQRTimeoutNeedsQR = errors.New("--timeout requires --qr")

// QR login deadline defaults and bounds.
const (
	qrTimeoutDefault = 5 * time.Minute
	qrTimeoutMin     = 30 * time.Second
)

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
		tdataDir   string
		useQR      bool
		qrTimeout  time.Duration
	)

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Interactive login: phone, code, optional 2FA password, or QR",
		Example: "  teleparse auth login\n" +
			"  teleparse auth login --phone +15551234567\n" +
			"  teleparse auth login --qr --timeout 2m\n" +
			"  teleparse auth login --import-telethon old.session.sqlite\n" +
			"  teleparse auth login --import-tdesktop ~/tdata/tdata",
		RunE: func(cmd *cobra.Command, _ []string) error {
			account := app.cfg.Auth.Account

			if cmd.Flags().Changed("timeout") && !useQR {
				return fail(cmd, fmt.Errorf("%s: %w", qrTimeout, errQRTimeoutNeedsQR))
			}

			if qrTimeout < qrTimeoutMin {
				return fail(cmd, fmt.Errorf("%s: %w", qrTimeout, errQRTimeoutMin))
			}

			if useQR && (phone != "" || importPath != "" || tdataDir != "") {
				return fail(cmd, errQRExclusive)
			}

			creds, err := resolveCredsInteractive(
				term.IsTerminal(int(os.Stdin.Fd())),
				notifyingPrompter{newStdPrompter()},
			)
			if err != nil {
				return fail(cmd, err)
			}

			plan, err := resolveLoginMethod(loginMenuText(app.style), useQR, phone, importPath, tdataDir,
				term.IsTerminal(int(os.Stdin.Fd())), newStdPrompter().Line)
			if err != nil {
				return fail(cmd, err)
			}

			if err := runSessionImports(app, cmd, account, plan); err != nil {
				return err
			}

			if plan.QR {
				info, err := tg.QRLogin(cmd.Context(), account, creds, tg.QROptions{
					Timeout: qrTimeout,
					ASCII:   app.noASCII,
					TTY:     term.IsTerminal(int(os.Stderr.Fd())),
					Out:     cmd.ErrOrStderr(),
				}, app.cfg, app.paths)
				if err != nil {
					return fail(cmd, err)
				}

				return printLine(cmd, "%s account %s is logged in via QR (user %d, %s)\n",
					app.style.Success("+"), account, info.ID, strings.TrimSpace(info.FirstName+" "+info.LastName))
			}

			if err := tg.Login(cmd.Context(), account, creds, plan.Phone, newStdPrompter(), app.cfg, app.paths); err != nil {
				return fail(cmd, err)
			}

			return printLine(cmd, "%s account %s is logged in\n", app.style.Success("+"), account)
		},
	}
	cmd.Flags().StringVar(&phone, "phone", "", "phone number in +E.164 format (asked interactively when empty)")
	cmd.Flags().StringVar(&importPath, "import-telethon", "", "import a Telethon .session SQLite file before login")
	cmd.Flags().StringVar(&tdataDir, "import-tdesktop",
		"", "import a Telegram Desktop tdata directory before login (multiple accounts: pick interactively)")
	cmd.Flags().BoolVar(&useQR, "qr", false, "log in by scanning a QR code with Telegram on another device")
	cmd.Flags().DurationVar(&qrTimeout, "timeout", qrTimeoutDefault,
		"QR login timeout (minimum 30s; the token auto-refreshes while waiting)")

	return cmd
}

// runSessionImports executes the tdata and Telethon imports requested by
// the login plan, reporting each with a success line.
func runSessionImports(app *App, cmd *cobra.Command, account string, plan loginPlan) error {
	if plan.TDataDir != "" {
		storage, err := tg.NewAccountManager(app.paths.AccountsDir).Storage(account)
		if err != nil {
			return fail(cmd, err)
		}

		if err := tg.TDesktopSessionImport(cmd.Context(), plan.TDataDir, newStdPrompter(), storage); err != nil {
			return fail(cmd, err)
		}

		if err := printLine(cmd, "%s imported tdata session for account %s\n",
			app.style.Success("+"), account); err != nil {
			return fail(cmd, err)
		}
	}

	if plan.TelethonPath != "" {
		storage, err := tg.NewAccountManager(app.paths.AccountsDir).Storage(account)
		if err != nil {
			return fail(cmd, err)
		}

		if err := tg.TelethonSessionImport(cmd.Context(), plan.TelethonPath, storage); err != nil {
			return fail(cmd, err)
		}

		if err := printLine(cmd, "%s imported telethon session for account %s\n",
			app.style.Success("+"), account); err != nil {
			return fail(cmd, err)
		}
	}

	return nil
}

func authLogoutCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "logout",
		Short:   "Log out and delete the local session",
		Example: "  teleparse auth logout --account work",
		RunE: func(cmd *cobra.Command, _ []string) error {
			account := app.cfg.Auth.Account

			creds, err := app.resolveCreds()
			if err != nil {
				return fail(cmd, err)
			}

			if err := tg.Logout(cmd.Context(), account, creds, app.cfg, app.paths); err != nil {
				return fail(cmd, err)
			}

			return printLine(cmd, "%s account %s logged out\n", app.style.Success("+"), account)
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

			creds, err := app.resolveCreds()
			if err != nil {
				return fail(cmd, err)
			}

			manager := tg.NewAccountManager(app.paths.AccountsDir)

			storage, err := manager.Storage(account)
			if err != nil {
				return fail(cmd, err)
			}

			err = tg.Run(cmd.Context(), account, creds, app.cfg, app.paths, func(
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
