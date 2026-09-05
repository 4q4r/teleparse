package download_test

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/download"
	"github.com/4q4r/teleparse/internal/store"

	tg "github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refetchResolver simulates a cached manifest row: no walked location, only
// a refetch hook — exactly what runResolver produces for items whose walk
// context is gone.
type refetchResolver struct {
	root    string
	loc     tg.InputFileLocationClass
	refetch download.RefetchFunc
}

func (r refetchResolver) Resolve(store.MediaItem) (download.Resolved, error) {
	return download.Resolved{
		Path:     filepath.Join(r.root, "cached.bin"),
		Location: r.loc,
		Refetch:  r.refetch,
	}, nil
}

// refetchingSource emulates the production fetch path (resolveLocation): a
// missing location must be obtainable through the input's refetch hook, and
// transfer failures surface for the retry ladder.
func refetchingSource(transfers *atomic.Int32, fail error) download.FetchFunc {
	return func(ctx context.Context, in download.Input, dest io.WriterAt) (int64, error) {
		if in.Location == nil {
			if in.Refetch == nil {
				return 0, download.ErrNoLocation
			}

			if _, err := in.Refetch(ctx); err != nil {
				return 0, err
			}
		}

		if fail != nil && transfers.Add(1) <= 1 {
			return 0, fail
		}

		written, err := dest.WriteAt([]byte("cached!"), 0)

		return int64(written), err
	}
}

// TestManagerWiresRefetchIntoFetchInput pins the production bug: cached
// manifest rows resolve with no location and a refetch hook, and the
// Manager must forward that hook into the Fetch input so the fetch path
// can hydrate the location.
func TestManagerWiresRefetchIntoFetchInput(t *testing.T) {
	t.Parallel()

	state := newTestStore(t)

	refetchCalls := atomic.Int32{}

	resolver := refetchResolver{
		root: t.TempDir(),
		refetch: func(context.Context) (tg.InputFileLocationClass, error) {
			refetchCalls.Add(1)

			return &tg.InputDocumentFileLocation{ID: 777}, nil
		},
	}

	mgr := download.NewManager(state, newPacer(time.Minute), managerConfig(), resolver, download.NoopReporter{})
	mgr.Fetch = refetchingSource(&atomic.Int32{}, nil)

	enqueue(t, mgr, queuedItem(1, 10, 7))

	res, err := mgr.Run(context.Background(), "run-refetch-wiring")
	require.NoError(t, err)

	assert.EqualValues(t, 1, res.Downloaded, "a cached row must download through its refetch hook")
	assert.EqualValues(t, 1, refetchCalls.Load())
}

// TestManagerRefetchNetworkErrorsRetry pins the ladder for transient
// refetch failures: a network error hydrating the location retries like
// any transfer hiccup instead of failing the item.
func TestManagerRefetchNetworkErrorsRetry(t *testing.T) {
	t.Parallel()

	state := newTestStore(t)

	refetchCalls := atomic.Int32{}

	resolver := refetchResolver{
		root: t.TempDir(),
		refetch: func(context.Context) (tg.InputFileLocationClass, error) {
			if refetchCalls.Add(1) <= 2 {
				return nil, &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset")}
			}

			return &tg.InputDocumentFileLocation{ID: 777}, nil
		},
	}

	mgr := download.NewManager(state, newPacer(time.Minute), managerConfig(), resolver, download.NoopReporter{})
	mgr.Fetch = refetchingSource(&atomic.Int32{}, nil)

	enqueue(t, mgr, queuedItem(1, 10, 7))

	res, err := mgr.Run(context.Background(), "run-refetch-neterr")
	require.NoError(t, err)

	assert.EqualValues(t, 1, res.Downloaded, "two transient refetch failures then success must download")
	assert.EqualValues(t, 2, res.Retries)
}

