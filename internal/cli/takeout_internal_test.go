package cli

import (
	"fmt"
	"testing"

	"github.com/4q4r/teleparse/internal/config"

	"github.com/gotd/td/tgerr"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEstimateScopeSize(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		specs   []string
		dialogs int
		want    int
	}{
		{name: "all implies every dialog", specs: []string{"all"}, dialogs: 338, want: 338},
		{name: "glob implies every dialog", specs: []string{"News*"}, dialogs: 120, want: 120},
		{name: "mixed glob wins", specs: []string{"saved", "News*"}, dialogs: 90, want: 90},
		{name: "explicit chats count one each", specs: []string{"@a", "@b", "123"}, dialogs: 338, want: 3},
		{name: "empty specs walk nothing", specs: nil, dialogs: 338, want: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, estimateScopeSize(tc.specs, tc.dialogs))
		})
	}
}

func TestResolveTakeoutModePrecedence(t *testing.T) {
	t.Parallel()

	// countFn is unused for the flag/config branches; the auto branch is
	// covered by TestResolveTakeoutModeAuto via a nil-safe probe seam.
	cfg := config.Default()

	mode, reason := resolveTakeoutModeFor(dlRunFlags{takeout: true}, cfg, nil, probeNone)
	assert.True(t, mode)
	assert.Contains(t, reason, "--takeout")

	mode, reason = resolveTakeoutModeFor(dlRunFlags{noTakeout: true}, cfg, nil, probeNone)
	assert.False(t, mode)
	assert.Contains(t, reason, "--no-takeout")

	forced := config.Default()
	forced.Net.Takeout = true

	mode, reason = resolveTakeoutModeFor(dlRunFlags{}, forced, nil, probeNone)
	assert.True(t, mode)
	assert.Contains(t, reason, "net.takeout")

	off := config.Default()
	off.Net.TakeoutAuto = false

	mode, reason = resolveTakeoutModeFor(dlRunFlags{}, off, nil, probeNone)
	assert.False(t, mode)
	assert.Contains(t, reason, "takeout_auto")
}

func TestResolveTakeoutModeAuto(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	require.Equal(t, 50, cfg.Net.TakeoutAutoMinChats)

	// Large scope engages auto takeout.
	mode, reason := resolveTakeoutModeFor(dlRunFlags{}, cfg, []string{"all"}, probeCount(254))
	assert.True(t, mode)
	assert.Contains(t, reason, "auto: ~254 chats >= 50")

	// Small explicit scope stays on the normal path.
	mode, reason = resolveTakeoutModeFor(dlRunFlags{}, cfg, []string{"@a", "@b"}, probeCount(338))
	assert.False(t, mode)
	assert.Contains(t, reason, "auto: ~2 chats < 50")

	// A failing dialog-count probe never blocks the run.
	mode, reason = resolveTakeoutModeFor(dlRunFlags{}, cfg, []string{"all"}, probeErr)
	assert.False(t, mode)
	assert.Contains(t, reason, "probe failed")
}

func TestTakeoutConfigDefaultsAndValidation(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	assert.True(t, cfg.Net.TakeoutAuto, "auto takeout is on by default")
	assert.Equal(t, 50, cfg.Net.TakeoutAutoMinChats)

	bad := config.Default()
	bad.Net.TakeoutAutoMinChats = 0

	err := bad.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, config.ErrBadTakeoutMinChats)
}

// probeCount answers a fixed dialog count.
func probeCount(n int) func() (int, error) {
	return func() (int, error) { return n, nil }
}

// probeErr simulates a failed dialog-count probe.
func probeErr() (int, error) { return 0, assert.AnError }

// probeNone marks an unused probe (flag/config branches never call it).
func probeNone() (int, error) { return -1, nil }

func TestTakeoutFallback(t *testing.T) {
	t.Parallel()

	delayErr := fmt.Errorf("init takeout session: %w",
		tgerr.New(420, "TAKEOUT_INIT_DELAY_86400"))

	// Auto-engaged takeout falls back with a notice.
	retry, notice := takeoutFallback(true, "auto: ~1095 chats >= 50", delayErr)
	require.True(t, retry)
	assert.Contains(t, notice, "continuing without export mode")
	assert.Contains(t, notice, "24h00m")

	// Forced takeout never silently falls back.
	retry, _ = takeoutFallback(false, "forced by --takeout", delayErr)
	assert.False(t, retry)

	// Non-takeout errors never trigger a fallback.
	retry, _ = takeoutFallback(true, "auto", assert.AnError)
	assert.False(t, retry)

	retry, _ = takeoutFallback(true, "auto", nil)
	assert.False(t, retry)
}

func TestIsTakeoutInitFailure(t *testing.T) {
	t.Parallel()

	require.True(t, isTakeoutInitFailure(tgerr.New(420, "TAKEOUT_INIT_DELAY_3600")))
	require.True(t, isTakeoutInitFailure(fmt.Errorf("outer: %w",
		tgerr.New(420, "TAKEOUT_SESSION_TOO_MANY"))))
	assert.False(t, isTakeoutInitFailure(assert.AnError))
	assert.False(t, isTakeoutInitFailure(nil))
}
