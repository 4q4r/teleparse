package download

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/config"
	"github.com/4q4r/teleparse/internal/pace"
	"github.com/4q4r/teleparse/internal/store"

	"github.com/gotd/td/telegram/downloader"
	tg "github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wrappedCancel reproduces the production connection-drop shape: gotd's
// RPC engine force-closes on a dying connection and fails acknowledged
// requests with a wrapped context.Canceled while the run stays alive.
func wrappedCancel() error {
	return fmt.Errorf("rpcDoRequest: engine forcibly closed: %w", context.Canceled)
}

// rangedCall records one upload.getFile request.
type rangedCall struct {
	offset   int64
	limit    int
	location int64
}

// rangedServer is an in-memory upload.getFile backend: it serves data
// from byte zero, records every request and injects scripted failures —
// failN counts remaining forced failures per offset, failErr overrides
// the default wrapped-cancel failure for an offset.
type rangedServer struct {
	mu      sync.Mutex
	data    []byte
	failN   map[int64]int
	failErr map[int64]error
	calls   []rangedCall
}

func (srv *rangedServer) UploadGetFile( //nolint:ireturn // mirrors the gotd seam
	_ context.Context, req *tg.UploadGetFileRequest,
) (tg.UploadFileClass, error) {
	srv.mu.Lock()
	defer srv.mu.Unlock()

	var location int64

	if doc, ok := req.Location.(*tg.InputDocumentFileLocation); ok {
		location = doc.ID
	}

	srv.calls = append(srv.calls, rangedCall{offset: req.Offset, limit: req.Limit, location: location})

	if srv.failN[req.Offset] > 0 {
		srv.failN[req.Offset]--

		err := wrappedCancel()
		if scripted, ok := srv.failErr[req.Offset]; ok {
			err = scripted
		}

		return nil, err
	}

	if req.Offset >= int64(len(srv.data)) {
		return &tg.UploadFile{}, nil
	}

	end := req.Offset + int64(req.Limit)

	if end > int64(len(srv.data)) {
		end = int64(len(srv.data))
	}

	return &tg.UploadFile{Bytes: srv.data[req.Offset:end]}, nil
}

func (srv *rangedServer) UploadGetFileHashes(
	context.Context, *tg.UploadGetFileHashesRequest,
) ([]tg.FileHash, error) {
	return nil, errNotImplemented
}

func (srv *rangedServer) UploadReuploadCDNFile(
	context.Context, *tg.UploadReuploadCDNFileRequest,
) ([]tg.FileHash, error) {
	return nil, errNotImplemented
}

func (srv *rangedServer) UploadGetCDNFileHashes(
	context.Context, *tg.UploadGetCDNFileHashesRequest,
) ([]tg.FileHash, error) {
	return nil, errNotImplemented
}

func (srv *rangedServer) UploadGetWebFile(
	context.Context, *tg.UploadGetWebFileRequest,
) (*tg.UploadWebFile, error) {
	return nil, errNotImplemented
}

// sleepRecorder captures sleeps without parking.
type sleepRecorder struct {
	mu      sync.Mutex
	slept   []time.Duration
	onSlept func(time.Duration)
}

func (rec *sleepRecorder) sleep(_ context.Context, delay time.Duration) error {
	rec.mu.Lock()
	rec.slept = append(rec.slept, delay)
	rec.mu.Unlock()

	if rec.onSlept != nil {
		rec.onSlept(delay)
	}

	return nil
}

// rangedTestFetch wires the server with test-sized knobs.
func rangedTestFetch(
	srv *rangedServer, rec *sleepRecorder, itemSize *int64, offset int64, refetch RefetchFunc,
) (FetchFunc, *Input) {
	in := &Input{
		Item:     store.MediaItem{ChatID: 1, MessageID: 2, MediaIndex: 3, MediaClass: "video", MediaID: 7, Size: itemSize},
		Offset:   offset,
		Location: &tg.InputDocumentFileLocation{ID: 7},
		Refetch:  refetch,
	}

	return RangedFetch(srv, RangedOptions{
		retries: 3,
		backoff: time.Millisecond,
		sleep:   rec.sleep,
	}), in
}

