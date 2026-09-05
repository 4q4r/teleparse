package download_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/config"
	"github.com/4q4r/teleparse/internal/download"
	"github.com/4q4r/teleparse/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chatPathResolver renders per-chat final paths so equal files sighted in
// different chats never collide: <root>/chat_<id>/msg_<msgid>.bin.
type chatPathResolver struct {
	root string
}

func (r chatPathResolver) Resolve(item store.MediaItem) (download.Resolved, error) {
	rel := filepath.Join(
		"chat_"+strconv.FormatInt(item.ChatID, 10),
		"msg_"+strconv.FormatInt(item.MessageID, 10)+".bin",
	)

	return download.Resolved{Path: filepath.Join(r.root, rel)}, nil
}

// occurrence builds a queued media row sighting the given unique file.
func occurrence(chatID, msgID, mediaID int64) store.MediaItem {
	return store.MediaItem{
		ChatID:     chatID,
		MessageID:  msgID,
		MediaID:    mediaID,
		MediaClass: "document",
		Filename:   strPtr("shared.bin"),
		Status:     store.StatusQueued,
	}
}

func strPtr(text string) *string { return &text }

// dedupeConfig builds a manager config with the given dedupe mode writing
// under root.
func dedupeConfig(root, mode string, mods ...func(*download.Config)) download.Config {
	cfg := download.Config{
		Concurrency: 2,
		RetryMax:    3,
		ClaimBatch:  10,
		Dedupe:      mode,
		Root:        root,
		BackoffBase: time.Millisecond,
		HookTimeout: 2 * time.Second,
		Output: config.Output{
			Collision:  "index",
			PartSuffix: ".part",
		},
	}
	for _, mod := range mods {
		mod(&cfg)
	}

	return cfg
}

func newDedupeManager(t *testing.T, state *store.Store, cfg download.Config,
	fetch download.FetchFunc,
) *download.Manager {
	t.Helper()

	mgr := download.NewManager(state, newPacer(time.Minute), cfg,
		chatPathResolver{root: cfg.Root}, download.NoopReporter{})
	mgr.Fetch = fetch

	return mgr
}

// countingFetch wraps byteSource with a call counter.
func countingFetch(content string, counter *atomic.Int64) download.FetchFunc {
	return func(ctx context.Context, in download.Input, dest io.WriterAt) (int64, error) {
		counter.Add(1)

		return byteSource(content)(ctx, in, dest)
	}
}

func blobPathOf(root string, mediaID int64) string {
	return download.BlobPath(root, "document", mediaID, ".bin")
}

func chatFinal(root string, chatID, msgID int64) string {
	return filepath.Join(root,
		"chat_"+strconv.FormatInt(chatID, 10),
		"msg_"+strconv.FormatInt(msgID, 10)+".bin")
}

// sameFile asserts two paths resolve to one inode.
func sameFile(t *testing.T, first, second string) {
	t.Helper()

	firstInfo, err := os.Stat(first)
	require.NoError(t, err)

	secondInfo, err := os.Stat(second)
	require.NoError(t, err)

	assert.True(t, os.SameFile(firstInfo, secondInfo), "%s and %s must share one inode", first, second)
}

func fileContentIs(t *testing.T, path, want string) {
	t.Helper()

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, want, string(got), "content of %s", path)
}

func TestBlobPathDerivation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	assert.Equal(t,
		filepath.Join(root, ".teleparse", "blobs", "document", "55.jpg"),
		download.BlobPath(root, "document", 55, ".jpg"))
	assert.Equal(t,
		filepath.Join(root, ".teleparse", "blobs", "photo", "7"),
		download.BlobPath(root, "photo", 7, ""))
	assert.Equal(t, filepath.Join(root, ".teleparse", "blobs"), download.BlobRoot(root))
}

