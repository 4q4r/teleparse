package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/4q4r/teleparse/internal/store"

	"github.com/gotd/td/telegram/downloader"
	tg "github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errNotImplemented and errPoolDownTest back the fetch test doubles.
var (
	errNotImplemented = errors.New("not implemented")
	errPoolDownTest   = errors.New("pool down")
)

// fakeInvoker is a distinguishable downloader.Client stand-in.
type fakeInvoker struct{ dc int }

func (f fakeInvoker) UploadGetFile( //nolint:ireturn // mirrors the gotd seam
	context.Context, *tg.UploadGetFileRequest,
) (tg.UploadFileClass, error) {
	return nil, errNotImplemented
}

func (f fakeInvoker) UploadGetFileHashes(
	context.Context, *tg.UploadGetFileHashesRequest,
) ([]tg.FileHash, error) {
	return nil, errNotImplemented
}

func (f fakeInvoker) UploadReuploadCDNFile(
	context.Context, *tg.UploadReuploadCDNFileRequest,
) ([]tg.FileHash, error) {
	return nil, errNotImplemented
}

func (f fakeInvoker) UploadGetCDNFileHashes(
	context.Context, *tg.UploadGetCDNFileHashesRequest,
) ([]tg.FileHash, error) {
	return nil, errNotImplemented
}

func (f fakeInvoker) UploadGetWebFile(
	context.Context, *tg.UploadGetWebFileRequest,
) (*tg.UploadWebFile, error) {
	return nil, errNotImplemented
}

// fakePools records InvokerFor traffic and answers from a fixed table.
type fakePools struct {
	byDC  map[int]downloader.Client
	fails map[int]error
	asked []int
}

func (p *fakePools) InvokerFor(_ context.Context, dc int) (downloader.Client, error) { //nolint:ireturn // mirrors the gotd seam
	p.asked = append(p.asked, dc)

	if err, ok := p.fails[dc]; ok {
		return nil, err
	}

	return p.byDC[dc], nil
}

// recordingRunner captures every gotd download invocation; the script maps
// call index to outcome.
type recordingRunner struct {
	calls  []runnerCall
	script []func(call runnerCall) error
}

type runnerCall struct {
	rpc     fakeInvoker
	threads int
	onRetry downloader.RetryHandler
	dest    io.WriterAt
}

func (r *recordingRunner) run(_ context.Context, rpc downloader.Client, _ tg.InputFileLocationClass,
	threads int, onRetry downloader.RetryHandler, dest io.WriterAt,
) error {
	call := runnerCall{
		rpc:     mustFakeInvoker(rpc),
		threads: threads,
		onRetry: onRetry,
		dest:    dest,
	}

	r.calls = append(r.calls, call)

	step := r.script[len(r.calls)-1]

	return step(call)
}

// mustFakeInvoker unwraps the recorded rpc stand-in; a wrong type is a
// programmer error in the test double.
func mustFakeInvoker(rpc downloader.Client) fakeInvoker {
	cast, ok := rpc.(fakeInvoker)
	if !ok {
		panic(fmt.Sprintf("unexpected invoker %T", rpc))
	}

	return cast
}

// throttleRecorder captures ItemReporter throttle surfacing.
type throttleRecorder struct {
	NoopReporter
	waits []int
}

func (t *throttleRecorder) Throttled(seconds int)                  { t.waits = append(t.waits, seconds) }
func (t *throttleRecorder) ItemStart(string, string, int64, int64) {}
func (t *throttleRecorder) ItemProgress(string, int64)             {}
func (t *throttleRecorder) ItemDone(string, bool)                  {}

func parallelInput(dc int) Input {
	return Input{
		Item:     store.MediaItem{ChatID: 1, MessageID: 2, MediaIndex: 3, MediaClass: "video", MediaID: 7},
		Offset:   0,
		Location: &tg.InputDocumentFileLocation{ID: 7},
		DC:       dc,
	}
}

