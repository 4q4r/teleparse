package download

import (
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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