func TestHardlinkServesEveryChatWithOneFetch(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	root := t.TempDir()

	var fetches atomic.Int64

	mgr := newDedupeManager(t, st, dedupeConfig(root, "hardlink"),
		countingFetch("shared-bytes", &fetches))

	require.NoError(t, mgr.Enqueue(context.Background(),
		[]store.MediaItem{occurrence(1, 10, 500), occurrence(2, 20, 500)}))

	res, err := mgr.Run(context.Background(), "run-hardlink-2")
	require.NoError(t, err)

	assert.EqualValues(t, 1, res.Downloaded)
	assert.EqualValues(t, 1, res.Linked)
	assert.EqualValues(t, 1, fetches.Load(), "the second chat must be served by a link, not a fetch")

	blob := blobPathOf(root, 500)

	for _, path := range []string{blob, chatFinal(root, 1, 10), chatFinal(root, 2, 20)} {
		fileContentIs(t, path, "shared-bytes")
	}

	sameFile(t, blob, chatFinal(root, 1, 10))
	sameFile(t, blob, chatFinal(root, 2, 20))
}

func TestHardlinkThirdOccurrenceLinks(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	root := t.TempDir()

	var fetches atomic.Int64

	mgr := newDedupeManager(t, st, dedupeConfig(root, "hardlink"),
		countingFetch("tri", &fetches))

	require.NoError(t, mgr.Enqueue(context.Background(),
		[]store.MediaItem{occurrence(1, 10, 900), occurrence(2, 20, 900), occurrence(3, 30, 900)}))

	res, err := mgr.Run(context.Background(), "run-hardlink-3")
	require.NoError(t, err)

	assert.EqualValues(t, 1, res.Downloaded)
	assert.EqualValues(t, 2, res.Linked)
	assert.EqualValues(t, 1, fetches.Load())

	blob := blobPathOf(root, 900)

	for _, path := range []string{
		blob, chatFinal(root, 1, 10), chatFinal(root, 2, 20), chatFinal(root, 3, 30),
	} {
		fileContentIs(t, path, "tri")
	}

	sameFile(t, blob, chatFinal(root, 3, 30))
}

func TestHardlinkDeletingAnyCopyNeverBreaksSurvivors(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	root := t.TempDir()

	var fetches atomic.Int64

	mgr := newDedupeManager(t, st, dedupeConfig(root, "hardlink"),
		countingFetch("precious", &fetches))

	require.NoError(t, mgr.Enqueue(context.Background(),
		[]store.MediaItem{occurrence(1, 10, 700), occurrence(2, 20, 700), occurrence(3, 30, 700)}))

	_, err := mgr.Run(context.Background(), "run-hardlink-del")
	require.NoError(t, err)

	blob := blobPathOf(root, 700)

	require.NoError(t, os.Remove(chatFinal(root, 1, 10)))
	fileContentIs(t, chatFinal(root, 2, 20), "precious")
	fileContentIs(t, chatFinal(root, 3, 30), "precious")
	fileContentIs(t, blob, "precious")

	require.NoError(t, os.Remove(chatFinal(root, 2, 20)))
	fileContentIs(t, chatFinal(root, 3, 30), "precious")
	fileContentIs(t, blob, "precious")

	// Even with every chat copy gone the blob itself keeps the data alive.
	require.NoError(t, os.Remove(chatFinal(root, 3, 30)))
	fileContentIs(t, blob, "precious")
}

func TestHardlinkRelinksWithoutNetworkAfterFinalDeleted(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	root := t.TempDir()

	var fetches atomic.Int64

	first := newDedupeManager(t, st, dedupeConfig(root, "hardlink"),
		countingFetch("bytes", &fetches))
	require.NoError(t, first.Enqueue(context.Background(), []store.MediaItem{occurrence(1, 10, 500)}))

	_, err := first.Run(context.Background(), "run-relink-1")
	require.NoError(t, err)

	require.NoError(t, os.Remove(chatFinal(root, 1, 10)))

	second := newDedupeManager(t, st, dedupeConfig(root, "hardlink"),
		countingFetch("bytes", &fetches))
	require.NoError(t, second.Enqueue(context.Background(), []store.MediaItem{occurrence(2, 20, 500)}))

	res, err := second.Run(context.Background(), "run-relink-2")
	require.NoError(t, err)

	assert.EqualValues(t, 1, fetches.Load(), "the blob survives, so the new chat must link without network")
	assert.EqualValues(t, 1, res.Linked)
	assert.EqualValues(t, 0, res.Downloaded)

	fileContentIs(t, chatFinal(root, 2, 20), "bytes")
	sameFile(t, blobPathOf(root, 500), chatFinal(root, 2, 20))
}

