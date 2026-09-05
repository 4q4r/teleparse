package download_test

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/download"
	"github.com/4q4r/teleparse/internal/store"

	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Byte-sized caps keep the pre-check arithmetic and boundary obvious.
const (
	takeoutTestCap    int64 = 8
	takeoutTestOversz int64 = 9
)

// TestManagerTakeoutFileTooBigFailsFast pins the rpc-path classification:
// a TAKEOUT_FILE_TOO_BIG rejection is fatal immediately — one transfer
// attempt, no retry ladder — and maps onto the takeout cap sentinel.
func TestManagerTakeoutFileTooBigFailsFast(t *testing.T) {
	t.Parallel()

	state := newTestStore(t)

	var calls atomic.Int64

	fetch := func(context.Context, download.Input, io.WriterAt) (int64, error) {
		calls.Add(1)

		return 0, tgerr.New(403, "TAKEOUT_FILE_TOO_BIG")
	}

	recorder := &detailRecorder{}

	root := t.TempDir()

	mgr := download.NewManager(state, newPacer(time.Minute), managerConfig(),
		fixedResolver{root: root, name: "file.bin"}, recorder)
	mgr.Fetch = fetch

	enqueue(t, mgr, queuedItem(1, 10, 5))

	res, err := mgr.Run(context.Background(), "run-takeout-big")
	require.NoError(t, err)

	assert.EqualValues(t, 1, res.Failed)
	assert.EqualValues(t, 0, res.Retries, "TAKEOUT_FILE_TOO_BIG must not climb the retry ladder")
	assert.EqualValues(t, 1, calls.Load(), "the fatal classification must stop after the first attempt")

	require.Len(t, recorder.failures, 1)
	require.ErrorIs(t, recorder.failures[0].err, download.ErrFileTooBigForTakeout)

	row, exists, err := state.MediaByFile(context.Background(), "document", 10)
	require.NoError(t, err)
	require.True(t, exists)
	assert.Equal(t, store.StatusFailed, row.Status)
}

// TestManagerTakeoutOversizedPrecheckFailsWithoutFetch pins the pre-check:
// an item larger than the active takeout cap fails with the sentinel
// before a single RPC fires.
func TestManagerTakeoutOversizedPrecheckFailsWithoutFetch(t *testing.T) {
	t.Parallel()

	state := newTestStore(t)

	var calls atomic.Int64

	fetch := func(context.Context, download.Input, io.WriterAt) (int64, error) {
		calls.Add(1)

		return 0, nil
	}

	cfg := managerConfig(func(c *download.Config) { c.FileMaxSize = takeoutTestCap })

	recorder := &detailRecorder{}

	root := t.TempDir()

	mgr := download.NewManager(state, newPacer(time.Minute), cfg,
		fixedResolver{root: root, name: "file.bin"}, recorder)
	mgr.Fetch = fetch

	enqueue(t, mgr, queuedItem(1, 10, takeoutTestOversz))

	res, err := mgr.Run(context.Background(), "run-takeout-precheck")
	require.NoError(t, err)

	assert.EqualValues(t, 1, res.Failed)
	assert.EqualValues(t, 0, calls.Load(), "oversized items must fail before any fetch")
	assert.EqualValues(t, 0, res.Downloaded)

	require.Len(t, recorder.failures, 1)
	require.ErrorIs(t, recorder.failures[0].err, download.ErrFileTooBigForTakeout)
	assert.Contains(t, recorder.failures[0].err.Error(), "9B", "the failure reason must name the item size")
	assert.Contains(t, recorder.failures[0].err.Error(), "8B", "the failure reason must name the active cap")

	row, exists, err := state.MediaByFile(context.Background(), "document", 10)
	require.NoError(t, err)
	require.True(t, exists)
	assert.Equal(t, store.StatusFailed, row.Status)
	require.NotNil(t, row.LastError)
	assert.Contains(t, *row.LastError, "takeout")
}

// TestManagerTakeoutItemAtCapStillDownloads pins the boundary: the cap is
// exclusive — an item exactly at the cap transfers normally.
func TestManagerTakeoutItemAtCapStillDownloads(t *testing.T) {
	t.Parallel()

	state := newTestStore(t)

	cfg := managerConfig(func(c *download.Config) { c.FileMaxSize = takeoutTestCap })

	root, mgr := newManagerIn(t, state, cfg, byteSource("12345678"), nil)
	enqueue(t, mgr, queuedItem(1, 10, takeoutTestCap))

	res, err := mgr.Run(context.Background(), "run-takeout-boundary")
	require.NoError(t, err)

	assert.EqualValues(t, 1, res.Downloaded)
	assert.EqualValues(t, 0, res.Failed)

	assert.FileExists(t, root+"/file.bin", "the at-cap item must land on disk")
}

// TestManagerTakeoutCapInactiveOffTakeout pins that a zero cap (no active
// takeout session) never trips the pre-check, whatever the size.
func TestManagerTakeoutCapInactiveOffTakeout(t *testing.T) {
	t.Parallel()

	state := newTestStore(t)

	_, mgr := newManagerIn(t, state, managerConfig(), byteSource("123456789"), nil)
	enqueue(t, mgr, queuedItem(1, 10, takeoutTestOversz))

	res, err := mgr.Run(context.Background(), "run-no-takeout")
	require.NoError(t, err)

	assert.EqualValues(t, 1, res.Downloaded)
	assert.EqualValues(t, 0, res.Failed)
}