func TestParallelFetchThreadsAndPoolSelection(t *testing.T) {
	t.Parallel()

	inv2, inv4 := fakeInvoker{dc: 2}, fakeInvoker{dc: 4}

	pools := &fakePools{byDC: map[int]downloader.Client{2: inv2, 4: inv4}}

	runner := &recordingRunner{script: []func(call runnerCall) error{
		func(call runnerCall) error {
			_, err := call.dest.WriteAt([]byte("hello"), 0)

			return err
		},
	}}

	fetch := ParallelFetch(pools, fakeInvoker{dc: 0}, ParallelOptions{Threads: 5, run: runner.run})

	written, err := fetch(t.Context(), parallelInput(2), &memWriterAt{})
	require.NoError(t, err)
	assert.EqualValues(t, 0, written)

	require.Len(t, runner.calls, 1)
	assert.Equal(t, 2, runner.calls[0].rpc.dc, "must route through the DC 2 pool")
	assert.Equal(t, 5, runner.calls[0].threads, "threads must reach the gotd builder")
	assert.Equal(t, []int{2}, pools.asked)
}

func TestParallelFetchUnknownDCUsesHomePool(t *testing.T) {
	t.Parallel()

	home := fakeInvoker{dc: 1}

	pools := &fakePools{byDC: map[int]downloader.Client{0: home}}

	runner := &recordingRunner{script: []func(call runnerCall) error{
		func(runnerCall) error { return nil },
	}}

	fetch := ParallelFetch(pools, fakeInvoker{dc: 9}, ParallelOptions{Threads: 1, run: runner.run})

	_, err := fetch(t.Context(), parallelInput(0), &memWriterAt{})
	require.NoError(t, err)

	assert.Equal(t, []int{0}, pools.asked, "DC 0 must ask for the home pool")
	assert.Equal(t, 1, runner.calls[0].rpc.dc)
}

func TestParallelFetchPoolFailureFallsBackToSingleConnection(t *testing.T) {
	t.Parallel()

	fallback := fakeInvoker{dc: 9}

	pools := &fakePools{
		byDC:  map[int]downloader.Client{},
		fails: map[int]error{2: errPoolDownTest},
	}

	runner := &recordingRunner{script: []func(call runnerCall) error{
		func(runnerCall) error { return nil },
	}}

	fetch := ParallelFetch(pools, fallback, ParallelOptions{Threads: 2, run: runner.run})

	_, err := fetch(t.Context(), parallelInput(2), &memWriterAt{})
	require.NoError(t, err)

	assert.Equal(t, 9, runner.calls[0].rpc.dc, "pool failure must fall back to the primary connection")
}

func TestParallelFetchRetriesOnceOnFileMigrate(t *testing.T) {
	t.Parallel()

	inv2, inv4 := fakeInvoker{dc: 2}, fakeInvoker{dc: 4}

	pools := &fakePools{byDC: map[int]downloader.Client{2: inv2, 4: inv4}}

	runner := &recordingRunner{script: []func(call runnerCall) error{
		func(runnerCall) error { return tgerr.New(303, "FILE_MIGRATE_4") },
		func(call runnerCall) error {
			_, err := call.dest.WriteAt([]byte("moved"), 0)

			return err
		},
	}}

	fetch := ParallelFetch(pools, fakeInvoker{dc: 0}, ParallelOptions{Threads: 3, run: runner.run})

	_, err := fetch(t.Context(), parallelInput(2), &memWriterAt{})
	require.NoError(t, err)

	require.Len(t, runner.calls, 2, "FILE_MIGRATE must trigger exactly one retry")
	assert.Equal(t, 2, runner.calls[0].rpc.dc)
	assert.Equal(t, 4, runner.calls[1].rpc.dc, "retry must target the migrated DC")
	assert.Equal(t, []int{2, 4}, pools.asked)
}

