package cli

import (
	"github.com/spf13/cobra"
)

func syncCmd(app *App) *cobra.Command {
	var filterSet filterFlags

	cmd := &cobra.Command{
		Use:   "sync [CHATS]...",
		Short: "Incremental sync: only messages past per-chat watermarks",
		Args:  cobra.ArbitraryArgs,
		RunE: func(c *cobra.Command, args []string) error {
			return runDownloadCommand(app, c, args, &filterSet, dlRunFlags{}, runMode{syncMode: true}, "")
		},
	}
	cmd.Flags().String("profile", "", "named filter profile overlay")
	addFilterFlags(cmd, &filterSet)

	return cmd
}
