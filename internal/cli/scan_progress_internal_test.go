package cli

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/4q4r/teleparse/internal/store"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scanClockFake pins the scan state's time source; tests advance it.
type scanClockFake struct{ now time.Time }

func (c *scanClockFake) Now() time.Time { return c.now }

func (c *scanClockFake) advance(d time.Duration) { c.now = c.now.Add(d) }

func newTestScanState(total int) (*scanState, *scanClockFake) {
	clock := &scanClockFake{now: time.Unix(0, 0)}

	return newScanState(clock.Now, total, false), clock
}

// walkFake advances the fake clock by took, marking the chat started and
// then completed like the real walk loop does.
func walkFake(state *scanState, clock *scanClockFake, title string, matches int, took time.Duration) {
	state.chatStart(title)
	clock.advance(took)
	state.chatDone(title, matches, took)
}

func TestScanStateETAIsMovingAverageTimesRemaining(t *testing.T) {
	t.Parallel()

	state, clock := newTestScanState(12)

	// Three walks of 2s each: average 2s, nine remaining -> 18s.
	walkFake(state, clock, "A", 1, 2*time.Second)
	walkFake(state, clock, "B", 1, 2*time.Second)
	walkFake(state, clock, "C", 1, 2*time.Second)

	eta, known := state.eta()
	require.True(t, known)
	assert.Equal(t, 18*time.Second, eta)
}

func TestScanStateETACapsDurationWindow(t *testing.T) {
	t.Parallel()

	state, clock := newTestScanState(scanEtaWindow + 5)

	for range scanEtaWindow + 5 {
		walkFake(state, clock, "chat", 1, time.Second)
	}

	assert.Len(t, state.durations, scanEtaWindow, "only the recent window feeds the average")
}

func TestScanStateETAUnknownBeforeFirstChatAndAfterDone(t *testing.T) {
	t.Parallel()

	state, clock := newTestScanState(3)

	_, known := state.eta()
	assert.False(t, known, "no durations yet")

	assert.Contains(t, strings.Join(state.lines(NewStyler(false)), "\n"), "ETA --")

	walkFake(state, clock, "A", 1, time.Second)
	walkFake(state, clock, "B", 1, time.Second)
	walkFake(state, clock, "C", 1, time.Second)

	_, known = state.eta()
	assert.False(t, known, "nothing remains")

	assert.NotContains(t, strings.Join(state.lines(NewStyler(false)), "\n"), "ETA",
		"a finished walk drops the ETA segment entirely")
}

func TestScanStateLinesRenderSettledChatsAndFooter(t *testing.T) {
	t.Parallel()

	state, clock := newTestScanState(61)

	walkFake(state, clock, "News Channel", 37, 2*time.Second)
	walkFake(state, clock, "Docs", 128, 3*time.Second)

	clock.advance(7300 * time.Millisecond)

	joined := strings.Join(state.lines(NewStyler(false)), "\n")

	// Average walk 2.5s over 59 remaining chats: 147s -> 2m27s; total
	// elapsed 2s + 3s + 7.3s = 12.3s.
	for _, want := range []string{
		"scanning 2/61", "matched 165 files", "ETA 2m27s", "12.3s",
		"+ News Channel (37)", "+ Docs (128)",
	} {
		assert.Contains(t, joined, want)
	}

	// Settled lines accumulate top-down, newest last, footer pinned below.
	assert.Greater(t, strings.Index(joined, "Docs (128)"), strings.Index(joined, "News Channel (37)"))

	lines := strings.Split(joined, "\n")
	require.NotEmpty(t, lines)
	assert.Contains(t, lines[len(lines)-1], "scanning 2/61", "footer renders last")
}

func TestScanStateLinesShowSingleTrailEntry(t *testing.T) {
	t.Parallel()

	state, clock := newTestScanState(10)

	walkFake(state, clock, "Only", 4, time.Second)

	joined := strings.Join(state.lines(NewStyler(false)), "\n")
	assert.Contains(t, joined, "+ Only (4)")
}