func TestHardlinkRedownloadsBlobWhenDeleted(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	root := t.TempDir()

	var fetches atomic.Int64

	first := newDedupeManager(t, st, dedupeConfig(root, "hardlink"),
		countingFetch("reborn", &fetches))
	require.NoError(t, first.Enqueue(context.Background(), []store.MediaItem{occurrence(1, 10, 600)}))

	_, err := first.Run(context.Background(), "run-blob-del-1")
	require.NoError(t, err)

	require.NoError(t, os.Remove(blobPathOf(root, 600)))

	second := newDedupeManager(t, st, dedupeConfig(root, "hardlink"),
		countingFetch("reborn", &fetches))
	require.NoError(t, second.Enqueue(context.Background(), []store.MediaItem{occurrence(2, 20, 600)}))

	res, err := second.Run(context.Background(), "run-blob-del-2")
	require.NoError(t, err)

	assert.EqualValues(t, 2, fetches.Load(), "a deleted blob forces exactly one re-download")
	assert.EqualValues(t, 1, res.Downloaded)

	fileContentIs(t, blobPathOf(root, 600), "reborn")
	fileContentIs(t, chatFinal(root, 2, 20), "reborn")
	sameFile(t, blobPathOf(root, 600), chatFinal(root, 2, 20))
}

func TestHardlinkRedownloadsWhenEveryCopyDeleted(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	root := t.TempDir()

	var fetches atomic.Int64

	first := newDedupeManager(t, st, dedupeConfig(root, "hardlink"),
		countingFetch("phoenix", &fetches))
	require.NoError(t, first.Enqueue(context.Background(),
		[]store.MediaItem{occurrence(1, 10, 800), occurrence(2, 20, 800)}))

	_, err := first.Run(context.Background(), "run-allgone-1")
	require.NoError(t, err)

	for _, path := range []string{
		blobPathOf(root, 800), chatFinal(root, 1, 10), chatFinal(root, 2, 20),
	} {
		require.NoError(t, os.Remove(path))
	}

	second := newDedupeManager(t, st, dedupeConfig(root, "hardlink"),
		countingFetch("phoenix", &fetches))
	require.NoError(t, second.Enqueue(context.Background(), []store.MediaItem{occurrence(3, 30, 800)}))

	res, err := second.Run(context.Background(), "run-allgone-2")
	require.NoError(t, err)

	assert.EqualValues(t, 2, fetches.Load())
	assert.EqualValues(t, 1, res.Downloaded)

	fileContentIs(t, chatFinal(root, 3, 30), "phoenix")
	sameFile(t, blobPathOf(root, 800), chatFinal(root, 3, 30))
}

func TestHardlinkFallsBackToCopyWhenLinkFails(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	root := t.TempDir()

	var fetches atomic.Int64

	mgr := newDedupeManager(t, st, dedupeConfig(root, "hardlink"),
		countingFetch("copied", &fetches))
	mgr.Link = func(string, string) error {
		return &os.LinkError{Op: "link", Err: syscall.EXDEV} //nolint:goerr113 // simulated cross-device link failure
	}

	require.NoError(t, mgr.Enqueue(context.Background(),
		[]store.MediaItem{occurrence(1, 10, 400), occurrence(2, 20, 400)}))

	res, err := mgr.Run(context.Background(), "run-exdev")
	require.NoError(t, err)

	assert.EqualValues(t, 1, res.Downloaded)
	assert.EqualValues(t, 1, res.Linked)
	assert.EqualValues(t, 2, res.LinkCopies, "both served paths fell back to copies")

	fileContentIs(t, blobPathOf(root, 400), "copied")
	fileContentIs(t, chatFinal(root, 1, 10), "copied")
	fileContentIs(t, chatFinal(root, 2, 20), "copied")

	firstInfo, err := os.Stat(chatFinal(root, 1, 10))
	require.NoError(t, err)

	secondInfo, err := os.Stat(chatFinal(root, 2, 20))
	require.NoError(t, err)

	assert.False(t, os.SameFile(firstInfo, secondInfo), "fallback copies are independent inodes")
}