// int64ptr is a test convenience for *int64 fields.
func int64ptr(v int64) *int64 { return &v }

// TestRangedFetchTransfersChunkByChunk pins the request shape: full
// 512KiB requests at 512KiB-aligned offsets, exact byte counts at the
// right destinations and no trailing request once the known total is
// reached.
func TestRangedFetchTransfersChunkByChunk(t *testing.T) {
	t.Parallel()

	data := make([]byte, 2*rangedChunkSize+1234)
	for idx := range data {
		data[idx] = byte(idx % 251)
	}

	srv := &rangedServer{data: data, failN: map[int64]int{}, failErr: map[int64]error{}}
	rec := &sleepRecorder{}

	fetch, in := rangedTestFetch(srv, rec, int64ptr(int64(len(data))), 0, nil)

	dest := &memWriterAt{}

	written, err := fetch(t.Context(), *in, dest)
	require.NoError(t, err)

	assert.EqualValues(t, len(data), written)
	assert.Equal(t, data, dest.data, "the file must land byte-exact")

	require.Len(t, srv.calls, 3, "a known total must not need a trailing empty request")

	for _, call := range srv.calls {
		assert.Equal(t, rangedChunkSize, call.limit, "every request must ask for a full chunk")
	}

	assert.EqualValues(t, 0, srv.calls[0].offset)
	assert.EqualValues(t, rangedChunkSize, srv.calls[1].offset)
	assert.EqualValues(t, 2*rangedChunkSize, srv.calls[2].offset)
	assert.Empty(t, rec.slept, "a healthy transfer never sleeps")
}

// TestRangedFetchDropRetriesSameChunk pins the auto-restart: a wrapped
// cancel at chunk k costs ONLY that chunk — the same range is
// re-requested after one backoff and the file completes.
func TestRangedFetchDropRetriesSameChunk(t *testing.T) {
	t.Parallel()

	data := make([]byte, 2*rangedChunkSize+100)

	srv := &rangedServer{
		data:    data,
		failN:   map[int64]int{rangedChunkSize: 1},
		failErr: map[int64]error{},
	}

	rec := &sleepRecorder{}

	fetch, in := rangedTestFetch(srv, rec, int64ptr(int64(len(data))), 0, nil)

	dest := &memWriterAt{}

	written, err := fetch(t.Context(), *in, dest)
	require.NoError(t, err)

	assert.EqualValues(t, len(data), written)
	assert.Equal(t, data, dest.data)

	require.Len(t, srv.calls, 4, "the dropped chunk is re-requested exactly once")

	assert.EqualValues(t, rangedChunkSize, srv.calls[1].offset)
	assert.EqualValues(t, rangedChunkSize, srv.calls[2].offset, "the retry must target the SAME chunk")

	require.Len(t, rec.slept, 1, "one transient failure means one backoff")
	assert.Equal(t, time.Millisecond, rec.slept[0], "the first retry backs off by the base")
}

// TestRangedFetchChunkBudgetExhaustionSurfaces pins the upward ladder
// handoff: repeated drops beyond the chunk budget wrap the last error
// (preserving the cancel chain) and climb to the Manager retry ladder.
func TestRangedFetchChunkBudgetExhaustionSurfaces(t *testing.T) {
	t.Parallel()

	srv := &rangedServer{data: make([]byte, rangedChunkSize), failN: map[int64]int{0: 99}, failErr: map[int64]error{}}

	rec := &sleepRecorder{}

	fetch, in := rangedTestFetch(srv, rec, int64ptr(rangedChunkSize), 0, nil)

	_, err := fetch(t.Context(), *in, &memWriterAt{})
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled, "exhaustion must keep the cancellation chain")
	assert.Contains(t, err.Error(), "after 3 retries")

	require.Len(t, srv.calls, 4, "the budget of 3 retries buys exactly 3 re-requests")
	assert.Len(t, rec.slept, 3)
}

