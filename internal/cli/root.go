// Package cli wires the teleparse command tree.
package cli

import (
	"fmt"
	"teleparse/internal/config"

	"github.com/spf13/cobra"
)

// version is set at build time via -ldflags.
var version = "dev"

// App carries shared state across commands.
type App struct {
	cfg   *config.Config
	paths *config.Paths
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
	app := &App{}
	root := &cobra.Command{
		Use:   "teleparse",
		Short: "Telegram userbot media parser with a powerful filter engine",
		Long: "teleparse scans chats of your personal Telegram accounts and downloads media\n" +
			"matching any combination of ~150 filters. Multi-account, proxy-friendly,\n" +
			"anti-ban paced. Sessions live in ~/.config/teleparse.",
		Version:       version,
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
			if v, _ := cmd.Flags().GetString("root"); v != "" {
				cfg.Output.Root = v
			}
			app.cfg, app.paths = cfg, paths
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
		proxyCmd(app),
		doctorCmd(app),
	)

	return root
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
