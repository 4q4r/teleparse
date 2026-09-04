package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

// errNotImplemented marks command stubs pending their implementation phase.
var errNotImplemented = errors.New("not implemented yet — this command is built in an upcoming phase")

// runPayloadDecodeTarget is documented in dl_cmd.go; this file keeps only
// the stubs that remain future work (profile management).

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
