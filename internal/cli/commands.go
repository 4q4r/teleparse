package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

// errNotImplemented marks command stubs pending their implementation phase.
var errNotImplemented = errors.New("not implemented yet — this command is built in an upcoming phase")

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
