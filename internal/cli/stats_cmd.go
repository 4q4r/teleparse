package cli

import (
	"fmt"
	"teleparse/internal/store"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func statsCmd(app *App) *cobra.Command {
	var chatID int64

	cmd := &cobra.Command{
		Use:     "stats",
		Short:   "Download statistics per chat and totals",
		Example: "  teleparse stats\n  teleparse stats --chat 123456789",
		Args:    cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runStats(app, c, chatID)
		},
	}
	cmd.Flags().Int64Var(&chatID, "chat", 0, "restrict totals to one chat id")

	return cmd
}

func runStats(app *App, cmd *cobra.Command, chatID int64) error {
	state, err := openStore(app)
	if err != nil {
		return fail(cmd, err)
	}

	defer func() { _ = state.Close() }()

	counts, err := state.Counts(cmd.Context())
	if err != nil {
		return fail(cmd, fmt.Errorf("count media: %w", err))
	}

	perChat, err := state.StatsPerChat(cmd.Context())
	if err != nil {
		return fail(cmd, fmt.Errorf("stats per chat: %w", err))
	}

	if err := printStatusCounts(cmd, counts); err != nil {
		return fail(cmd, err)
	}

	return printPerChat(cmd, perChat, chatID)
}

func printStatusCounts(cmd *cobra.Command, counts map[string]int) error {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)

	if _, err := fmt.Fprintln(writer, "STATUS\tROWS"); err != nil {
		return fmt.Errorf("write status header: %w", err)
	}

	for _, status := range sortedStatuses(counts) {
		if _, err := fmt.Fprintf(writer, "%s\t%d\n", status, counts[status]); err != nil {
			return fmt.Errorf("write status row: %w", err)
		}
	}

	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush status table: %w", err)
	}

	return printLine(cmd, "\n")
}

func printPerChat(cmd *cobra.Command, stats []store.ChatStats, chatID int64) error {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)

	if _, err := fmt.Fprintln(writer, "CHAT\tTITLE\tDONE\tBYTES"); err != nil {
		return fmt.Errorf("write chat header: %w", err)
	}

	var (
		totalFiles int64
		totalBytes int64
	)

	for _, row := range stats {
		if chatID != 0 && row.ChatID != chatID {
			continue
		}

		if _, err := fmt.Fprintf(writer, "%d\t%s\t%d\t%s\n",
			row.ChatID, row.Title, row.DoneCount, humanBytes(row.DoneBytes)); err != nil {
			return fmt.Errorf("write chat row: %w", err)
		}

		totalFiles += row.DoneCount
		totalBytes += row.DoneBytes
	}

	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush chat table: %w", err)
	}

	return printLine(cmd, "total: %d file(s), %s\n", totalFiles, humanBytes(totalBytes))
}

// statusOrder lists the media lifecycle statuses in report order.
func statusOrder() []string {
	return []string{
		"discovered", "queued", "downloading", "done", "failed", "skipped",
	}
}

func sortedStatuses(counts map[string]int) []string {
	known := statusOrder()

	for status := range counts {
		known = appendStatus(known, status)
	}

	return known
}

func appendStatus(known []string, status string) []string {
	for _, existing := range known {
		if existing == status {
			return known
		}
	}

	return append(known, status)
}
