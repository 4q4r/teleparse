package download_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/download"
	"github.com/4q4r/teleparse/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// interruptRecorder captures how canceled transfers settle: interruptions
// and failure details must stay mutually exclusive.
type interruptRecorder struct {
	download.NoopReporter

	mu          sync.Mutex
	interrupted []string
	failures    []failureCall
}

func (r *interruptRecorder) ItemStart(string, string, int64, int64) {}

func (r *interruptRecorder) ItemProgress(string, int64) {}

func (r *interruptRecorder) ItemDone(string, bool) {}

func (r *interruptRecorder) Throttled(int) {}

func (r *interruptRecorder) ItemFailedDetail(key string, attempts int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.failures = append(r.failures, failureCall{key: key, attempts: attempts, err: err})
}

func (r *interruptRecorder) ItemInterrupted(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.interrupted = append(r.interrupted, key)
}

// connDeathCancel reproduces the exact production error shape from the
// takeout run: a dropped MTProto connection force-closes its RPC engine and
// fails acknowledged upload.getFile requests with a WRAPPED context.Canceled
// ("engine forcibly closed", gotd rpc/engine.go) — while the run context
// itself stays alive and other items keep downloading.
func connDeathCancel() error {
	return fmt.Errorf(
		"parallel download: get file: get next chunk: rpcDoRequest: engine forcibly closed: %w",
		context.Canceled)
}

// TestRunTakeoutConnDeathCanceledIsResumable is the Bug A regression: a
// wrapped context.Canceled surfacing while the run is alive (connection
// death, not user interrupt) stays retryable inside the ladder, and when the
// ladder exhausts the item settles INTERRUPTED — never terminally failed:
// no failure reason, the .part file kept, the retry budget unburned and a
// later run able to resume and complete the transfer.
func TestRunTakeoutConnDeathCanceledIsResumable(t *testing.T) {
	t.Parallel()

	state := newTestStore(t)

	recorder := &interruptRecorder{}

	root := t.TempDir()

	mgr := download.NewManager(state, newPacer(time.Minute), managerConfig(),
		fixedResolver{root: root, name: "file.bin"}, recorder)
	mgr.Fetch = func(context.Context, download.Input, io.WriterAt) (int64, error) {
		return 0, connDeathCancel()
	}

	enqueue(t, mgr, queuedItem(1, 10, 11))

	res, err := mgr.Run(context.Background(), "run-conndeath")
	require.NoError(t, err)

	assert.EqualValues(t, 0, res.Failed, "a connection-drop cancel must not count as failed")
	assert.EqualValues(t, 1, res.Interrupted, "the exhausted ladder must settle interrupted")
	assert.EqualValues(t, 2, res.Retries, "retry_max 3 means two retries before settling")
	assert.Empty(t, res.FailedByChat, "a cancel is not the chat's failure")

	require.Len(t, recorder.interrupted, 1, "the transfer line must settle as interrupted")
	assert.Empty(t, recorder.failures, "interrupted items must not emit failure detail")

	assert.FileExists(t, filepath.Join(root, "file.bin.part"), "the .part file must survive for resume")

	row, exists, err := state.MediaByFile(t.Context(), "document", 10)
	require.NoError(t, err)
	require.True(t, exists)
	assert.Equal(t, store.StatusDownloading, row.Status,
		"an interrupted row stays owned so the live run cannot re-claim it")
	assert.Zero(t, row.Attempts, "cancellation must not consume the retry budget")

	reset, err := state.ResetDownloading(t.Context())
	require.NoError(t, err)
	assert.EqualValues(t, 1, reset, "the interrupted row must reclaim after the crash-style reset")

	// A later run with a healthy connection completes the item; the same
	// manager re-drains its enqueued chat after the reset, exactly like a
	// resume run reclaiming the row.
	mgr.Fetch = byteSource("hello world")

	resumed, err := mgr.Run(t.Context(), "run-conndeath-resume")
	require.NoError(t, err)
	assert.EqualValues(t, 1, resumed.Downloaded, "the interrupted item must resume and complete")
	assert.NoFileExists(t, filepath.Join(root, "file.bin.part"), "the promoted file leaves no part behind")
}

// TestRunUserCancelInterruptsItemGracefully pins Ctrl-C semantics: a canceled
// run settles its in-flight item as interrupted on the first cancel-flavored
// error — no further ladder attempts, no attempts burned, no FAIL reason.
func TestRunUserCancelInterruptsItemGracefully(t *testing.T) {
	t.Parallel()

	state := newTestStore(t)

	recorder := &interruptRecorder{}

	root := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	started := make(chan struct{})

	mgr := download.NewManager(state, newPacer(time.Minute), managerConfig(),
		fixedResolver{root: root, name: "file.bin"}, recorder)
	mgr.Fetch = func(ctx context.Context, _ download.Input, _ io.WriterAt) (int64, error) {
		close(started)

		<-ctx.Done()

		return 0, fmt.Errorf("fetch: %w", ctx.Err())
	}

	enqueue(t, mgr, queuedItem(1, 10, 11))

	var (
		res     download.Result
		runErr  error
		runDone = make(chan struct{})
	)

	go func() {
		res, runErr = mgr.Run(ctx, "run-usercancel")
		close(runDone)
	}()

	<-started
	cancel()
	<-runDone

	require.NoError(t, runErr)

	assert.EqualValues(t, 1, res.Interrupted, "the canceled item must settle interrupted")
	assert.EqualValues(t, 0, res.Failed)
	assert.EqualValues(t, 0, res.Retries, "a canceled run must not keep retrying")

	require.Len(t, recorder.interrupted, 1)
	assert.Empty(t, recorder.failures, "an interrupt must not render a FAIL reason")

	row, exists, err := state.MediaByFile(t.Context(), "document", 10)
	require.NoError(t, err)
	require.True(t, exists)
	assert.Zero(t, row.Attempts, "the cancel must not burn the retry budget")

	assert.FileExists(t, filepath.Join(root, "file.bin.part"), "the partial transfer stays resumable")
}

// TestQuietReporterInterruptedLine pins the non-TTY surface: an interrupted
// transfer prints one honest line and retires the key silently.
func TestQuietReporterInterruptedLine(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	reporter := download.NewQuietReporter(&out)

	reporter.ItemStart("k", "video.mp4", 10, 0)
	reporter.ItemInterrupted("k")

	// The interruption already retired the item; ItemDone must stay silent.
	reporter.ItemDone("k", false)

	assert.Equal(t, "interrupted video.mp4\n", out.String())
}
