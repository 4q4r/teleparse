package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/filters"
	"github.com/4q4r/teleparse/internal/scan"
	"github.com/4q4r/teleparse/internal/store"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newCountsFixture builds a collector and targets for three chats: Docs
// (5 matches), News (2) and Zero (0).
func newCountsFixture() (*walkCollector, []scan.Target) {
	collector := &walkCollector{items: []store.MediaItem{
		{ChatID: 1}, {ChatID: 1}, {ChatID: 3}, {ChatID: 3}, {ChatID: 3}, {ChatID: 3}, {ChatID: 3},
	}}

	targets := []scan.Target{
		{Chat: filters.Chat{ID: 1, Title: "News"}},
		{Chat: filters.Chat{ID: 2, Title: "Zero"}},
		{Chat: filters.Chat{ID: 3, Title: "Docs"}},
	}

	return collector, targets
}

func TestPrintCountsTableSortedWithTotal(t *testing.T) {
	t.Parallel()

	cmd, out := newOutCmd()

	collector, targets := newCountsFixture()

	require.NoError(t, printCounts(cmd, &App{style: NewStyler(false)}, collector, targets))

	got := out.String()
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")

	require.Len(t, lines, 5)
	assert.Equal(t, []string{"CHAT", "TITLE", "MATCHES"}, strings.Fields(lines[0]), "header row")
	assert.Equal(t, []string{"3", "Docs", "5"}, strings.Fields(lines[1]), "most matches first")
	assert.Equal(t, []string{"1", "News", "2"}, strings.Fields(lines[2]))
	assert.Equal(t, []string{"2", "Zero", "0"}, strings.Fields(lines[3]), "zero-match chats sink to the end")
	assert.Equal(t, []string{"TOTAL", "3", "chats,", "2", "with", "matches", "7"}, strings.Fields(lines[4]))

	// The matches column holds one fixed offset across every row.
	matchesAt := strings.Index(lines[0], "MATCHES")
	for _, idx := range []int{1, 2, 3, 4} {
		assert.Equal(t, matchesAt, len(lines[idx])-1, "matches column aligns on line %d", idx)
	}

	assert.NotContains(t, got, "\x1b[", "disabled styler renders plain")
}

func TestPrintCountsTableColoredWhenStylerEnabled(t *testing.T) {
	t.Parallel()

	cmd, out := newOutCmd()

	collector, targets := newCountsFixture()

	require.NoError(t, printCounts(cmd, &App{style: NewStyler(true)}, collector, targets))

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	require.Len(t, lines, 5)

	assert.Contains(t, lines[0], "\x1b[", "header renders dim")
	assert.Contains(t, lines[1], "\x1b[", "match counts render green")
	assert.Contains(t, lines[3], "\x1b[", "zero rows render dim")
	assert.Contains(t, lines[4], "\x1b[", "TOTAL row renders bold")

	for _, plain := range []string{"Docs", "Zero", "TOTAL", "MATCHES"} {
		assert.Contains(t, out.String(), plain, "colored rows keep the plain content")
	}
}

func TestPrintCountsJSON(t *testing.T) {
	t.Parallel()

	cmd, out := newOutCmd()

	collector, targets := newCountsFixture()

	require.NoError(t, printCounts(cmd, &App{style: NewStyler(false), format: FormatJSON}, collector, targets))

	var payload struct {
		Chats []struct {
			ChatID  int64  `json:"chat_id"`
			Title   string `json:"title"`
			Matches int    `json:"matches"`
		} `json:"chats"`
		TotalChats       int `json:"total_chats"`
		ChatsWithMatches int `json:"chats_with_matches"`
		TotalMatches     int `json:"total_matches"`
	}

	require.NoError(t, json.Unmarshal(out.Bytes(), &payload))

	require.Len(t, payload.Chats, 3)
	assert.Equal(t, "Docs", payload.Chats[0].Title, "sorted by matches desc")
	assert.Equal(t, 5, payload.Chats[0].Matches)
	assert.Equal(t, "Zero", payload.Chats[2].Title)
	assert.Zero(t, payload.Chats[2].Matches)
	assert.Equal(t, 3, payload.TotalChats)
	assert.Equal(t, 2, payload.ChatsWithMatches)
	assert.Equal(t, 7, payload.TotalMatches)
}

func TestPrintCountsPlain(t *testing.T) {
	t.Parallel()

	cmd, out := newOutCmd()

	collector, targets := newCountsFixture()

	require.NoError(t, printCounts(cmd, &App{style: NewStyler(false), format: FormatPlain}, collector, targets))

	got := out.String()

	assert.NotContains(t, got, "\t", "plain render must not pad")
	assert.NotContains(t, got, "\x1b[", "plain render carries no colors")

	for _, want := range []string{
		"chat_id: 3\n", "title: Docs\n", "matches: 5\n",
		"total_chats: 3\n", "chats_with_matches: 2\n", "total_matches: 7\n",
	} {
		assert.Contains(t, got, want)
	}
}

func TestPrintCountsEmptyTargetsRenderTotalsRow(t *testing.T) {
	t.Parallel()

	cmd, out := newOutCmd()

	require.NoError(t, printCounts(cmd, &App{style: NewStyler(false)}, &walkCollector{}, nil))

	got := out.String()
	assert.Contains(t, got, "CHAT")
	assert.Contains(t, got, "TOTAL")
	assert.Contains(t, got, "0 chats, 0 with matches")
}

func TestPrintScanSummaryLine(t *testing.T) {
	t.Parallel()

	cmd, errBuf := newErrCmd()

	require.NoError(t, printScanSummary(cmd, &App{errStyle: NewStyler(false)}, 254, 891, 72*time.Second))

	assert.Equal(t, "scanned: 254 chats, matched: 891 files, took 1m12s\n", errBuf.String())
}

func TestPrintScanSummaryColoredAndSilentSuppressed(t *testing.T) {
	t.Parallel()

	cmd, errBuf := newErrCmd()

	require.NoError(t, printScanSummary(cmd, &App{errStyle: NewStyler(true)}, 3, 7, 2*time.Second))
	assert.Contains(t, errBuf.String(), "\x1b[")
	assert.Contains(t, errBuf.String(), "scanned:")
	assert.Contains(t, errBuf.String(), "3 chats")

	silent := &cobra.Command{}
	silent.Flags().Bool("silent-output", true, "")

	quietBuf := &strings.Builder{}
	silent.SetErr(quietBuf)

	require.NoError(t, printScanSummary(silent, &App{}, 3, 7, time.Second))
	assert.Empty(t, quietBuf.String(), "silent mode suppresses the summary")
}

func TestStylersForSplitsStdoutAndStderrSinks(t *testing.T) {
	t.Parallel()

	base := StyleOptions{}

	outStyle, errStyle := stylersFor(base, true, true)
	assert.True(t, outStyle.Enabled())
	assert.True(t, errStyle.Enabled())

	// Stdout piped (json into jq), stderr still a terminal: only the
	// stderr styler keeps colors.
	outStyle, errStyle = stylersFor(base, false, true)
	assert.False(t, outStyle.Enabled())
	assert.True(t, errStyle.Enabled())

	outStyle, errStyle = stylersFor(base, true, false)
	assert.True(t, outStyle.Enabled())
	assert.False(t, errStyle.Enabled())
}