// TestRangedFetchCancelReturnsImmediately pins Ctrl-C semantics: a dead
// run context climbs before any RPC fires — no retry, no sleep.
func TestRangedFetchCancelReturnsImmediately(t *testing.T) {
	t.Parallel()

	srv := &rangedServer{data: []byte("x"), failN: map[int64]int{}, failErr: map[int64]error{}}

	rec := &sleepRecorder{}

	fetch, in := rangedTestFetch(srv, rec, int64ptr(1), 0, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := fetch(ctx, *in, &memWriterAt{})
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)

	assert.Empty(t, srv.calls, "a canceled run must not fire RPCs")
	assert.Empty(t, rec.slept)
}

// TestRangedFetchFileReferenceExpiredRefetches pins the refetch seam
// mid-file: one expiry mints a fresh location and the SAME chunk
// re-requests against it, without burning the retry budget.
func TestRangedFetchFileReferenceExpiredRefetches(t *testing.T) {
	t.Parallel()

	data := []byte("fresh-reference-payload")

	srv := &rangedServer{
		data:    data,
		failN:   map[int64]int{0: 1},
		failErr: map[int64]error{0: tgerr.New(400, "FILE_REFERENCE_EXPIRED")},
	}

	rec := &sleepRecorder{}

	refetched := 0

	fetch, in := rangedTestFetch(srv, rec, int64ptr(int64(len(data))), 0, func(
		context.Context,
	) (tg.InputFileLocationClass, error) {
		refetched++

		return &tg.InputDocumentFileLocation{ID: 8}, nil
	})

	dest := &memWriterAt{}

	written, err := fetch(t.Context(), *in, dest)
	require.NoError(t, err)

	assert.EqualValues(t, len(data), written)
	assert.Equal(t, 1, refetched, "the seam must run exactly once")
	assert.Empty(t, rec.slept, "a refetch is not a transient retry")

	require.Len(t, srv.calls, 2)
	assert.EqualValues(t, 7, srv.calls[0].location, "the first request rides the stale location")
	assert.EqualValues(t, 8, srv.calls[1].location, "the re-request rides the fresh location")
}

// TestRangedFetchFloodWaitSleptAndReported pins the throttle surface:
// short flood waits surface to the reporter, sleep inside the fetch and
// never burn the chunk budget.
func TestRangedFetchFloodWaitSleptAndReported(t *testing.T) {
	t.Parallel()

	data := []byte("payload-after-throttle")

	srv := &rangedServer{
		data:    data,
		failN:   map[int64]int{0: 1},
		failErr: map[int64]error{0: tgerr.New(420, "FLOOD_WAIT_2")},
	}

	recorder := &throttleRecorder{}

	sleeps := &sleepRecorder{}

	fetch := RangedFetch(srv, RangedOptions{
		Reporter: recorder,
		retries:  3,
		backoff:  time.Millisecond,
		sleep:    sleeps.sleep,
	})

	in := Input{
		Item:     store.MediaItem{ChatID: 1, MessageID: 2, MediaIndex: 3, MediaClass: "video", MediaID: 7, Size: int64ptr(int64(len(data)))},
		Location: &tg.InputDocumentFileLocation{ID: 7},
	}

	written, err := fetch(t.Context(), in, &memWriterAt{})
	require.NoError(t, err)
	assert.EqualValues(t, len(data), written)

	assert.Equal(t, []int{2}, recorder.waits, "the flood wait must surface to the live UI")

	require.Len(t, sleeps.slept, 1)
	assert.Equal(t, 2*time.Second, sleeps.slept[0], "the wait itself is slept, not the backoff")
}

