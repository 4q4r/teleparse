package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

// errNotImplemented marks command stubs pending their implementation phase.
var errNotImplemented = errors.New("not implemented yet — this command is built in an upcoming phase")

func authCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage account sessions (login, logout, status)",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "login",
			Short: "Interactive login: phone, code, optional 2FA password",
			RunE: func(c *cobra.Command, _ []string) error {
				return fail(c, errNotImplemented)
			},
		},
		&cobra.Command{
			Use:   "logout",
			Short: "Log out and delete the local session",
			RunE: func(c *cobra.Command, _ []string) error {
				return fail(c, errNotImplemented)
			},
		},
		&cobra.Command{
			Use:   "status",
			Short: "Show who am I, session age, DC",
			RunE: func(c *cobra.Command, _ []string) error {
				return fail(c, errNotImplemented)
			},
		},
		&cobra.Command{
			Use:   "list",
			Short: "List known local sessions",
			RunE: func(c *cobra.Command, _ []string) error {
				return fail(c, errNotImplemented)
			},
		},
		&cobra.Command{
			Use:   "export",
			Short: "Export session as a portable string (treat as a secret)",
			RunE: func(c *cobra.Command, _ []string) error {
				return fail(c, errNotImplemented)
			},
		},
	)

	_ = app

	return cmd
}

func chatsCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "chats",
		Short: "List and inspect accessible chats",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List dialogs (all chats this account can see)",
			RunE: func(c *cobra.Command, _ []string) error {
				return fail(c, errNotImplemented)
			},
		},
		&cobra.Command{
			Use:   "show CHAT",
			Short: "Show chat metadata",
			RunE: func(c *cobra.Command, _ []string) error {
				return fail(c, errNotImplemented)
			},
		},
	)

	_ = app

	return cmd
}

func scanCmd(app *App) *cobra.Command {
	var filterSet filterFlags

	cmd := &cobra.Command{
		Use:   "scan [CHATS]...",
		Short: "Preview what dl would fetch (dry-run): plan + manifest rows, no downloads",
		Args:  cobra.ArbitraryArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return fail(c, errNotImplemented)
		},
	}
	addFilterFlags(cmd, &filterSet)

	_ = app

	return cmd
}

func dlCmd(app *App) *cobra.Command {
	var filterSet filterFlags

	cmd := &cobra.Command{
		Use:   "dl [CHATS]...",
		Short: "Download media matching filters (CHATS: all | @user | link | id | saved | glob)",
		Args:  cobra.ArbitraryArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return fail(c, errNotImplemented)
		},
	}
	cmd.Flags().Bool("dry-run", false, "list matches, download nothing (same as scan)")
	cmd.Flags().Bool("explain", false, "print which filters push down to the server vs run client-side")
	cmd.Flags().Bool("count-only", false, "only print per-chat match counts")
	cmd.Flags().Bool("takeout", false, "wrap session in takeout mode (lower flood limits)")
	addFilterFlags(cmd, &filterSet)

	_ = app

	return cmd
}

func syncCmd(app *App) *cobra.Command {
	var filterSet filterFlags

	cmd := &cobra.Command{
		Use:   "sync [CHATS]...",
		Short: "Incremental sync: only messages past per-chat watermarks",
		Args:  cobra.ArbitraryArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return fail(c, errNotImplemented)
		},
	}
	addFilterFlags(cmd, &filterSet)

	_ = app

	return cmd
}

func resumeCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resume [RUN_ID]",
		Short: "Resume parked (FloodWait) or crashed runs",
		RunE: func(c *cobra.Command, _ []string) error {
			return fail(c, errNotImplemented)
		},
	}
	_ = app

	return cmd
}

func runsCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runs",
		Short: "Inspect download runs",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use: "list", Short: "List runs",
			RunE: func(c *cobra.Command, _ []string) error { return fail(c, errNotImplemented) },
		},
		&cobra.Command{
			Use: "show RUN_ID", Short: "Show run details",
			RunE: func(c *cobra.Command, _ []string) error { return fail(c, errNotImplemented) },
		},
		&cobra.Command{
			Use: "clean", Short: "Delete finished runs from index",
			RunE: func(c *cobra.Command, _ []string) error { return fail(c, errNotImplemented) },
		},
	)

	_ = app

	return cmd
}

func profileCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage named filter profiles stored in config.toml",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use: "save NAME", Short: "Save current filter defaults as profile",
			RunE: func(c *cobra.Command, _ []string) error { return fail(c, errNotImplemented) },
		},
		&cobra.Command{
			Use: "list", Short: "List profiles",
			RunE: func(c *cobra.Command, _ []string) error { return fail(c, errNotImplemented) },
		},
		&cobra.Command{
			Use: "show NAME", Short: "Show profile filters",
			RunE: func(c *cobra.Command, _ []string) error { return fail(c, errNotImplemented) },
		},
		&cobra.Command{
			Use: "rm NAME", Short: "Remove profile",
			RunE: func(c *cobra.Command, _ []string) error { return fail(c, errNotImplemented) },
		},
	)

	_ = app

	return cmd
}

func exportCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export manifest (jsonl/csv)",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use: "jsonl", Short: "Export manifest as JSONL",
			RunE: func(c *cobra.Command, _ []string) error { return fail(c, errNotImplemented) },
		},
		&cobra.Command{
			Use: "csv", Short: "Export manifest as CSV",
			RunE: func(c *cobra.Command, _ []string) error { return fail(c, errNotImplemented) },
		},
	)

	_ = app

	return cmd
}

func statsCmd(app *App) *cobra.Command {
	_ = app

	return &cobra.Command{
		Use:   "stats",
		Short: "Download statistics per chat and totals",
		RunE: func(c *cobra.Command, _ []string) error {
			return fail(c, errNotImplemented)
		},
	}
}

func proxyCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Inspect and test proxy configuration",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "show",
			Short: "Print effective proxy settings",
			RunE: func(c *cobra.Command, _ []string) error {
				proxy := app.cfg.Net.Proxy
				if proxy == "" {
					proxy = "(direct)"
				}
				if _, err := fmt.Fprintln(c.OutOrStdout(), "proxy:", proxy); err != nil {
					return fmt.Errorf("print proxy: %w", err)
				}

				return nil
			},
		},
		&cobra.Command{
			Use: "test [URL]", Short: "Probe proxy connectivity to Telegram DCs",
			RunE: func(c *cobra.Command, _ []string) error { return fail(c, errNotImplemented) },
		},
	)

	return cmd
}

func doctorCmd(app *App) *cobra.Command {
	_ = app

	return &cobra.Command{
		Use:   "doctor",
		Short: "Check config, API credentials, session, disk, database",
		RunE: func(c *cobra.Command, _ []string) error {
			return fail(c, errNotImplemented)
		},
	}
}
