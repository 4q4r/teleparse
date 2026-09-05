package download_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/download"
	"github.com/4q4r/teleparse/internal/filters"
	"github.com/4q4r/teleparse/internal/store"

	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// messageDate is a fixed unix timestamp feeding {date} renders.
const messageDate = 1750000000

// templateResolver resolves items through the real RenderTemplate with
// per-chat titles and per-message file metadata, mirroring the CLI resolver.
type templateResolver struct {
	root     string
	template string
	titles   map[int64]string
	files    map[int64]*filters.FileInfo
}

// Resolve renders the item's relative path and attaches a sidecar meta.
func (r templateResolver) Resolve(item store.MediaItem) (download.Resolved, error) {
	file := r.files[item.MessageID]

	fctx := filters.Context{
		Chat:    filters.Chat{ID: item.ChatID, Type: "channel", Title: r.titles[item.ChatID]},
		Message: filters.Message{ID: item.MessageID, Date: messageDate},
		File:    file,
	}

	rel, err := download.RenderTemplate(r.template, fctx, file)
	if err != nil {
		return download.Resolved{}, fmt.Errorf("render path for %d: %w", item.MessageID, err)
	}

	meta := &download.SidecarMeta{ChatID: item.ChatID, ChatTitle: r.titles[item.ChatID], MessageID: item.MessageID}

	return download.Resolved{Path: filepath.Join(r.root, rel), Meta: meta}, nil
}

// contentByMsg returns a FetchFunc writing per-message content from
// Input.Offset, simulating ranged byte sources.
func contentByMsg(contents map[int64]string) download.FetchFunc {
	return func(_ context.Context, in download.Input, dest io.WriterAt) (int64, error) {
		content := contents[in.Item.MessageID]

		offset := in.Offset
		if offset > int64(len(content)) {
			offset = int64(len(content))
		}

		written, err := dest.WriteAt([]byte(content[offset:]), offset)

		return int64(written), err
	}
}

// sizedQueuedItem returns a queued item whose size matches its content.
func sizedQueuedItem(chatID, msgID int64, content string) store.MediaItem {
	size := int64(len(content))

	return store.MediaItem{
		ChatID:     chatID,
		MessageID:  msgID,
		MediaID:    msgID,
		MediaClass: "document",
		Size:       &size,
		Status:     store.StatusQueued,
	}
}

func TestLifecycleHappyPathDownloadsAllItems(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	root := t.TempDir()

	resolver := templateResolver{
		root: root,
		// {filename} already carries the extension, mirroring the default
		// production template; appending {ext} would double it.
		template: "{chat}/{msgid}_{filename}",
		titles:   map[int64]string{1: "News", 2: "Docs"},
		files: map[int64]*filters.FileInfo{
			10: {Present: true, Kind: "document", Name: "a.jpg", Ext: ".jpg", Size: 11},
			11: {Present: true, Kind: "document", Name: "b.png", Ext: ".png", Size: 4},
			20: {Present: true, Kind: "document", Name: "c.bin", Ext: ".bin", Size: 7},
		},
	}

	contents := map[int64]string{10: "hello world", 11: "data", 20: "payload"}

	cfg := managerConfig(func(c *download.Config) { c.Output.Sidecar = true })

	mgr := download.NewManager(st, newPacer(time.Minute), cfg, resolver, download.NoopReporter{})
	mgr.Fetch = contentByMsg(contents)

	ctx := context.Background()
	require.NoError(t, st.CreateRun(ctx, &store.Run{RunID: "run-happy", Account: "main", FilterJSON: "{}"}))
	enqueue(t, mgr, sizedQueuedItem(1, 10, contents[10]),
		sizedQueuedItem(1, 11, contents[11]), sizedQueuedItem(2, 20, contents[20]))

	res, err := mgr.Run(ctx, "run-happy")
	require.NoError(t, err)

	assert.EqualValues(t, 3, res.Downloaded)
	assert.EqualValues(t, 0, res.Skipped)
	assert.EqualValues(t, 0, res.Failed)
	assert.EqualValues(t, 22, res.Bytes)

	paths := []string{
		filepath.Join(root, "News", "10_a.jpg"),
		filepath.Join(root, "News", "11_b.png"),
		filepath.Join(root, "Docs", "20_c.bin"),
	}

	for _, path := range paths {
		assert.FileExists(t, path, "templated final path must exist")
		assert.NoFileExists(t, path+".part", "part file must be renamed away")
	}

	raw, err := os.ReadFile(paths[0] + ".json")
	require.NoError(t, err, "sidecar must be written next to the first item")

	var meta download.SidecarMeta

	require.NoError(t, json.Unmarshal(raw, &meta)) //nolint:musttag // mirrors the sidecar marshal side
	assert.Equal(t, int64(10), meta.MessageID)
	assert.Equal(t, "News", meta.ChatTitle)

	for _, item := range []struct {
		class  string
		fileID int64
	}{
		{"document", 10}, {"document", 11}, {"document", 20},
	} {
		row, exists, err := st.MediaByFile(ctx, item.class, item.fileID)
		require.NoError(t, err)
		require.True(t, exists)
		assert.Equal(t, store.StatusDone, row.Status)
		require.NotNil(t, row.Path)
		assert.Contains(t, paths, *row.Path)
	}

	// The CLI finishes the run after a clean Manager exit; mirror it here.
	require.NoError(t, st.FinishRun(ctx, "run-happy", store.StatusDone, ""))

	run, exists, err := st.GetRun(ctx, "run-happy")
	require.NoError(t, err)
	require.True(t, exists)
	assert.Equal(t, store.StatusDone, run.Status)
}

