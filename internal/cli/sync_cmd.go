package cli

import (
	"github.com/spf13/cobra"
)

func syncCmd(app *App) *cobra.Command {
	var (
		filterSet filterFlags
		flags     dlRunFlags
	)

	cmd := &cobra.Command{
		Use:     "sync [CHATS]...",
		Short:   "Incremental sync: only messages past per-chat watermarks",
		Example: "  teleparse sync @durov\n  teleparse sync all --chat-type channel",
		Args:    cobra.ArbitraryArgs,
		RunE: func(c *cobra.Command, args []string) error {
			return runDownloadCommand(app, c, args, &filterSet, flags, runMode{
				syncMode: true,
				fullWalk: flags.full,
			}, "", nil)
		},
	}
	cmd.Flags().String("profile", "", "named filter profile overlay")
	addFullWalkFlag(cmd, &flags)
	addSilentOutputMirror(cmd)
	addFilterFlags(cmd, &filterSet)

	return cmd
}
