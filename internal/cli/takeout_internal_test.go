package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/config"
	"github.com/4q4r/teleparse/internal/tg"

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

// TestPrintTakeoutPoolsNotice pins the dl-level notice under an active
// takeout session: verbose runs explain the single-connection download
// mode, silent mode prints nothing.
func TestPrintTakeoutPoolsNotice(t *testing.T) {
	t.Parallel()

	cmd, errBuf := newErrCmd()

	require.NoError(t, printTakeoutPoolsNotice(cmd, &App{errStyle: NewStyler(false)}, false))

	got := errBuf.String()
	assert.Contains(t, got, "downloads ride the takeout session")
	assert.Contains(t, got, "parallel pools resume on non-takeout runs")

	require.NoError(t, printTakeoutPoolsNotice(cmd, &App{}, true))
	assert.Equal(t, got, errBuf.String(), "silent mode prints nothing")
}

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

// takeoutGib is 1GiB in bytes; cap assertions build their expected values
// from it independently of the production constants.
const takeoutGib int64 = 1 << 30

// writeTakeoutPremiumCache seeds a fresh premium cache record so the
// cache-only resolution in runAccountSession has an answer without a query.
func writeTakeoutPremiumCache(t *testing.T, dir, name string, premium bool) {
	t.Helper()

	accountDir := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(accountDir, 0o700))

	blob := `{"premium": ` + strconv.FormatBool(premium) + `, "checked_at": "` +
		time.Now().UTC().Format(time.RFC3339Nano) + `"}` + "\n"

	require.NoError(t, os.WriteFile(filepath.Join(accountDir, "premium.json"), []byte(blob), 0o600))
}

// TestTakeoutConfigForFileMaxSize pins the session config: FileMaxSize
// must carry the account cap (2GiB base, 4GiB premium) or the server
// session allows no file bytes at all.
func TestTakeoutConfigForFileMaxSize(t *testing.T) {
	t.Parallel()

	base := takeoutConfigFor(false)
	assert.Equal(t, 2*takeoutGib, base.FileMaxSize, "non-premium accounts cap at 2GiB")
	assert.True(t, base.Files)
	assert.True(t, base.MessageUsers)
	assert.True(t, base.MessageChats)
	assert.True(t, base.MessageMegagroups)
	assert.True(t, base.MessageChannels)

	premium := takeoutConfigFor(true)
	assert.Equal(t, 4*takeoutGib, premium.FileMaxSize, "premium accounts cap at 4GiB")
	assert.True(t, premium.Files)
}

// TestTakeoutCapResolvesFromPremiumCacheOnly pins the cached-or-skip rule:
// the cap resolution before the session opens reads the premium cache and
// never queries; unknown answers default to the base cap.
func TestTakeoutCapResolvesFromPremiumCacheOnly(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	manager := tg.NewAccountManager(dir)

	unknown := manager.AccountPremium(t.Context(), "acct", nil)
	require.Equal(t, tg.PremiumSourceNone, unknown.Source)
	assert.Equal(t, 2*takeoutGib, takeoutCapFor(unknown.Premium), "unknown premium defaults to 2GiB")

	writeTakeoutPremiumCache(t, dir, "acct", true)

	premium := manager.AccountPremium(t.Context(), "acct", nil)
	require.Equal(t, tg.PremiumSourceCache, premium.Source)
	assert.True(t, premium.Premium)
	assert.Equal(t, 4*takeoutGib, takeoutCapFor(premium.Premium), "a cached premium answer yields 4GiB")

	writeTakeoutPremiumCache(t, dir, "plain", false)

	plain := manager.AccountPremium(t.Context(), "plain", nil)
	require.Equal(t, tg.PremiumSourceCache, plain.Source)
	assert.Equal(t, 2*takeoutGib, takeoutCapFor(plain.Premium), "a cached non-premium answer yields 2GiB")
}

// TestTakeoutFileCapActiveOnlyUnderTakeout pins the download-side cap: no
// active takeout session means no cap at all.
func TestTakeoutFileCapActiveOnlyUnderTakeout(t *testing.T) {
	t.Parallel()

	assert.Zero(t, takeoutFileCap(false, true))
	assert.Zero(t, takeoutFileCap(false, false))
	assert.Equal(t, 2*takeoutGib, takeoutFileCap(true, false))
	assert.Equal(t, 4*takeoutGib, takeoutFileCap(true, true))
}

// TestTakeoutCapNotice pins the engagement-notice cap line: it always
// names the active cap and how premium raises it.
func TestTakeoutCapNotice(t *testing.T) {
	t.Parallel()

	unknown := takeoutCapNotice(tg.PremiumStatus{Source: tg.PremiumSourceNone})
	assert.Contains(t, unknown, "2.0GiB", "unknown premium engages the base cap")
	assert.Contains(t, unknown, "4.0GiB", "the notice must say premium raises the cap")
	assert.Contains(t, unknown, "premium unknown")

	premium := takeoutCapNotice(tg.PremiumStatus{Premium: true, Source: tg.PremiumSourceCache})
	assert.Contains(t, premium, "4.0GiB")
	assert.Contains(t, premium, "premium")

	plain := takeoutCapNotice(tg.PremiumStatus{Source: tg.PremiumSourceCache})
	assert.Contains(t, plain, "2.0GiB")
	assert.Contains(t, plain, "4.0GiB")
}