func TestLifecycleFloodWaitParkAndResume(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	root := t.TempDir()

	resolver := templateResolver{
		root:     root,
		template: "{msgid}.bin",
		titles:   map[int64]string{1: "News"},
		files:    map[int64]*filters.FileInfo{},
	}

	var floodCalls atomic.Int64

	floodFetch := func(context.Context, download.Input, io.WriterAt) (int64, error) {
		floodCalls.Add(1)

		return 0, tgerr.New(420, "FLOOD_WAIT_300")
	}

	mgr := download.NewManager(st, newPacer(time.Minute), managerConfig(), resolver, download.NoopReporter{})
	mgr.Fetch = floodFetch

	ctx := context.Background()
	require.NoError(t, st.CreateRun(ctx, &store.Run{RunID: "run-flood", Account: "main", FilterJSON: "{}"}))

	first := sizedQueuedItem(1, 1, "aaaaa")
	second := sizedQueuedItem(1, 2, "bbbbb")

	// One item only: the park outcome must be deterministic before the
	// resume phase widens the scenario back to a full chat.
	enqueue(t, mgr, first)

	res, err := mgr.Run(ctx, "run-flood")
	require.NoError(t, err)

	assert.True(t, res.Parked, "300s flood wait exceeds the 60s threshold and must park")
	assert.WithinDuration(t, time.Now().Add(300*time.Second), res.ResumeAt, 5*time.Second)
	assert.GreaterOrEqual(t, floodCalls.Load(), int64(1))

	run, exists, err := st.GetRun(ctx, "run-flood")
	require.NoError(t, err)
	require.True(t, exists)
	assert.Equal(t, store.StatusParked, run.Status)
	require.NotNil(t, run.ResumeAt)

	parsed, err := time.Parse(time.RFC3339Nano, *run.ResumeAt)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(300*time.Second), parsed, 5*time.Second)

	counts, err := st.Counts(ctx)
	require.NoError(t, err)
	assert.Zero(t, counts[store.StatusDone], "nothing may finish while parked")

	// Parked mid-flight: the row keeps its downloading claim so no parallel
	// run can steal it; crash recovery is what requeues it.
	parkedRow, exists, err := st.MediaByFile(ctx, "document", 1)
	require.NoError(t, err)
	require.True(t, exists)
	assert.Equal(t, store.StatusDownloading, parkedRow.Status, "a parked item keeps its claim")

	// Resume: crash recovery requeues the dangling row, the walk-equivalent
	// re-enqueue resets them and a fresh Manager drains the remainder.
	reset, err := st.ResetDownloading(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), reset, "exactly the parked item is requeued")

	contents := map[int64]string{1: "aaaaa", 2: "bbbbb"}

	mgr2 := download.NewManager(st, newPacer(time.Minute), managerConfig(), resolver, download.NoopReporter{})
	mgr2.Fetch = contentByMsg(contents)

	require.NoError(t, st.CreateRun(ctx, &store.Run{RunID: "run-resumed", Account: "main", FilterJSON: "{}"}))
	enqueue(t, mgr2, first, second)

	res2, err := mgr2.Run(ctx, "run-resumed")
	require.NoError(t, err)
	assert.EqualValues(t, 2, res2.Downloaded)
	assert.False(t, res2.Parked)

	require.NoError(t, st.FinishRun(ctx, "run-resumed", store.StatusDone, ""))

	run2, exists, err := st.GetRun(ctx, "run-resumed")
	require.NoError(t, err)
	require.True(t, exists)
	assert.Equal(t, store.StatusDone, run2.Status)

	assert.FileExists(t, filepath.Join(root, "1.bin"))
	assert.FileExists(t, filepath.Join(root, "2.bin"))

	parked, exists, err := st.GetRun(ctx, "run-flood")
	require.NoError(t, err)
	require.True(t, exists)
	assert.Equal(t, store.StatusParked, parked.Status, "the parked run keeps its record after resume")
}