// TestManagerFailsFastWhenRefetchReportsMessageGone pins honest
// classification: a refetch whose message is deleted for good must fail the
// item immediately with the real cause, never loop or mask it as a missing
// location.
func TestManagerFailsFastWhenRefetchReportsMessageGone(t *testing.T) {
	t.Parallel()

	state := newTestStore(t)

	recorder := &detailRecorder{}

	refetchCalls := atomic.Int32{}

	resolver := refetchResolver{
		root: t.TempDir(),
		refetch: func(context.Context) (tg.InputFileLocationClass, error) {
			refetchCalls.Add(1)

			return nil, errors.Join(download.ErrMessageGone)
		},
	}

	mgr := download.NewManager(state, newPacer(time.Minute), managerConfig(), resolver, recorder)
	mgr.Fetch = refetchingSource(&atomic.Int32{}, nil)

	enqueue(t, mgr, queuedItem(1, 10, 7))

	res, err := mgr.Run(context.Background(), "run-refetch-gone")
	require.NoError(t, err)

	assert.EqualValues(t, 1, res.Failed)
	assert.EqualValues(t, 0, res.Retries, "a gone message is fatal: no retries")
	assert.EqualValues(t, 1, refetchCalls.Load(), "the ladder must stop after one refetch")

	require.Len(t, recorder.failures, 1)
	assert.ErrorIs(t, recorder.failures[0].err, download.ErrMessageGone, "the real cause must surface")
}

// TestManagerRefetchesOnFileReferenceExpired pins mid-download
// rehydration: an expired reference must mint a fresh location via the
// refetch hook and the retry must carry it into the next fetch input.
func TestManagerRefetchesOnFileReferenceExpired(t *testing.T) {
	t.Parallel()

	state := newTestStore(t)

	stale := &tg.InputDocumentFileLocation{ID: 777, FileReference: []byte("stale")}
	fresh := &tg.InputDocumentFileLocation{ID: 777, FileReference: []byte("fresh")}

	refetchCalls := atomic.Int32{}

	resolver := refetchResolver{root: t.TempDir(), loc: stale, refetch: func(context.Context) (tg.InputFileLocationClass, error) {
		refetchCalls.Add(1)

		return fresh, nil
	}}

	var seen []tg.InputFileLocationClass

	mgr := download.NewManager(state, newPacer(time.Minute), managerConfig(), resolver, download.NoopReporter{})
	mgr.Fetch = func(_ context.Context, in download.Input, dest io.WriterAt) (int64, error) {
		seen = append(seen, in.Location)

		if len(seen) == 1 {
			return 0, tgerr.New(400, "FILE_REFERENCE_EXPIRED")
		}

		written, err := dest.WriteAt([]byte("fresh!"), 0)

		return int64(written), err
	}

	enqueue(t, mgr, queuedItem(1, 10, 6))

	res, err := mgr.Run(context.Background(), "run-refetch-expired")
	require.NoError(t, err)

	assert.EqualValues(t, 1, res.Downloaded)
	assert.EqualValues(t, 1, refetchCalls.Load(), "one expiry refetches once")

	require.Len(t, seen, 2)
	assert.Same(t, stale, seen[0])
	assert.Same(t, fresh, seen[1], "the retried transfer must ride the fresh location")
}

// TestManagerFailsFastWhenExpiredRefetchReportsMessageGone pins the expiry
// path's honest classification: refetching an expired reference whose
// message is gone must fail fast with the real cause instead of looping on
// FILE_REFERENCE_EXPIRED until the ladder exhausts.
func TestManagerFailsFastWhenExpiredRefetchReportsMessageGone(t *testing.T) {
	t.Parallel()

	state := newTestStore(t)

	recorder := &detailRecorder{}

	stale := &tg.InputDocumentFileLocation{ID: 777, FileReference: []byte("stale")}

	refetchCalls := atomic.Int32{}
	transfers := atomic.Int32{}

	resolver := refetchResolver{root: t.TempDir(), loc: stale, refetch: func(context.Context) (tg.InputFileLocationClass, error) {
		refetchCalls.Add(1)

		return nil, errors.Join(download.ErrMessageGone)
	}}

	mgr := download.NewManager(state, newPacer(time.Minute), managerConfig(), resolver, recorder)
	mgr.Fetch = refetchingSource(&transfers, tgerr.New(400, "FILE_REFERENCE_EXPIRED"))

	enqueue(t, mgr, queuedItem(1, 10, 7))

	res, err := mgr.Run(context.Background(), "run-expired-gone")
	require.NoError(t, err)

	assert.EqualValues(t, 1, res.Failed)
	assert.EqualValues(t, 0, res.Retries)
	assert.EqualValues(t, 1, transfers.Load(), "only the first transfer may run")
	assert.EqualValues(t, 1, refetchCalls.Load())

	require.Len(t, recorder.failures, 1)
	assert.ErrorIs(t, recorder.failures[0].err, download.ErrMessageGone)
}
