package cli

import (
	"fmt"
	"strconv"
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

	if err := renderStats(cmd, counts, perChat, chatID, app.outputFormat()); err != nil {
		return fail(cmd, err)
	}

	return nil
}

// chatStatJSON is the stable json shape of one per-chat stat row.
type chatStatJSON struct {
	ChatID int64  `json:"chat_id"`
	Title  string `json:"title"`
	Done   int64  `json:"done"`
	Bytes  int64  `json:"bytes"`
}

// statsTotalJSON is the stable json shape of the totals.
type statsTotalJSON struct {
	Files int64 `json:"files"`
	Bytes int64 `json:"bytes"`
}

// statsJSON is the stable json envelope of the stats command.
type statsJSON struct {
	Statuses map[string]int `json:"statuses"`
	PerChat  []chatStatJSON `json:"per_chat"`
	Total    statsTotalJSON `json:"total"`
}

// renderStats prints status counts, per-chat rows and totals in the
// requested format.
func renderStats(cmd *cobra.Command, counts map[string]int, stats []store.ChatStats,
	chatID int64, format OutputFormat,
) error {
	if format == FormatJSON {
		perChat := make([]chatStatJSON, 0, len(stats))

		var total statsTotalJSON

		for _, row := range stats {
			if chatID != 0 && row.ChatID != chatID {
				continue
			}

			perChat = append(perChat, chatStatJSON{
				ChatID: row.ChatID, Title: row.Title, Done: row.DoneCount, Bytes: row.DoneBytes,
			})

			total.Files += row.DoneCount
			total.Bytes += row.DoneBytes
		}

		return printJSON(cmd, statsJSON{Statuses: counts, PerChat: perChat, Total: total})
	}

	if format == FormatPlain {
		return renderStatsPlain(cmd, counts, stats, chatID)
	}

	if err := printStatusCounts(cmd, counts); err != nil {
		return err
	}

	return printPerChat(cmd, stats, chatID)
}

// renderStatsPlain prints greppable key: value stat lines.
func renderStatsPlain(cmd *cobra.Command, counts map[string]int, stats []store.ChatStats, chatID int64) error {
	pairs := make([][2]string, 0, len(counts)+len(stats)*3+2)

	for _, status := range sortedStatuses(counts) {
		pairs = append(pairs, [2]string{"status." + status, strconv.Itoa(counts[status])})
	}

	var (
		totalFiles int64
		totalBytes int64
	)

	for _, row := range stats {
		if chatID != 0 && row.ChatID != chatID {
			continue
		}

		pairs = append(pairs,
			[][2]string{
				{fmt.Sprintf("chat.%d.title", row.ChatID), row.Title},
				{fmt.Sprintf("chat.%d.done", row.ChatID), strconv.FormatInt(row.DoneCount, 10)},
				{fmt.Sprintf("chat.%d.bytes", row.ChatID), strconv.FormatInt(row.DoneBytes, 10)},
			}...,
		)

		totalFiles += row.DoneCount
		totalBytes += row.DoneBytes
	}

	pairs = append(pairs,
		[2]string{"total.files", strconv.FormatInt(totalFiles, 10)},
		[2]string{"total.bytes", strconv.FormatInt(totalBytes, 10)},
	)

	return printPlainPairs(cmd, pairs)
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
