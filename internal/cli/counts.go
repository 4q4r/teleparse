package cli

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/4q4r/teleparse/internal/scan"

	"github.com/spf13/cobra"
)

// countRow is one rendered per-chat match count: matches includes the
// prior-run cached manifest rows for chats walked incrementally.
type countRow struct {
	chatID  int64
	title   string
	matches int
	cached  int
}

// countTotals aggregates the rendered count table; cached sums the
// manifest rows earlier runs already matched and incremental reports
// whether any chat was walked from its watermark.
type countTotals struct {
	chats         int
	chatsWithHits int
	files         int
	cached        int
	incremental   bool
	duplicates    int
}

// countJSON is the --format json envelope of the counts table.
type countJSON struct {
	Chats            []countRowJSON `json:"chats"`
	TotalChats       int            `json:"total_chats"`
	ChatsWithMatches int            `json:"chats_with_matches"`
	TotalMatches     int            `json:"total_matches"`
	Cached           int            `json:"cached"`
	Incremental      bool           `json:"incremental"`
	Duplicates       int            `json:"duplicates"`
}

// countRowJSON is one chat entry of the json counts envelope.
type countRowJSON struct {
	ChatID  int64  `json:"chat_id"`
	Title   string `json:"title"`
	Matches int    `json:"matches"`
	Cached  int    `json:"cached"`
}

// countsTableColumnPad is the gutter between padded table columns,
// matching the tabwriter settings used elsewhere in the CLI.
const countsTableColumnPad = 2

// printCounts renders per-chat match counts in the requested output
// format: a padded table (dim header, green counts, dim zero rows, bold
// TOTAL row) by default, stable-field json, or greppable key: value
// blocks. duplicates counts files this run already tracked under another
// message; colors apply only to the table format.
func printCounts(cmd *cobra.Command, app *App, collector *walkCollector, targets []scan.Target, duplicates int) error {
	rows := countRows(collector, targets)
	totals := countTotalsFor(rows, duplicates, len(collector.incremental) > 0)

	switch app.outputFormat() {
	case FormatJSON:
		return printJSON(cmd, countsEnvelope(rows, totals))
	case FormatPlain:
		return printCountsPlain(cmd, rows, totals)
	default:
		return printCountsTable(cmd, app.style, rows, totals)
	}
}

// chatLabel renders a chat's display name, falling back to chat_<id> when
// the title is blank so progress lines, tables and summaries never show an
// empty name.
func chatLabel(chatID int64, title string) string {
	if strings.TrimSpace(title) == "" {
		return "chat_" + strconv.FormatInt(chatID, 10)
	}

	return title
}

// countRows computes per-chat match counts — new matches plus the cached
// manifest rows of incrementally walked chats — sorted by matches desc
// with zero-match chats last; chat id breaks ties so output stays stable.
func countRows(collector *walkCollector, targets []scan.Target) []countRow {
	rows := make([]countRow, 0, len(targets))

	for _, target := range targets {
		cached := int(collector.cached[target.Chat.ID])
		rows = append(rows, countRow{
			chatID:  target.Chat.ID,
			title:   chatLabel(target.Chat.ID, target.Chat.Title),
			matches: countFor(collector, target.Chat.ID) + cached,
			cached:  cached,
		})
	}

	sort.SliceStable(rows, func(a, b int) bool {
		if rows[a].matches != rows[b].matches {
			return rows[a].matches > rows[b].matches
		}

		return rows[a].chatID < rows[b].chatID
	})

	return rows
}

// countTotalsFor aggregates chats scanned, chats holding at least one
// match, the total file count, its cached share and the files this run
// already tracked under another message.
func countTotalsFor(rows []countRow, duplicates int, incremental bool) countTotals {
	totals := countTotals{chats: len(rows), duplicates: duplicates, incremental: incremental}

	for _, row := range rows {
		totals.files += row.matches
		totals.cached += row.cached

		if row.matches > 0 {
			totals.chatsWithHits++
		}
	}

	return totals
}

// countsEnvelope maps rows into the json payload keeping the table sort.
func countsEnvelope(rows []countRow, totals countTotals) countJSON {
	chats := make([]countRowJSON, 0, len(rows))

	for _, row := range rows {
		chats = append(chats, countRowJSON{
			ChatID: row.chatID, Title: row.title, Matches: row.matches, Cached: row.cached,
		})
	}

	return countJSON{
		Chats:            chats,
		TotalChats:       totals.chats,
		ChatsWithMatches: totals.chatsWithHits,
		TotalMatches:     totals.files,
		Cached:           totals.cached,
		Incremental:      totals.incremental,
		Duplicates:       totals.duplicates,
	}
}

// printCountsTable renders the padded, colored counts table: header dim,
// rows with matches green, zero rows fully dim and a bold TOTAL row.
// Padding derives from plain text widths so ANSI codes never skew the
// columns.
func printCountsTable(cmd *cobra.Command, styler Styler, rows []countRow, totals countTotals) error {
	totalText := strconv.Itoa(totals.chats) + " chats, " +
		strconv.Itoa(totals.chatsWithHits) + " with matches"

	if totals.duplicates > 0 {
		totalText += ", " + strconv.Itoa(totals.duplicates) + " duplicates"
	}

	widths := countsColumnWidths(rows, totalText)

	styled := make([]string, 0, len(rows)+2)
	styled = append(styled, countsHeaderLine(styler, widths))

	for _, row := range rows {
		styled = append(styled, countsRowLine(styler, row, widths))
	}

	styled = append(styled, countsTotalLine(styler, totalText, totals, widths))

	for idx := range styled {
		if err := printLine(cmd, "%s\n", styled[idx]); err != nil {
			return fmt.Errorf("write counts row %d: %w", idx, err)
		}
	}

	return nil
}