func TestDedupeModesMatrix(t *testing.T) {
	t.Parallel()

	for mode, chatTwoHasFile := range map[string]bool{
		"hardlink":  true,
		"unique-id": false,
		"off":       false,
	} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			st := newTestStore(t)

			root := t.TempDir()

			var fetches atomic.Int64

			mgr := newDedupeManager(t, st, dedupeConfig(root, mode),
				countingFetch("m", &fetches))
			require.NoError(t, mgr.Enqueue(context.Background(),
				[]store.MediaItem{occurrence(1, 10, 300), occurrence(2, 20, 300)}))

			_, err := mgr.Run(context.Background(), "run-matrix-"+mode)
			require.NoError(t, err)

			fileContentIs(t, chatFinal(root, 1, 10), "m")

			if chatTwoHasFile {
				fileContentIs(t, chatFinal(root, 2, 20), "m")
			} else {
				assert.NoFileExists(t, chatFinal(root, 2, 20),
					"mode %s must not produce a file in the second chat", mode)
			}
		})
	}
}

func TestHardlinkSkipExistingSkipsWithoutRelink(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	root := t.TempDir()

	var fetches atomic.Int64

	cfg := dedupeConfig(root, "hardlink", func(c *download.Config) { c.SkipExisting = true })

	require.NoError(t, os.MkdirAll(filepath.Dir(chatFinal(root, 1, 10)), 0o700))
	require.NoError(t, os.WriteFile(chatFinal(root, 1, 10), []byte("old"), 0o600))

	mgr := newDedupeManager(t, st, cfg, countingFetch("never", &fetches))
	require.NoError(t, mgr.Enqueue(context.Background(), []store.MediaItem{occurrence(1, 10, 200)}))

	res, err := mgr.Run(context.Background(), "run-skip-link")
	require.NoError(t, err)

	assert.EqualValues(t, 1, res.Skipped)
	assert.EqualValues(t, 0, fetches.Load())
	fileContentIs(t, chatFinal(root, 1, 10), "old")
	assert.NoFileExists(t, blobPathOf(root, 200), "skip-existing must not materialize a blob")
}

func TestBlobStatsCountsTrackedAndOrphaned(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	root := t.TempDir()

	tracked := occurrence(1, 10, 700)
	tracked.Status = store.StatusDone
	stored, err := st.UpsertMedia(context.Background(), &tracked)
	require.NoError(t, err)
	require.True(t, stored)

	trackedBlob := download.BlobPath(root, "document", 700, ".bin")
	require.NoError(t, os.MkdirAll(filepath.Dir(trackedBlob), 0o700))
	require.NoError(t, os.WriteFile(trackedBlob, make([]byte, 100), 0o600))

	orphanBlob := download.BlobPath(root, "photo", 800, ".jpg")
	require.NoError(t, os.MkdirAll(filepath.Dir(orphanBlob), 0o700))
	require.NoError(t, os.WriteFile(orphanBlob, make([]byte, 4), 0o600))

	report, err := download.BlobStats(context.Background(), st, root)
	require.NoError(t, err)

	assert.EqualValues(t, 2, report.Blobs)
	assert.EqualValues(t, 104, report.Bytes)
	assert.EqualValues(t, 1, report.Tracked)
	assert.EqualValues(t, 1, report.Orphaned)
}

func TestBlobGCRemovesOnlyOrphans(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	linked := download.BlobPath(root, "document", 100, ".bin")
	require.NoError(t, os.MkdirAll(filepath.Dir(linked), 0o700))
	require.NoError(t, os.WriteFile(linked, make([]byte, 10), 0o600))

	survivor := filepath.Join(root, "chat_1", "msg_10.bin")
	require.NoError(t, os.MkdirAll(filepath.Dir(survivor), 0o700))
	require.NoError(t, os.Link(linked, survivor))

	orphan := download.BlobPath(root, "photo", 200, ".jpg")
	require.NoError(t, os.MkdirAll(filepath.Dir(orphan), 0o700))
	require.NoError(t, os.WriteFile(orphan, make([]byte, 6), 0o600))

	removed, freed, err := download.BlobGC(root)
	require.NoError(t, err)

	assert.EqualValues(t, 1, removed)
	assert.EqualValues(t, 6, freed)
	assert.NoFileExists(t, orphan)
	assert.FileExists(t, linked, "a blob with surviving links must never be collected")
	fileContentIs(t, survivor, string(make([]byte, 10)))
}
