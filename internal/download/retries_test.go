package download_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"

	"github.com/4q4r/teleparse/internal/download"

	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// flakySource fails the first failN calls with err, then succeeds with content.
func flakySource(content string, failN int32, err error) download.FetchFunc {
	var calls atomic.Int32

	return func(ctx context.Context, _ download.Input, dest io.WriterAt) (int64, error) {
		if calls.Add(1) <= failN {
			return 0, err
		}

		written, werr := dest.WriteAt([]byte(content), 0)

		return int64(written), werr
	}
}

// networkReset builds a genuine net.Error simulating a connection reset.
func networkReset() error {
	return &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset by peer")}
}

func TestRunCountsRetriesUntilSuccess(t *testing.T) {
	t.Parallel()

	state := newTestStore(t)

	_, mgr := newManagerIn(t, state, managerConfig(),
		flakySource("hello", 2, networkReset()), nil)
	enqueue(t, mgr, queuedItem(1, 10, 5))

	res, err := mgr.Run(context.Background(), "run-retries")
	require.NoError(t, err)

	assert.EqualValues(t, 1, res.Downloaded, "two retryable failures then success must download")
	assert.EqualValues(t, 0, res.Failed)
	assert.EqualValues(t, 2, res.Retries, "each retried attempt must count once")
}

func TestRunCountsRetriesOnExhaustedLadder(t *testing.T) {
	t.Parallel()

	state := newTestStore(t)

	boom := errors.New("permanent link failure")

	_, mgr := newManagerIn(t, state, managerConfig(), flakySource("hello", 99, boom), nil)
	enqueue(t, mgr, queuedItem(1, 10, 5))

	res, err := mgr.Run(context.Background(), "run-exhausted")
	require.NoError(t, err)

	assert.EqualValues(t, 1, res.Failed)
	assert.EqualValues(t, 2, res.Retries, "retry_max 3 means two retries after the first attempt")
}

// TestRunRetriesTransientServerErrors pins the retry ladder: network errors
// and server-side 5xx-style Telegram errors are retryable; only the
// documented fatal list aborts immediately.
func TestRunRetriesTransientServerErrors(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "network timeout", err: networkReset()},
		{name: "server internal error", err: tgerr.New(500, "INTERNAL")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			state := newTestStore(t)

			_, mgr := newManagerIn(t, state, managerConfig(), flakySource("ok", 1, tc.err), nil)
			enqueue(t, mgr, queuedItem(1, 10, 2))

			res, err := mgr.Run(context.Background(), "run-"+tc.name)
			require.NoError(t, err)
			assert.EqualValues(t, 1, res.Downloaded, "%s must be classified retryable", tc.name)
			assert.EqualValues(t, 1, res.Retries)
		})
	}
}

// detailRecorder captures FailureDetailReporter calls.
type detailRecorder struct {
	download.NoopReporter

	failures []failureCall
}

type failureCall struct {
	key      string
	attempts int
	err      error
}

func (r *detailRecorder) ItemStart(string, string, int64, int64) {}

func (r *detailRecorder) ItemProgress(string, int64) {}

func (r *detailRecorder) ItemDone(string, bool) {}

func (r *detailRecorder) Throttled(int) {}

func (r *detailRecorder) ItemFailedDetail(key string, attempts int, err error) {
	r.failures = append(r.failures, failureCall{key: key, attempts: attempts, err: err})
}

func TestManagerReportsFailureDetail(t *testing.T) {
	t.Parallel()

	state := newTestStore(t)

	recorder := &detailRecorder{}

	root := t.TempDir()

	boom := errors.New("boom")

	mgr := download.NewManager(state, newPacer(0), managerConfig(),
		fixedResolver{root: root, name: "file.bin"}, recorder)
	mgr.Fetch = func(context.Context, download.Input, io.WriterAt) (int64, error) {
		return 0, boom
	}

	enqueue(t, mgr, queuedItem(1, 2, 10))

	res, err := mgr.Run(context.Background(), "run-detail")
	require.NoError(t, err)
	assert.EqualValues(t, 1, res.Failed)

	require.Len(t, recorder.failures, 1, "the exhausted ladder must report failure detail once")
	assert.Equal(t, "1/2/0", recorder.failures[0].key)
	assert.Equal(t, 3, recorder.failures[0].attempts, "attempts consumed include the first try")
	require.ErrorIs(t, recorder.failures[0].err, boom)
}

func TestQuietReporterFailureDetailLine(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	reporter := download.NewQuietReporter(&out)

	reporter.ItemStart("k", "video.mp4", 10, 0)
	reporter.ItemFailedDetail("k", 3, errors.New("timeout"))

	// The detail line retires the item; a later ItemDone must stay silent.
	reporter.ItemDone("k", true)

	assert.Equal(t, "FAIL video.mp4 (attempts 3): timeout\n", out.String())
}

func TestQuietReporterFailureDetailUnknownKey(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	reporter := download.NewQuietReporter(&out)
	reporter.ItemFailedDetail("ghost", 2, errors.New("boom"))

	assert.Empty(t, out.String(), "unknown keys must not render")
}
