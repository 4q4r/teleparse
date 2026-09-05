package download

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestPercentOfClampsAtOneHundred pins the rendered-percent clamp: even if a
// reporter ever receives over-counted deltas, no surface may render more
// than 100%.
func TestPercentOfClampsAtOneHundred(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "100%", percentOf(267, 100), "over-total progress must clamp to 100%")
	assert.Equal(t, "100%", percentOf(100, 100))
	assert.Equal(t, "50%", percentOf(50, 100))
	assert.Equal(t, "0%", percentOf(-5, 100), "negative progress must clamp to 0%")
	assert.Empty(t, percentOf(50, 0), "unknown totals render no percent")
}

// TestLiveItemLineClampsBeyondTotal pins the defense in depth: an item line
// whose current position exceeds its total renders a full bar and 100%, not
// 167%.
func TestLiveItemLineClampsBeyondTotal(t *testing.T) {
	t.Parallel()

	state, _ := newTestState()

	state.itemStart("k", "big.bin", 100, 0)
	state.itemProgress("k", 167)

	line := state.lines(100)[0]

	assert.Contains(t, line, "100%", "the rendered percent must clamp at 100%, got %q", line)
	assert.NotContains(t, line, "167%")

	assert.Contains(t, line, "100B/100B", "the rendered position must clamp at the total")
}

// TestLiveStateSummaryShowsInterrupted pins the interrupted counter in the
// final summary: canceled transfers surface as "interrupted: N", never as
// failures.
func TestLiveStateSummaryShowsInterrupted(t *testing.T) {
	t.Parallel()

	state, _ := newTestState()

	state.inc("interrupted", 2)
	state.inc("failed", 1)

	summary := state.summaryLine()

	assert.Contains(t, summary, "interrupted: 2")
	assert.Contains(t, summary, "1 failed")
}
