package download_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/download"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// offsetRecorder captures the Input.Offset every ladder attempt receives.
type offsetRecorder struct {
	mu      sync.Mutex
	offsets []int64
}

func (rec *offsetRecorder) record(offset int64) {
	rec.mu.Lock()
	defer rec.mu.Unlock()

	rec.offsets = append(rec.offsets, offset)
}

func (rec *offsetRecorder) snapshot() []int64 {
	rec.mu.Lock()
	defer rec.mu.Unlock()

	return append([]int64(nil), rec.offsets...)
}

// TestRunRangedDropResumesAtCurrentOffset pins THE zero-redownload
// contract of the ranged path: a mid-file connection drop burns one
// ladder attempt, and the re-entry receives offset == the bytes already
// on disk — the fetch re-transfers nothing below them.
func TestRunRangedDropResumesAtCurrentOffset(t *testing.T) {
	t.Parallel()

	state := newTestStore(t)

	root := t.TempDir()

	recorder := &offsetRecorder{}

	mgr := download.NewManager(state, newPacer(0), managerConfig(),
		fixedResolver{root: root, name: "file.bin"}, &detailRecorder{})

	mgr.Fetch = func(_ context.Context, in download.Input, dest io.WriterAt) (int64, error) {
		recorder.record(in.Offset)

		if in.Offset == 0 {
			_, err := dest.WriteAt([]byte("AAAAA"), 0)
			require.NoError(t, err)

			// Connection drop mid-file: half the item persisted, the
			// in-flight request dies with gotd's wrapped cancel.
			return 0, connDeathCancel()
		}

		written, err := dest.WriteAt([]byte("BBBBB"), in.Offset)

		return int64(written), err
	}

	enqueue(t, mgr, queuedItem(1, 10, 10))

	res, err := mgr.Run(context.Background(), "run-ranged-offset")
	require.NoError(t, err)

	assert.EqualValues(t, 1, res.Downloaded, "the item must complete across the drop")
	assert.EqualValues(t, 0, res.Failed)

	// THE assertion: the retry re-entered at the persisted byte count,
	// so no byte below it was ever re-requested.
	assert.Equal(t, []int64{0, 5}, recorder.snapshot(),
		"the ladder re-entry must receive the current on-disk offset")

	got, err := os.ReadFile(filepath.Join(root, "file.bin"))
	require.NoError(t, err)
	assert.Equal(t, "AAAAABBBBB", string(got), "the persisted prefix must survive untouched")
}

// TestRunRangedExhaustionClassifiesInterrupted pins that the ranged
// chunk-budget exhaustion error — a wrapped cancel climbing after the
// per-chunk budget burned — keeps the PR #35 backstop at the ladder:
// exhaustion settles interrupted-resumable, never terminally failed.
func TestRunRangedExhaustionClassifiesInterrupted(t *testing.T) {
	t.Parallel()

	state := newTestStore(t)

	recorder := &interruptRecorder{}

	root := t.TempDir()

	mgr := download.NewManager(state, newPacer(time.Minute), managerConfig(),
		fixedResolver{root: root, name: "file.bin"}, recorder)

	// The exact production shape of a chunk budget burning through on a
	// dead connection: the wrapped cancel chain must survive the
	// exhaustion wrap and the fetch wrap.
	mgr.Fetch = func(context.Context, download.Input, io.WriterAt) (int64, error) {
		return 0, fmt.Errorf(
			"ranged download document/10: chunk at %d after 12 retries: rpcDoRequest: engine forcibly closed: %w",
			512*1024, context.Canceled)
	}

	enqueue(t, mgr, queuedItem(1, 10, 11))

	res, err := mgr.Run(context.Background(), "run-ranged-exhausted")
	require.NoError(t, err)

	assert.EqualValues(t, 0, res.Failed, "an exhausted chunk budget is not the chat's failure")
	assert.EqualValues(t, 1, res.Interrupted, "the exhausted ladder must settle interrupted")

	require.Len(t, recorder.interrupted, 1)
	assert.Empty(t, recorder.failures, "no FAIL reason may render for a resumable interrupt")

	assert.FileExists(t, filepath.Join(root, "file.bin.part"), "the .part bytes must stay for resume")
}