// TestRangedFetchLongFloodWaitSurfacesForParking pins the ceiling:
// flood waits beyond the in-fetch maximum climb immediately so the run
// pacer can park the whole run.
func TestRangedFetchLongFloodWaitSurfacesForParking(t *testing.T) {
	t.Parallel()

	srv := &rangedServer{
		data:    []byte("x"),
		failN:   map[int64]int{0: 1},
		failErr: map[int64]error{0: tgerr.New(420, "FLOOD_WAIT_977")},
	}

	sleeps := &sleepRecorder{}

	fetch := RangedFetch(srv, RangedOptions{retries: 3, backoff: time.Millisecond, sleep: sleeps.sleep})

	in := Input{
		Item:     store.MediaItem{ChatID: 1, MessageID: 2, MediaIndex: 3, MediaClass: "video", MediaID: 7, Size: int64ptr(1)},
		Location: &tg.InputDocumentFileLocation{ID: 7},
	}

	_, err := fetch(t.Context(), in, &memWriterAt{})
	require.Error(t, err)

	wait, flood := tgerr.AsFloodWait(err)
	require.True(t, flood, "the long wait must climb as a flood error")
	assert.Equal(t, 977*time.Second, wait, "the server-mandated duration must survive untouched")
	assert.Empty(t, sleeps.slept)
}

// TestRangedFetchExactChunkTotalNeedsNoTail pins the known-total
// boundary: an exact multiple of the chunk size stops on the last full
// chunk without a trailing request.
func TestRangedFetchExactChunkTotalNeedsNoTail(t *testing.T) {
	t.Parallel()

	data := make([]byte, rangedChunkSize)

	srv := &rangedServer{data: data, failN: map[int64]int{}, failErr: map[int64]error{}}

	rec := &sleepRecorder{}

	fetch, in := rangedTestFetch(srv, rec, int64ptr(rangedChunkSize), 0, nil)

	dest := &memWriterAt{}

	written, err := fetch(t.Context(), *in, dest)
	require.NoError(t, err)

	assert.EqualValues(t, rangedChunkSize, written)
	require.Len(t, srv.calls, 1, "a full final chunk satisfies the total")
}

// TestRangedFetchUnknownSizeStopsOnEmpty pins the unknown-total EOF
// semantics: an empty answer past the data ends the transfer without a
// size to compare against.
func TestRangedFetchUnknownSizeStopsOnEmpty(t *testing.T) {
	t.Parallel()

	data := make([]byte, rangedChunkSize)

	srv := &rangedServer{data: data, failN: map[int64]int{}, failErr: map[int64]error{}}

	rec := &sleepRecorder{}

	fetch, in := rangedTestFetch(srv, rec, nil, 0, nil)

	dest := &memWriterAt{}

	written, err := fetch(t.Context(), *in, dest)
	require.NoError(t, err)

	assert.EqualValues(t, rangedChunkSize, written)
	assert.Equal(t, data, dest.data)

	require.Len(t, srv.calls, 2, "an unknown total probes once past the end")
	assert.EqualValues(t, rangedChunkSize, srv.calls[1].offset, "the probe rides the next aligned offset")
}

// TestRangedFetchResumesAtUnalignedOffset pins mid-chunk resume: the
// first request floors onto the 4096 boundary, the already-present
// prefix of the answer is dropped, nothing below the resume offset is
// written and only the tail transfers.
func TestRangedFetchResumesAtUnalignedOffset(t *testing.T) {
	t.Parallel()

	data := make([]byte, rangedChunkSize+2000)
	for idx := range data {
		data[idx] = byte(idx % 251)
	}

	offset := int64(rangedChunkAlign + 123) // 4219: deliberately unaligned

	srv := &rangedServer{data: data, failN: map[int64]int{}, failErr: map[int64]error{}}

	rec := &sleepRecorder{}

	fetch, in := rangedTestFetch(srv, rec, int64ptr(int64(len(data))), offset, nil)

	dest := &memWriterAt{}

	written, err := fetch(t.Context(), *in, dest)
	require.NoError(t, err)

	assert.Equal(t, int64(len(data))-offset, written, "only the missing tail counts")

	require.Len(t, srv.calls, 1, "one aligned request spans the remaining tail")

	assert.EqualValues(t, rangedChunkAlign, srv.calls[0].offset, "the request floors onto the 4096 boundary")
	assert.Equal(t, make([]byte, offset), dest.data[:offset], "bytes below the resume offset must stay untouched")
	assert.Equal(t, data[offset:], dest.data[offset:])
}