func TestLifecycleCrashResumeUsesPartFileSizeAsOffset(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	root := t.TempDir()

	resolver := templateResolver{
		root:     root,
		template: "file.bin",
		titles:   map[int64]string{1: "News"},
		files:    map[int64]*filters.FileInfo{},
	}

	var offsetSeen atomic.Int64

	fetch := func(_ context.Context, in download.Input, dest io.WriterAt) (int64, error) {
		offsetSeen.Store(in.Offset)

		written, err := dest.WriteAt([]byte(" world"), in.Offset)

		return int64(written), err
	}

	mgr := download.NewManager(st, newPacer(time.Minute), managerConfig(), resolver, download.NoopReporter{})
	mgr.Fetch = fetch

	// Crash aftermath: 5 bytes already on disk while the stale store row
	// claims 2; the part file size is the only trusted resume offset.
	item := sizedQueuedItem(1, 5, "hello world")
	item.BytesDone = 2

	enqueue(t, mgr, item)
	require.NoError(t, os.WriteFile(filepath.Join(root, "file.bin.part"), []byte("hello"), 0o600))

	res, err := mgr.Run(context.Background(), "run-crash")
	require.NoError(t, err)
	assert.EqualValues(t, 1, res.Downloaded)
	assert.EqualValues(t, 5, offsetSeen.Load(), "resume must continue at the on-disk part size, not bytes_done")

	got, err := os.ReadFile(filepath.Join(root, "file.bin"))
	require.NoError(t, err)
	assert.Equal(t, "hello world", string(got), "final file must contain part bytes plus fetched remainder")

	row, exists, err := st.MediaByFile(context.Background(), "document", 5)
	require.NoError(t, err)
	require.True(t, exists)
	assert.Equal(t, store.StatusDone, row.Status)
}

func TestLifecycleRetryLadderBackoffAndSuccess(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	var calls atomic.Int64

	fetch := func(ctx context.Context, in download.Input, dest io.WriterAt) (int64, error) {
		if calls.Add(1) < 3 {
			return 0, errors.New("transient network hiccup")
		}

		return contentByMsg(map[int64]string{9: "ok"})(ctx, in, dest)
	}

	// Pacing in this ladder: the Manager owns the exponential attempt
	// backoff (base*2^(attempt-1), capped at 30s); the pacer contributes
	// only the inter-download jitter delay, which is zero here. The exact
	// call count also pins the claim invariant: the feeder must never
	// re-claim a row whose retry ladder is still running.
	cfg := managerConfig(func(c *download.Config) { c.BackoffBase = 5 * time.Millisecond })

	root, mgr := newManagerIn(t, st, cfg, fetch, nil)

	enqueue(t, mgr, sizedQueuedItem(1, 9, "ok"))

	start := time.Now()

	res, err := mgr.Run(context.Background(), "run-retry-ladder")
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.EqualValues(t, 1, res.Downloaded)
	assert.EqualValues(t, 3, calls.Load(), "two failures then success, with no second worker re-running the ladder")
	assert.GreaterOrEqual(t, elapsed, 15*time.Millisecond, "backoff 5ms+10ms must elapse before the third attempt")

	assert.FileExists(t, filepath.Join(root, "file.bin"))

	row, _, err := st.MediaByFile(context.Background(), "document", 9)
	require.NoError(t, err)
	assert.Equal(t, store.StatusDone, row.Status)
	assert.Equal(t, 2, row.Attempts, "two failures are recorded before the successful attempt")
}

