package download_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"teleparse/internal/config"
	"teleparse/internal/download"
	"teleparse/internal/store"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// itemRecorder captures every ItemReporter call.
type itemRecorder struct {
	download.NoopReporter

	mu        sync.Mutex
	starts    []startCall
	deltas    map[string]int64
	doneState map[string]bool
}

type startCall struct {
	key    string
	name   string
	total  int64
	offset int64
}

func newItemRecorder() *itemRecorder {
	return &itemRecorder{deltas: map[string]int64{}, doneState: map[string]bool{}}
}

func (r *itemRecorder) ItemStart(key, name string, total, offset int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.starts = append(r.starts, startCall{key: key, name: name, total: total, offset: offset})
}

func (r *itemRecorder) ItemProgress(key string, delta int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.deltas[key] += delta
}

func (r *itemRecorder) ItemDone(key string, failed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.doneState[key] = failed
}

func (r *itemRecorder) Throttled(int) {}

func TestManagerReportsByteProgress(t *testing.T) {
	t.Parallel()

	state := newTestStore(t)

	recorder := newItemRecorder()

	root := t.TempDir()

	mgr := download.NewManager(state, newPacer(0), managerConfig(),
		fixedResolver{root: root, name: "file.bin"}, recorder)
	mgr.Fetch = byteSource("0123456789")

	require.NoError(t, mgr.Enqueue(t.Context(), []store.MediaItem{queuedItem(1, 2, 10)}))

	res, err := mgr.Run(t.Context(), "run-1")
	require.NoError(t, err)
	assert.EqualValues(t, 1, res.Downloaded)

	require.Len(t, recorder.starts, 1)

	want := startCall{key: "1/2/0", name: "file.bin", total: 10, offset: 0}

	assert.Equal(t, want, recorder.starts[0], "ItemStart carries key, label, size and resume offset")
	assert.EqualValues(t, 10, recorder.deltas["1/2/0"], "byte deltas must sum to the transferred size")
	assert.False(t, recorder.doneState["1/2/0"], "successful item must retire as not failed")
}

func TestManagerReportsFailedItemDone(t *testing.T) {
	t.Parallel()

	state := newTestStore(t)

	recorder := newItemRecorder()

	root := t.TempDir()

	boom := errors.New("boom")

	mgr := download.NewManager(state, newPacer(0), managerConfig(),
		fixedResolver{root: root, name: "file.bin"}, recorder)
	mgr.Fetch = func(context.Context, download.Input, io.WriterAt) (int64, error) {
		return 0, boom
	}

	require.NoError(t, mgr.Enqueue(t.Context(), []store.MediaItem{queuedItem(1, 2, 10)}))

	res, err := mgr.Run(t.Context(), "run-1")
	require.NoError(t, err)
	assert.EqualValues(t, 1, res.Failed)
	assert.True(t, recorder.doneState["1/2/0"], "exhausted item must retire as failed")
}

func TestPlainReportersStayItemSilent(t *testing.T) {
	t.Parallel()

	state := newTestStore(t)

	root := t.TempDir()

	mgr := download.NewManager(state, newPacer(0), managerConfig(),
		fixedResolver{root: root, name: "file.bin"}, download.NoopReporter{})
	mgr.Fetch = byteSource("abc")

	require.NoError(t, mgr.Enqueue(t.Context(), []store.MediaItem{queuedItem(1, 2, 3)}))

	res, err := mgr.Run(t.Context(), "run-1")
	require.NoError(t, err)
	assert.EqualValues(t, 1, res.Downloaded, "NoopReporter runs must behave exactly as before")
}

func TestManagerForwardsResolvedDC(t *testing.T) {
	t.Parallel()

	state := newTestStore(t)

	var seenDC []int

	root := t.TempDir()

	mgr := download.NewManager(state, newPacer(0), managerConfig(),
		dcResolver{root: root, dc: 4}, download.NoopReporter{})
	mgr.Fetch = func(_ context.Context, in download.Input, dest io.WriterAt) (int64, error) {
		seenDC = append(seenDC, in.DC)

		written, err := dest.WriteAt([]byte("0123456789"), 0)

		return int64(written), err
	}

	require.NoError(t, mgr.Enqueue(t.Context(), []store.MediaItem{queuedItem(1, 2, 10)}))

	_, err := mgr.Run(t.Context(), "run-1")
	require.NoError(t, err)
	assert.Equal(t, []int{4}, seenDC, "Resolved.DC must reach the fetch Input")
}

// dcResolver is a fixedResolver that also carries a file DC.
type dcResolver struct {
	root string
	dc   int
}

func (r dcResolver) Resolve(item store.MediaItem) (download.Resolved, error) {
	resolved, err := (fixedResolver{root: r.root, name: "file.bin"}).Resolve(item)
	if err != nil {
		return download.Resolved{}, err
	}

	resolved.DC = r.dc

	return resolved, nil
}

var _ = config.Output{} // keep config imported for managerConfig parity