// failWriterAt rejects every write.
type failWriterAt struct{}

func (failWriterAt) WriteAt([]byte, int64) (int, error) {
	return 0, errNotImplemented
}

// TestRangedFetchWriteFailureSurfaces pins that destination errors are
// never retried: disk failures climb immediately.
func TestRangedFetchWriteFailureSurfaces(t *testing.T) {
	t.Parallel()

	srv := &rangedServer{data: []byte("0123456789"), failN: map[int64]int{}, failErr: map[int64]error{}}

	rec := &sleepRecorder{}

	fetch, in := rangedTestFetch(srv, rec, int64ptr(10), 0, nil)

	_, err := fetch(t.Context(), *in, failWriterAt{})
	require.Error(t, err)
	require.ErrorIs(t, err, errNotImplemented)

	require.Len(t, srv.calls, 1, "a write failure must not re-request the chunk")
}

// TestRangedFetchNonTransientSurfaces pins that fatal rpc errors climb
// on the first answer: no budget is spent on unrecoverable failures.
func TestRangedFetchNonTransientSurfaces(t *testing.T) {
	t.Parallel()

	srv := &rangedServer{
		data:    []byte("0123456789"),
		failN:   map[int64]int{0: 1},
		failErr: map[int64]error{0: tgerr.New(403, "TAKEOUT_FILE_TOO_BIG")},
	}

	rec := &sleepRecorder{}

	fetch, in := rangedTestFetch(srv, rec, int64ptr(10), 0, nil)

	_, err := fetch(t.Context(), *in, &memWriterAt{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TAKEOUT_FILE_TOO_BIG")

	require.Len(t, srv.calls, 1, "fatal errors must not retry")
	assert.Empty(t, rec.slept)
}

// TestFetchForSelectsRangedWhenPoolsNil pins the wire: nil pools (the
// takeout single-connection session) selects the ranged engine. A fatal
// rpc answer climbs on the first request, so no backoff schedule runs.
func TestFetchForSelectsRangedWhenPoolsNil(t *testing.T) {
	t.Parallel()

	srv := &rangedServer{
		data:    []byte("0123456789"),
		failN:   map[int64]int{0: 1},
		failErr: map[int64]error{0: tgerr.New(403, "TAKEOUT_FILE_TOO_BIG")},
	}

	fetch := FetchFor(nil, srv, FetchOptions{})

	_, err := fetch(t.Context(), parallelInput(0), &memWriterAt{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ranged download", "nil pools must ride the ranged engine")
}

// TestFetchForKeepsParallelWithPools pins the wire: pooled runs keep the
// gotd parallel engine unchanged.
func TestFetchForKeepsParallelWithPools(t *testing.T) {
	t.Parallel()

	boom := errors.New("pool rpc down")

	pools := &fakePools{
		byDC:  map[int]downloader.Client{0: fakeInvoker{dc: 1}},
		fails: map[int]error{0: boom},
	}

	fetch := FetchFor(pools, fakeInvoker{dc: 9}, FetchOptions{Threads: 2})

	_, err := fetch(t.Context(), parallelInput(0), &memWriterAt{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parallel download", "pooled runs must keep the parallel engine")
}

// rangedResolver maps every item onto <root>/file.bin with a fixed
// location.
type rangedResolver struct{ root string }

func (r rangedResolver) Resolve(store.MediaItem) (Resolved, error) {
	return Resolved{
		Path:     r.root + "/file.bin",
		Location: &tg.InputDocumentFileLocation{ID: 7},
	}, nil
}

// TestRangedManagerSurvivesRepeatedDrops is the end-to-end core
// assertion: drops exceeding the chunk budget burn one ladder attempt
// and the re-entry receives the CURRENT on-disk offset — no request
// below the already-persisted bytes ever fires, and the file completes.
func TestRangedManagerSurvivesRepeatedDrops(t *testing.T) {
	t.Parallel()

	total := int64(2*rangedChunkSize + 500)

	data := make([]byte, total)
	for idx := range data {
		data[idx] = byte(idx % 251)
	}

	// Three drops at the second chunk exceed the chunk budget of two;
	// the ladder's second attempt re-enters at the persisted offset.
	srv := &rangedServer{
		data:    data,
		failN:   map[int64]int{rangedChunkSize: 3},
		failErr: map[int64]error{},
	}

	sleeps := &sleepRecorder{}

	state, err := store.Open(t.TempDir() + "/state.db")
	require.NoError(t, err)

	t.Cleanup(func() { _ = state.Close() })

	mgr := NewManager(
		state,
		pace.New(pace.Config{Concurrency: 1, FloodSleepThreshold: time.Hour}),
		Config{
			Concurrency: 1,
			RetryMax:    3,
			ClaimBatch:  10,
			Dedupe:      "unique-id",
			BackoffBase: time.Millisecond,
			HookTimeout: time.Second,
			Output:      config.Output{Collision: "index", PartSuffix: ".part"},
		},
		rangedResolver{root: t.TempDir()},
		NoopReporter{},
	)

	mgr.Fetch = RangedFetch(srv, RangedOptions{retries: 2, backoff: time.Millisecond, sleep: sleeps.sleep})

	size := total

	require.NoError(t, mgr.Enqueue(t.Context(), []store.MediaItem{{
		ChatID:     1,
		MessageID:  2,
		MediaIndex: 3,
		MediaID:    7,
		MediaClass: "document",
		Size:       &size,
	}}))

	res, err := mgr.Run(t.Context(), "run-ranged-drops")
	require.NoError(t, err)

	assert.EqualValues(t, 1, res.Downloaded, "the file must complete across the drops")
	assert.EqualValues(t, 0, res.Failed)
	assert.EqualValues(t, 1, res.Retries, "the exhausted chunk budget costs exactly one ladder retry")

	srv.mu.Lock()
	defer srv.mu.Unlock()

	// Zero-redownload: after the budget burns at the second chunk, the
	// next request targets the CURRENT offset — never byte zero.
	zeroOffsetRequests := 0

	for _, call := range srv.calls {
		if call.offset == 0 {
			zeroOffsetRequests++
		}
	}

	assert.Equal(t, 1, zeroOffsetRequests, "chunk zero must be requested exactly once across ALL attempts")

	// The ladder re-entry at the persisted offset: the call after the
	// budget exhaustion targets the same second chunk, not byte zero.
	require.GreaterOrEqual(t, len(srv.calls), 6)

	assert.EqualValues(t, rangedChunkSize, srv.calls[4].offset,
		"the re-entered attempt must resume at the persisted chunk boundary")
}

// TestRangedBackoffSchedule pins 1s→3s→9s growth with the cap.
func TestRangedBackoffSchedule(t *testing.T) {
	t.Parallel()

	base := time.Second

	assert.Equal(t, base, rangedBackoff(1, base))
	assert.Equal(t, 3*base, rangedBackoff(2, base))
	assert.Equal(t, 9*base, rangedBackoff(3, base))
	assert.Equal(t, 9*base, rangedBackoff(7, base), "the backoff caps at 9s")
}

// compile-time: the fake satisfies the gotd download client seam.
var _ downloader.Client = (*rangedServer)(nil)
