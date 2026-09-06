package download

import (
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock pins the live state's time source; tests advance it explicitly.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

func newTestState() (*liveState, *fakeClock) {
	clock := &fakeClock{now: time.Unix(0, 0)}

	return newLiveState(clock.Now), clock
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

func itoaLive(value int) string { return strconv.Itoa(value) }

func TestLiveStateRendersPercentSpeedAndTotal(t *testing.T) {
	t.Parallel()

	state, clock := newTestState()

	state.itemStart("1/2/0", "video.mp4", 1000, 0)

	state.itemProgress("1/2/0", 500)

	clock.advance(time.Second)

	lines := state.lines(100)
	joined := lines[0]

	for _, want := range []string{"video.mp4", "50%", "500B/s", "500B/1000B"} {
		if !contains(joined, want) {
			t.Errorf("item line %q missing %q", joined, want)
		}
	}
}

func TestLiveStateRendersETA(t *testing.T) {
	t.Parallel()

	state, clock := newTestState()

	state.itemStart("k", "big.bin", 2000, 0)

	state.itemProgress("k", 1000)

	clock.advance(2 * time.Second)

	lines := state.lines(100)

	if !contains(lines[0], "eta 2s") {
		t.Errorf("item line %q missing eta 2s", lines[0])
	}
}

func TestLiveStateProgressFromResumeOffsetCountsTotal(t *testing.T) {
	t.Parallel()

	state, _ := newTestState()

	state.itemStart("k", "resumed.bin", 1000, 400)

	state.itemProgress("k", 100)

	lines := state.lines(100)

	if !contains(lines[0], "50%") || !contains(lines[0], "500B/1000B") {
		t.Errorf("item line %q should show 50%% and 500B/1000B", lines[0])
	}
}

func TestLiveStateDoneRemovesLineAndTotalsRender(t *testing.T) {
	t.Parallel()

	state, _ := newTestState()

	state.itemStart("k", "a.jpg", 10, 0)

	state.itemProgress("k", 10)

	state.itemDone("k")

	state.inc("downloaded", 1)
	state.inc("bytes", 10)
	state.inc("skipped", 2)
	state.inc("failed", 1)

	lines := state.lines(100)

	joined := joinLines(lines)

	if contains(joined, "a.jpg") {
		t.Errorf("settled item should leave the active area: %q", joined)
	}

	if !contains(joined, "1 done") || !contains(joined, "2 skipped") || !contains(joined, "1 failed") {
		t.Errorf("totals line %q missing counters", joined)
	}
}

func TestLiveStateCapsVisibleItemsWithEllipsis(t *testing.T) {
	t.Parallel()

	state, _ := newTestState()

	for idx := range maxVisibleItems + 2 {
		key := "item" + itoaLive(idx)

		state.itemStart(key, "f"+itoaLive(idx)+".bin", 100, 0)
		state.itemProgress(key, 50)
	}

	lines := state.lines(100)

	joined := joinLines(lines)

	if !contains(joined, "+2 more") {
		t.Errorf("overflow line missing: %q", joined)
	}

	if contains(joined, "f6.bin") || contains(joined, "f7.bin") {
		t.Errorf("items beyond the cap should be hidden: %q", joined)
	}
}

func TestLiveStateSpeedDecaysOverWindow(t *testing.T) {
	t.Parallel()

	state, clock := newTestState()

	state.itemStart("k", "burst.bin", 10_000, 0)

	state.itemProgress("k", 10_000)

	clock.advance(10 * time.Second)

	lines := state.lines(100)

	if !contains(lines[0], "0B/s") {
		t.Errorf("stale speed should decay to zero: %q", lines[0])
	}
}

func TestLiveStateThrottleNotice(t *testing.T) {
	t.Parallel()

	state, clock := newTestState()

	state.throttled(5)

	joined := joinLines(state.lines(100))

	if !contains(joined, "throttled 5s") {
		t.Errorf("throttle notice missing: %q", joined)
	}

	clock.advance(10 * time.Second)

	expired := joinLines(state.lines(100))

	if contains(expired, "throttled 5s") {
		t.Errorf("stale throttle notice must expire: %q", expired)
	}
}

// TestLiveStateStallMarkerAppearsAfterQuietSpells pins the UX gap the
// 0B/s freeze exposed: an active transfer with no byte delta for the
// stall window renders the stalled marker so users can tell a real stall
// from slow progress.
func TestLiveStateStallMarkerAppearsAfterQuietSpells(t *testing.T) {
	t.Parallel()

	state, clock := newTestState()

	state.itemStart("k", "frozen.bin", 1000, 0)

	state.itemProgress("k", 900)

	clock.advance(liveSpeedWindow)

	healthy := state.lines(100)
	if contains(healthy[0], "stalled") {
		t.Errorf("a fresh transfer must not render the stall marker: %q", healthy[0])
	}

	clock.advance(liveStalledAfter)

	stalled := state.lines(100)
	if !contains(stalled[0], "stalled") {
		t.Errorf("a transfer quiet past the stall window must render the marker: %q", stalled[0])
	}
}

// TestLiveStateStallMarkerRecoversOnDelta pins that the marker clears as
// soon as bytes flow again.
func TestLiveStateStallMarkerRecoversOnDelta(t *testing.T) {
	t.Parallel()

	state, clock := newTestState()

	state.itemStart("k", "frozen.bin", 1000, 0)

	state.itemProgress("k", 500)

	clock.advance(liveStalledAfter + time.Second)

	if !contains(state.lines(100)[0], "stalled") {
		t.Fatalf("precondition: the marker must be visible before recovery")
	}

	state.itemProgress("k", 100)

	recovered := state.lines(100)
	if contains(recovered[0], "stalled") {
		t.Errorf("a fresh delta must clear the stall marker: %q", recovered[0])
	}
}

func TestLiveStateSummaryLine(t *testing.T) {
	t.Parallel()

	state, clock := newTestState()

	state.inc("downloaded", 3)
	state.inc("skipped", 1)
	state.inc("failed", 2)
	state.inc("retries", 5)
	state.inc("bytes", 2048)

	clock.advance(4 * time.Second)

	summary := state.summaryLine()

	for _, want := range []string{"3 done", "1 skipped", "2 failed", "5 retries", "2.0KiB", "4s"} {
		if !contains(summary, want) {
			t.Errorf("summary %q missing %q", summary, want)
		}
	}
}

// TestLiveStateRendersASCIIOnly pins the --no-ascii contract: every line
// the live view can render stays inside plain ASCII whatever the state.
func TestLiveStateRendersASCIIOnly(t *testing.T) {
	t.Parallel()

	state, clock := newTestState()

	// Exercise every rendering path: long names (truncation tilde), bars,
	// percents, speeds, etas, overflow, totals, throttle and summary.
	state.setPhase("downloading")

	for idx := range maxVisibleItems + 2 {
		key := "item" + itoaLive(idx)

		state.itemStart(key, "a-very-long-file-name-"+itoaLive(idx)+".bin", 1000, 0)
		state.itemProgress(key, 400)
	}

	clock.advance(time.Second)

	state.throttled(5)

	rendered := joinLines(state.lines(40)) + "\n" + state.summaryLine()

	for idx, runeValue := range rendered {
		if runeValue >= utf8.RuneSelf {
			t.Fatalf("non-ASCII rune %q at offset %d in rendered view %q", runeValue, idx, rendered)
		}
	}
}

func TestLiveStateTotalSpeedAggregatesItems(t *testing.T) {
	t.Parallel()

	state, clock := newTestState()

	state.itemStart("a", "a.bin", 100, 0)
	state.itemStart("b", "b.bin", 100, 0)

	state.itemProgress("a", 50)

	clock.advance(time.Second)

	state.itemProgress("b", 30)

	lines := state.lines(100)
	totals := lines[len(lines)-1]

	if !contains(totals, "30B/s") {
		t.Errorf("totals line %q should show the aggregated 30B/s, not double-counted bytes", totals)
	}
}

func TestLiveStateUnknownKeysAreNoops(t *testing.T) {
	t.Parallel()

	state, _ := newTestState()

	state.itemProgress("ghost", 10)
	state.itemDone("ghost")

	if lines := state.lines(100); len(lines) == 0 {
		t.Errorf("unknown-key updates must not crash or render")
	}
}

// TestLiveStateSurfacesFailureReasons pins that failed transfers expose
// their reason in the visible block and the final summary.
func TestLiveStateSurfacesFailureReasons(t *testing.T) {
	t.Parallel()

	state, _ := newTestState()
	state.itemStart("k1", "archive.zip", 100, 0)
	state.itemFailed("k1", "rpc error code 403: TAKEOUT_REQUIRED")

	visible := strings.Join(state.lines(80), "\n")
	assert.Contains(t, visible, "FAIL")
	assert.Contains(t, visible, "TAKEOUT_REQUIRED")

	summary := state.summaryLine()
	assert.Contains(t, summary, "FAIL rpc error code 403: TAKEOUT_REQUIRED")
}

// TestTruncateLiveTailKeepsTheTail pins the failure-reason truncation:
// the tail survives (Telegram errors end with their machine code), the
// head is dropped behind a tilde, and the width budget holds.
func TestTruncateLiveTailKeepsTheTail(t *testing.T) {
	t.Parallel()

	long := "fetch 1/2/0: download document/42: " +
		strings.Repeat("context ", 20) + "rpc error code 403: TAKEOUT_FILE_TOO_BIG"

	got := truncateLiveTail(long, liveReasonWidth)

	assert.True(t, strings.HasSuffix(got, "TAKEOUT_FILE_TOO_BIG"), "the rpc code must stay visible, got %q", got)
	assert.Contains(t, got, "~")
	assert.LessOrEqual(t, len(got), liveReasonWidth)

	short := "rpc error code 403: TAKEOUT_FILE_TOO_BIG"
	assert.Equal(t, short, truncateLiveTail(short, liveReasonWidth), "short reasons pass through")
}

// TestLiveStateFailLinesKeepTheErrorTail pins the rendered FAIL lines:
// both the settled live view and the final summary keep the error tail
// (the actionable code) instead of the truncated head.
func TestLiveStateFailLinesKeepTheErrorTail(t *testing.T) {
	t.Parallel()

	state, _ := newTestState()

	state.itemStart("k", "video.mp4", 100, 0)

	reason := strings.Repeat("context ", 30) + "rpc error code 403: TAKEOUT_FILE_TOO_BIG"
	state.itemFailed("k", reason)

	rendered := state.lines(60)

	failLine := ""
	for _, line := range rendered {
		if strings.HasPrefix(line, failMarker) {
			failLine = line

			break
		}
	}

	require.NotEmpty(t, failLine, "the live view must render a FAIL line, got %v", rendered)
	assert.True(t, strings.HasSuffix(failLine, "TAKEOUT_FILE_TOO_BIG"),
		"the live FAIL line must end with the rpc code, got %q", failLine)

	summary := state.summaryLine()
	assert.Contains(t, summary, "TAKEOUT_FILE_TOO_BIG", "the summary must keep the error tail")
}