func TestParallelFetchMigrateExhaustionReturnsError(t *testing.T) {
	t.Parallel()

	pools := &fakePools{byDC: map[int]downloader.Client{2: fakeInvoker{dc: 2}}}

	runner := &recordingRunner{script: []func(call runnerCall) error{
		func(runnerCall) error { return tgerr.New(303, "FILE_MIGRATE_4") },
		func(runnerCall) error { return tgerr.New(303, "FILE_MIGRATE_4") },
	}}

	fetch := ParallelFetch(pools, fakeInvoker{dc: 0}, ParallelOptions{Threads: 1, run: runner.run})

	_, err := fetch(t.Context(), parallelInput(2), &memWriterAt{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FILE_MIGRATE")
	require.Len(t, runner.calls, 2, "only one migrate retry is allowed")
}

func TestParallelFetchSurfacesFloodWaits(t *testing.T) {
	t.Parallel()

	recorder := &throttleRecorder{}

	runner := &recordingRunner{script: []func(call runnerCall) error{
		func(call runnerCall) error {
			call.onRetry(downloader.RetryEvent{
				Operation: "reader.chunk", Attempt: 1,
				Err: tgerr.New(420, "FLOOD_PREMIUM_WAIT_7"),
			})

			call.onRetry(downloader.RetryEvent{
				Operation: "reader.chunk", Attempt: 2,
				Err: tgerr.New(420, "FLOOD_WAIT_3"),
			})

			_, err := call.dest.WriteAt([]byte("data"), 0)

			return err
		},
	}}

	fetch := ParallelFetch(nil, fakeInvoker{dc: 1}, ParallelOptions{Threads: 1, Reporter: recorder, run: runner.run})

	_, err := fetch(t.Context(), parallelInput(1), &memWriterAt{})
	require.NoError(t, err)
	assert.Equal(t, []int{7, 3}, recorder.waits, "both flood wait flavors must surface")
}

func TestParallelFetchSkipsAlreadyPresentBytes(t *testing.T) {
	t.Parallel()

	runner := &recordingRunner{script: []func(call runnerCall) error{
		func(call runnerCall) error {
			_, err := call.dest.WriteAt([]byte("skipmeok"), 0)

			return err
		},
	}}

	fetch := ParallelFetch(nil, fakeInvoker{dc: 1}, ParallelOptions{Threads: 1, run: runner.run})

	in := parallelInput(1)
	in.Offset = 6

	dest := &memWriterAt{}

	_, err := fetch(t.Context(), in, dest)
	require.NoError(t, err)
	assert.Equal(t, "ok", string(dest.data[6:]), "bytes below the resume offset must be dropped")
}

// memWriterAt is an in-memory WriterAt.
type memWriterAt struct{ data []byte }

func (m *memWriterAt) WriteAt(chunk []byte, off int64) (int, error) {
	end := off + int64(len(chunk))

	if int64(len(m.data)) < end {
		m.data = append(m.data, make([]byte, end-int64(len(m.data)))...)
	}

	copy(m.data[off:end], chunk)

	return len(chunk), nil
}

// TestParallelFetchNilPoolsRidesFallback pins the takeout path: with no
// pools at all (takeout forbids raw media connections), downloads must
// ride the fallback client (the takeout-wrapped API).
func TestParallelFetchNilPoolsRidesFallback(t *testing.T) {
	t.Parallel()

	fallback := fakeInvoker{dc: 9}

	runner := &recordingRunner{script: []func(call runnerCall) error{
		func(runnerCall) error { return nil },
	}}

	fetch := ParallelFetch(nil, fallback, ParallelOptions{Threads: 4, run: runner.run})

	_, err := fetch(t.Context(), parallelInput(2), &memWriterAt{})
	require.NoError(t, err)

	require.Len(t, runner.calls, 1)
	assert.Equal(t, 9, runner.calls[0].rpc.dc, "nil pools must use the fallback client")
	assert.Equal(t, 4, runner.calls[0].threads)
}
