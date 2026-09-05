// Package cli wires the teleparse command tree.
package cli

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/4q4r/teleparse/internal/config"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// version is injected at release time via -ldflags; when absent ("dev"),
// binaries built with `go install pkg@version` fall back to the module
// version baked into the build info.
var version = "dev"

// effectiveVersion resolves the runtime version: ldflags value first,
// then the go-install module version, then "dev".
func effectiveVersion() string {
	if version != "dev" {
		return version
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}

	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}

	return version
}

// App carries shared state across commands.
type App struct {
	cfg      *config.Config
	paths    *config.Paths
	format   OutputFormat
	noASCII  bool
	style    Styler
	errStyle Styler
	root     *cobra.Command
}

// outputFormat reports the effective --format value (validated once in
// PersistentPreRunE; table when the flag is absent).
func (a *App) outputFormat() OutputFormat {
	if a.format == "" {
		return FormatTable
	}

	return a.format
}

// silentMode reports whether output-noise suppression was requested. The
// dl/scan/sync family carries a local -s mirror (silent-output) because the
// inherited --silent long name is shadowed there by the silently-sent
// message filter; on every other command the global --silent/-s wins.
func (a *App) silentMode(cmd *cobra.Command) bool {
	if v, err := cmd.Flags().GetBool("silent-output"); err == nil && v {
		return true
	}

	if v, err := cmd.Flags().GetBool("silent"); err == nil && v {
		return true
	}

	return false
}

// New builds the root command.
func New() *cobra.Command {
	return newApp().root
}

// Execute runs the root command, prints failures with a styled error prefix
// and returns the process exit code. It exists so main.go never formats
// errors itself: the Styler lives on the App, enabled by flag parsing.
func Execute() int {
	app := newApp()

	if err := app.root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, app.style.ErrorPrefix(), err)

		return exitFailure
	}

	return exitSuccess
}

// Process exit codes returned by Execute.
const (
	exitSuccess = 0
	exitFailure = 1
)

// newApp builds the shared App with its root command tree.
func newApp() *App {
	app := &App{}
	root := &cobra.Command{
		Use:   "teleparse",
		Short: "Telegram userbot media parser with a powerful filter engine",
		Long: "teleparse scans chats of your personal Telegram accounts and downloads media\n" +
			"matching any combination of ~150 filters. Multi-account, proxy-friendly,\n" +
			"anti-ban paced. Sessions live in ~/.config/teleparse.",
		Version:       effectiveVersion(),
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			path, _ := cmd.Flags().GetString("config")
			cfg, paths, err := config.Load(path)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if v, _ := cmd.Flags().GetString("account"); v != "" {
				cfg.Auth.Account = v
			}
			if v, _ := cmd.Flags().GetString("proxy"); v != "" {
				cfg.Net.Proxy = v
				cfg.Net.ProxySource = "flag"
			}
			if rootFlag, _ := cmd.Flags().GetString("root"); rootFlag != "" {
				cfg.Output.Root = rootFlag

				// Load built paths from the pre-flag config; the resolved
				// downloads root must follow the flag too (downloads,
				// doctor and the blob store all read paths.Downloads).
				paths.Downloads = rootFlag
			}
			rawFormat, _ := cmd.Flags().GetString("format")

			format, err := ParseOutputFormat(rawFormat)
			if err != nil {
				return err
			}
			app.cfg, app.paths = cfg, paths
			app.format = format
			app.noASCII, _ = cmd.Flags().GetBool("no-ascii")

			noColor, _ := cmd.Flags().GetBool("no-color")

			base := StyleOptions{
				NoColorFlag: noColor,
				NoASCIIFlag: app.noASCII,
				NoColorEnv:  os.Getenv("NO_COLOR") != "",
			}

			app.style, app.errStyle = stylersFor(base,
				term.IsTerminal(int(os.Stdout.Fd())),
				term.IsTerminal(int(os.Stderr.Fd())))

			return nil
		},
	}
	root.PersistentFlags().String("config", "", "config file path (default ~/.config/teleparse/config.toml)")
	root.PersistentFlags().String("account", "", "account name (default from config)")
	root.PersistentFlags().String("proxy", "", "proxy URL: socks5:// socks4:// http:// mtproto:// webproxy://")
	root.PersistentFlags().String("root", "", "downloads root (overrides config)")
	root.PersistentFlags().BoolP("silent", "s", false,
		"suppress progress UI, per-item lines and summaries (errors and actionable\n"+
			"results still print; on dl/scan/sync use -s: plain --silent there filters\n"+
			"silently-sent messages)")
	root.PersistentFlags().String("format", "table",
		"output format for chats list, runs list|show, stats, proxy show|test,\nping and auth list: table | json | plain")
	root.PersistentFlags().Bool("no-ascii", false,
		"plain ASCII output: line-per-item progress instead of the live redraw UI,\n"+
			"ASCII-only tables and bars (also disables colors)")
	root.PersistentFlags().Bool("no-color", false,
		"disable colored output (also disabled by --no-ascii, the NO_COLOR\n"+
			"environment variable and non-terminal stdout)")

	root.AddCommand(
		authCmd(app),
		chatsCmd(app),
		scanCmd(app),
		dlCmd(app),
		syncCmd(app),
		resumeCmd(app),
		runsCmd(app),
		profileCmd(app),
		exportCmd(app),
		statsCmd(app),
		dedupeCmd(app),
		proxyCmd(app),
		pingCmd(app),
		doctorCmd(app),
	)

	app.root = root

	return app
}

// addFilterFlags registers every filters.Options field as a flag on cmd
// using the `flag` and `usage` struct tags (single source of truth).
func addFilterFlags(cmd *cobra.Command, opts *filterFlags) {
	registerReflectFlags(cmd.Flags(), &opts.opts)
	opts.cmd = cmd
}

func fail(cmd *cobra.Command, err error) error {
	cmd.SilenceUsage = false
	return err
}

// printLine writes one formatted line to cmd's stdout.
func printLine(cmd *cobra.Command, format string, args ...any) error {
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), format, args...); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	return nil
}
