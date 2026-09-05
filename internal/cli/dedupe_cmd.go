package cli

import (
	"fmt"

	"github.com/4q4r/teleparse/internal/download"

	"github.com/spf13/cobra"
)

// dedupeCmd maintains the hardlink blob store under the downloads root.
func dedupeCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dedupe",
		Short: "Inspect and clean the hardlink-dedupe blob store",
		Long: "With dedupe = hardlink (the default), teleparse stores each unique file once\n" +
			"under <root>/.teleparse/blobs and hardlinks it into every chat that sighted\n" +
			"it, so deleting any chat's copy never breaks the others.\n" +
			"stats reports blob-store usage; gc removes blobs whose last consumer is\n" +
			"gone (link count 1).",
	}

	cmd.AddCommand(dedupeStatsCmd(app), dedupeGcCmd(app))

	return cmd
}

// dedupeStatsCmd reports blob counts, total bytes and tracked vs orphaned.
func dedupeStatsCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Report blob counts, bytes and tracked vs orphaned blobs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			state, err := openStore(app)
			if err != nil {
				return err
			}

			defer func() { _ = state.Close() }()

			report, err := download.BlobStats(cmd.Context(), state, app.paths.Downloads)
			if err != nil {
				return fmt.Errorf("blob stats: %w", err)
			}

			return printLine(cmd, "blobs: %d (%s), tracked: %d, orphaned: %d\n",
				report.Blobs, humanBytes(report.Bytes), report.Tracked, report.Orphaned)
		},
	}
}

// dedupeGcCmd deletes blobs whose data no longer has any consumer.
func dedupeGcCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "gc",
		Short: "Delete blobs no chat copy still references",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			removed, freed, err := download.BlobGC(app.paths.Downloads)
			if err != nil {
				return fmt.Errorf("blob gc: %w", err)
			}

			return printLine(cmd, "removed: %d (%s freed)\n", removed, humanBytes(freed))
		},
	}
}