func TestScanStateLinesUnknownETARendersPlaceholder(t *testing.T) {
	t.Parallel()

	state, _ := newTestScanState(9)

	joined := strings.Join(state.lines(NewStyler(false)), "\n")
	assert.Contains(t, joined, "ETA --")
}

func TestScanStateLinesTruncateLongTitles(t *testing.T) {
	t.Parallel()

	state, clock := newTestScanState(4)

	walkFake(state, clock, strings.Repeat("x", scanTitleWidth+10), 1, time.Second)

	joined := strings.Join(state.lines(NewStyler(false)), "\n")

	first := strings.Split(joined, "\n")[0]
	prefix := "+ "

	require.Contains(t, first, prefix)

	entry := strings.TrimSuffix(strings.TrimPrefix(first, prefix), " (1)")

	assert.Len(t, entry, scanTitleWidth)
	assert.Contains(t, entry, "x~", "truncation keeps a tilde marker")
}

func TestScanStateLinesColoredWhenStylerEnabled(t *testing.T) {
	t.Parallel()

	state, clock := newTestScanState(6)

	walkFake(state, clock, "News", 3, time.Second)

	assert.NotContains(t, strings.Join(state.lines(NewStyler(false)), "\n"), "\x1b[")
	assert.Contains(t, strings.Join(state.lines(NewStyler(true)), "\n"), "\x1b[")
}

func TestScanStateLinesAndClockASCIIOnly(t *testing.T) {
	t.Parallel()

	state, clock := newTestScanState(5)
	state.asciiOnly = true

	walkFake(state, clock, strings.Repeat("long-title-", 6), 2, 90*time.Second)
	walkFake(state, clock, "Docs", 128, 3*time.Second)

	// An in-flight chat exercises the ASCII spinner frames too.
	state.chatStart(strings.Repeat("current-", 8))

	rendered := strings.Join(state.lines(NewStyler(true)), "\n") +
		scanClock(time.Duration(99999)*time.Second)

	for idx, runeValue := range rendered {
		if runeValue >= utf8.RuneSelf {
			t.Fatalf("non-ASCII rune %q at offset %d in rendered view %q", runeValue, idx, rendered)
		}
	}
}

func TestScanClockFormats(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		got  string
		want string
	}{
		{"seconds", scanClock(4 * time.Second), "4s"},
		{"zero", scanClock(0), "0s"},
		{"minute pads seconds", scanClock(64 * time.Second), "1m04s"},
		{"hour pads both", scanClock(3723 * time.Second), "1h02m03s"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, tc.got)
		})
	}
}

func TestScanElapsedUsesTenthsBelowMinute(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "12.3s", scanElapsed(12300*time.Millisecond))
	assert.Equal(t, "1m04s", scanElapsed(64*time.Second))
}

func TestScanModelAppliesChatMessages(t *testing.T) {
	t.Parallel()

	state, _ := newTestScanState(3)

	model := scanModel{state: state, styler: NewStyler(false)}

	next, _ := model.Update(scanChatMsg{title: "News", matches: 7, took: time.Second})

	view := next.View().Content
	assert.Contains(t, view, "scanning 1/3")
	assert.Contains(t, view, "+ News (7)")
}

func TestScanModelTickReschedules(t *testing.T) {
	t.Parallel()

	state, _ := newTestScanState(2)

	model := scanModel{state: state, styler: NewStyler(false)}

	next, cmd := model.Update(scanTickMsg{})
	require.NotNil(t, cmd, "ticks must reschedule")

	_, ok := next.(scanModel)
	require.True(t, ok, "the model stays a scanModel")
}