// countsColumnWidths resolves the plain-text width of the chat id and
// title columns across header, data and TOTAL rows.
func countsColumnWidths(rows []countRow, totalText string) [2]int {
	cells := make([]string, 0, len(rows)+2)
	cells = append(cells, "CHAT", "TOTAL")

	for _, row := range rows {
		cells = append(cells, strconv.FormatInt(row.chatID, 10))
	}

	titleCells := append([]string{"TITLE", totalText}, rowTitles(rows)...)

	var widths [2]int

	for _, cell := range cells {
		widths[0] = max(widths[0], len(cell))
	}

	for _, cell := range titleCells {
		widths[1] = max(widths[1], len(cell))
	}

	return widths
}

// rowTitles extracts the title column of every row.
func rowTitles(rows []countRow) []string {
	titles := make([]string, 0, len(rows))

	for _, row := range rows {
		titles = append(titles, row.title)
	}

	return titles
}

// countsHeaderLine renders the dim header row.
func countsHeaderLine(styler Styler, widths [2]int) string {
	return padCountCell(styler.Dim("CHAT"), widths[0]) +
		padCountCell(styler.Dim("TITLE"), widths[1]) +
		styler.Dim("MATCHES")
}

// cachedSuffix renders the dim "(+N cached)" annotation for counts that
// include rows served from the manifest cache.
func cachedSuffix(styler Styler, cached int) string {
	if cached == 0 {
		return ""
	}

	return " " + styler.Dim("(+"+strconv.Itoa(cached)+" cached)")
}

// countsRowLine renders one data row: green counts when the chat
// matched, the whole row dim otherwise.
func countsRowLine(styler Styler, row countRow, widths [2]int) string {
	if row.matches == 0 {
		return padCountCell(styler.Dim(strconv.FormatInt(row.chatID, 10)), widths[0]) +
			padCountCell(styler.Dim(row.title), widths[1]) +
			styler.Dim(strconv.Itoa(row.matches))
	}

	return padCountCell(strconv.FormatInt(row.chatID, 10), widths[0]) +
		padCountCell(row.title, widths[1]) +
		styler.Success(strconv.Itoa(row.matches)) + cachedSuffix(styler, row.cached)
}

// countsTotalLine renders the bold TOTAL row.
func countsTotalLine(styler Styler, totalText string, totals countTotals, widths [2]int) string {
	return padCountCell(styler.Bold("TOTAL"), widths[0]) +
		padCountCell(styler.Dim(totalText), widths[1]) +
		styler.Bold(strconv.Itoa(totals.files)) + cachedSuffix(styler, totals.cached)
}

// padCountCell pads a (possibly styled) cell to width; padding is
// appended after the ANSI codes so it never counts toward them.
func padCountCell(cell string, width int) string {
	visible := len(strings.TrimFunc(cell, func(r rune) bool { return r == '\x1b' || (r >= '\x00' && r <= '\x1f') }))
	if visible >= width {
		return cell + strings.Repeat(" ", countsTableColumnPad)
	}

	return cell + strings.Repeat(" ", width-visible+countsTableColumnPad)
}

// printCountsPlain renders key: value blocks plus totals, no padding.
func printCountsPlain(cmd *cobra.Command, rows []countRow, totals countTotals) error {
	keys := []string{"chat_id", "title", "matches"}

	blocks := make([][]string, 0, len(rows))

	for _, row := range rows {
		blocks = append(blocks, []string{
			strconv.FormatInt(row.chatID, 10),
			row.title,
			strconv.Itoa(row.matches),
		})
	}

	if err := printPlainRows(cmd, keys, blocks); err != nil {
		return fmt.Errorf("write plain counts: %w", err)
	}

	pairs := [][2]string{
		{"total_chats", strconv.Itoa(totals.chats)},
		{"chats_with_matches", strconv.Itoa(totals.chatsWithHits)},
		{"total_matches", strconv.Itoa(totals.files)},
		{"cached", strconv.Itoa(totals.cached)},
		{"incremental", yesNo(totals.incremental)},
		{"duplicates", strconv.Itoa(totals.duplicates)},
	}

	if err := printPlainPairs(cmd, pairs); err != nil {
		return fmt.Errorf("write plain counts totals: %w", err)
	}

	return nil
}

// printScanSummary renders the final dry-run/scan outcome line to stderr
// (colored via the stderr styler) so machine-readable stdout stays clean;
// silent mode suppresses it. The matched total includes rows served from
// the manifest cache, andotated when any were.
func printScanSummary(
	cmd *cobra.Command, app *App, chats int, collector *walkCollector, duplicates int, took time.Duration,
) error {
	if app.silentMode(cmd) {
		return nil
	}

	cached := int64(0)

	for _, value := range collector.cached {
		cached += value
	}

	annotation := ""
	if cached > 0 {
		annotation = " " + app.errStyle.Dim("(+"+strconv.FormatInt(cached, 10)+" cached)")
	}

	dupAnnotation := ""
	if duplicates > 0 {
		dupAnnotation = ", " + app.errStyle.Dim("duplicates:") + " " +
			app.errStyle.Success(strconv.Itoa(duplicates))
	}

	line := app.errStyle.Dim("scanned:") + " " + app.errStyle.Success(strconv.Itoa(chats)+" chats") + ", " +
		app.errStyle.Dim("matched:") + " " +
		app.errStyle.Success(strconv.Itoa(len(collector.items)+int(cached))+" files") + annotation + dupAnnotation + ", " +
		app.errStyle.Dim("took") + " " + scanClock(took) + "\n"

	if _, err := fmt.Fprint(cmd.ErrOrStderr(), line); err != nil {
		return fmt.Errorf("print scan summary: %w", err)
	}

	return nil
}