func TestLifecycleDedupeUniqueIDAcrossChats(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	root := t.TempDir()

	resolver := templateResolver{
		root:     root,
		template: "{msgid}.bin",
		titles:   map[int64]string{1: "News", 2: "Docs"},
		files:    map[int64]*filters.FileInfo{},
	}

	contents := map[int64]string{1: "first"}

	var fetches atomic.Int64

	countingFetch := func(ctx context.Context, in download.Input, dest io.WriterAt) (int64, error) {
		fetches.Add(1)

		return contentByMsg(contents)(ctx, in, dest)
	}

	mgr := download.NewManager(st, newPacer(time.Minute), managerConfig(), resolver, download.NoopReporter{})
	mgr.Fetch = countingFetch

	ctx := context.Background()

	// Phase 1: chat 1 downloads media file 1 and marks it done.
	enqueue(t, mgr, sizedQueuedItem(1, 1, "first"))

	res, err := mgr.Run(ctx, "run-dedupe-one")
	require.NoError(t, err)
	assert.EqualValues(t, 1, res.Downloaded)
	assert.EqualValues(t, 1, fetches.Load())

	// Phase 2: chat 2 discovers the same unique media id. Enqueue skips it
	// entirely (no row, no chat registration), so the second Run is a no-op
	// and the done row keeps pointing at chat 1.
	duplicate := sizedQueuedItem(2, 9, "first")
	duplicate.MediaID = 1

	enqueue(t, mgr, duplicate)

	res2, err := mgr.Run(ctx, "run-dedupe-two")
	require.NoError(t, err)
	assert.EqualValues(t, 0, res2.Downloaded)
	assert.EqualValues(t, 0, res2.Skipped)
	assert.EqualValues(t, 1, res2.Duplicates, "the done-file skip counts as a duplicate")
	assert.EqualValues(t, 1, fetches.Load(), "the duplicate must never reach Fetch")

	row, exists, err := st.MediaByFile(ctx, "document", 1)
	require.NoError(t, err)
	require.True(t, exists)
	assert.Equal(t, int64(1), row.MessageID, "the original done row wins")
	assert.Equal(t, int64(1), row.ChatID)

	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "exactly one file may land on disk")
}

// TestManagerEnqueueDuplicateAcrossChats reproduces the forwarded-file
// crash: the same unique media id enqueued for two different chats in one
// batch must never abort Enqueue, must count one duplicate, and must leave
// a folder only for the chat that owns the first sighting.
func TestManagerEnqueueDuplicateAcrossChats(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	root := t.TempDir()

	resolver := templateResolver{
		root:     root,
		template: "{chat}/{msgid}_{filename}",
		titles:   map[int64]string{1: "News", 2: "Docs"},
		files: map[int64]*filters.FileInfo{
			5: {Present: true, Kind: "document", Name: "a.jpg", Ext: ".jpg", Size: 5},
		},
	}

	mgr := download.NewManager(st, newPacer(time.Minute), managerConfig(), resolver, download.NoopReporter{})
	mgr.Fetch = byteSource("hello")

	ctx := context.Background()
	require.NoError(t, st.CreateRun(ctx, &store.Run{RunID: "run-dup", Account: "main", FilterJSON: "{}"}))

	original := sizedQueuedItem(1, 5, "hello")
	duplicate := sizedQueuedItem(2, 6, "hello")
	duplicate.MediaID = original.MediaID

	enqueue(t, mgr, original, duplicate)

	res, err := mgr.Run(ctx, "run-dup")
	require.NoError(t, err, "duplicate files must never abort the run")
	assert.EqualValues(t, 1, res.Downloaded)
	assert.EqualValues(t, 1, res.Duplicates, "the forwarded copy counts once")

	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, 1, "only the first-sighting chat may create a folder")
	assert.Equal(t, "News", entries[0].Name())
}

func TestLifecycleHookPathSubstitution(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	// runHooks splits the rendered command on whitespace, so hook templates
	// must be Fields-safe: quoted compound commands (sh -c '...') do not
	// survive the split. A cp with {path} on both sides proves substitution.
	cfg := managerConfig(func(c *download.Config) {
		c.Hooks.PostDownload = []string{"cp {path} {path}.hook"}
	})

	root, mgr := newManagerIn(t, st, cfg, byteSource("payload"), nil)

	enqueue(t, mgr, sizedQueuedItem(1, 3, "payload"))

	res, err := mgr.Run(context.Background(), "run-hook")
	require.NoError(t, err)
	assert.EqualValues(t, 1, res.Downloaded)

	original, err := os.ReadFile(filepath.Join(root, "file.bin"))
	require.NoError(t, err)

	hooked, err := os.ReadFile(filepath.Join(root, "file.bin.hook"))
	require.NoError(t, err, "hook must receive the substituted final path")

	assert.Equal(t, string(original), string(hooked))
}