func TestScanProgressSilentOrEmptyIsNil(t *testing.T) {
	t.Parallel()

	cmd := &cobra.Command{}

	assert.Nil(t, newScanProgress(cmd, nil, 5), "no app means no progress UI")
	assert.Nil(t, newScanProgress(cmd, &App{}, 0), "nothing to walk means no progress UI")

	app := &App{}
	cmd.Flags().Bool("silent-output", true, "")

	assert.Nil(t, newScanProgress(cmd, app, 5), "silent mode reports nothing")
}

func TestScanProgressNilReceiverIsNoop(t *testing.T) {
	t.Parallel()

	var progress *scanProgress

	assert.NotPanics(t, func() {
		progress.chatDone("News", 3, time.Second)
		progress.close()
	})
}

func TestScanProgressNonTTYWritesLinePerChat(t *testing.T) {
	t.Parallel()

	cmd, stderr := newErrCmd()

	// Non-TTY construction: total known, plain line surface.
	progress := &scanProgress{out: cmd.ErrOrStderr(), total: 254, styler: NewStyler(false)}

	progress.chatDone("News Channel", 37, time.Second)
	progress.chatDone("Docs", 128, time.Second)
	progress.close()

	out := stderr.String()
	assert.Contains(t, out, "scanned 1/254  News Channel: 37 matches\n")
	assert.Contains(t, out, "scanned 2/254  Docs: 128 matches\n")
}

func TestScanProgressWalkLoopCallbackDeltas(t *testing.T) {
	t.Parallel()

	// Mirrors the executeRun wiring: matches per chat come from the
	// collector length delta around each walk.
	cmd, stderr := newErrCmd()

	progress := &scanProgress{out: cmd.ErrOrStderr(), total: 3, styler: NewStyler(false)}

	collector := &walkCollector{}

	for _, target := range []struct {
		title   string
		matches int
	}{
		{"A", 2},
		{"B", 0},
		{"C", 5},
	} {
		before := len(collector.items)

		for range target.matches {
			collector.items = append(collector.items, storeMediaItem())
		}

		progress.chatDone(target.title, len(collector.items)-before, time.Second)
	}

	assert.Equal(t, "scanned 1/3  A: 2 matches\nscanned 2/3  B: 0 matches\nscanned 3/3  C: 5 matches\n", stderr.String())
}

// storeMediaItem builds one minimal media item for collector simulations.
func storeMediaItem() store.MediaItem {
	return store.MediaItem{ChatID: 1}
}

// newErrCmd returns a command whose stderr buffers into buf.
func newErrCmd() (*cobra.Command, *strings.Builder) {
	var buf strings.Builder

	cmd := &cobra.Command{}
	cmd.SetErr(&buf)

	return cmd, &buf
}

func TestScanStateSpinnerLineForCurrentChat(t *testing.T) {
	t.Parallel()

	state, clock := newTestScanState(4)

	walkFake(state, clock, "A", 1, time.Second)

	state.chatStart("Sirius")
	state.spinner = 3

	joined := strings.Join(state.lines(NewStyler(false)), "\n")
	assert.Contains(t, joined, "⠸ Sirius", "braille spinner frame prefixes the active chat")
	assert.NotContains(t, joined, "+ Sirius", "the active chat has not settled yet")

	lines := strings.Split(joined, "\n")
	require.GreaterOrEqual(t, len(lines), 3)
	assert.Contains(t, lines[0], "+ A (1)", "settled lines come first")
	assert.Contains(t, lines[len(lines)-1], "scanning 1/4", "footer stays last")
}

func TestScanStateAsciiSpinnerUnderNoASCII(t *testing.T) {
	t.Parallel()

	state, clock := newTestScanState(4)
	state.asciiOnly = true

	walkFake(state, clock, "A", 1, time.Second)

	state.chatStart("Sirius")
	state.spinner = 2

	joined := strings.Join(state.lines(NewStyler(false)), "\n")
	assert.Contains(t, joined, "- Sirius", "ASCII spinner frame prefixes the active chat")
}
