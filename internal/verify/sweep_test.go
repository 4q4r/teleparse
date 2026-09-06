package verify_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/4q4r/teleparse/internal/download"
	"github.com/4q4r/teleparse/internal/store"
	"github.com/4q4r/teleparse/internal/verify"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sweepEnv is one temp store plus its downloads root, holding a few
// ready-made manifest rows.
type sweepEnv struct {
	state *store.Store
	root  string
}

// newSweepEnv opens a fresh store inside a temp dir and returns it with
// the downloads root.
func newSweepEnv(t *testing.T) *sweepEnv {
	t.Helper()

	dir := t.TempDir()

	state, err := store.Open(filepath.Join(dir, "state.db"))
	require.NoError(t, err)

	t.Cleanup(func() { _ = state.Close() })

	return &sweepEnv{state: state, root: filepath.Join(dir, "root")}
}

// addDoneRow inserts a done manifest row and returns its final path and
// blob path.
func (e *sweepEnv) addDoneRow(t *testing.T, chatID, mediaID int64, name string, size int64, sum string,
) (string, string) {
	t.Helper()

	finalPath := filepath.Join(e.root, "chat", name)
	blobPath := download.BlobPath(e.root, "document", mediaID, filepath.Ext(name))

	stored, err := e.state.UpsertMedia(t.Context(), &store.MediaItem{
		ChatID: chatID, MessageID: 100, MediaIndex: 0,
		MediaClass: "document", MediaID: mediaID,
		Filename: &name, Size: &size,
		Status: store.StatusDone, Path: &finalPath, Sha256: &sum,
	})
	require.NoError(t, err)
	require.True(t, stored, "the row must insert")

	return finalPath, blobPath
}

// writeFinal writes the final-path file with the row's expected content.
func writeFinal(t *testing.T, path string, content []byte) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, content, 0o600))
}

// writeBlob writes the canonical blob file for a media id.
func writeBlob(t *testing.T, path string, content []byte) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, content, 0o600))
}

// contentSum returns the lowercase hex sha256 of the fixture content.
func contentSum(content []byte) string {
	digest := sha256.Sum256(content)

	return hex.EncodeToString(digest[:])
}

func TestSweepClassifiesIntactRowAsOK(t *testing.T) {
	t.Parallel()

	env := newSweepEnv(t)

	content := []byte("intact document bytes")

	finalPath, _ := env.addDoneRow(t, 1, 10, "report.bin", int64(len(content)), contentSum(content))
	writeFinal(t, finalPath, content)

	report, err := verify.Sweep(t.Context(), env.state, verify.Options{Root: env.root})
	require.NoError(t, err)

	assert.Equal(t, 1, report.Rows)
	assert.Equal(t, 1, report.Classes[verify.ClassOK])
	assert.Empty(t, report.Findings)
	assert.Zero(t, report.Problems)
}

func TestSweepReportsMissingWhenBothCopiesGone(t *testing.T) {
	t.Parallel()

	env := newSweepEnv(t)

	env.addDoneRow(t, 1, 11, "gone.bin", 16, "")

	report, err := verify.Sweep(t.Context(), env.state, verify.Options{Root: env.root})
	require.NoError(t, err)

	require.Len(t, report.Findings, 1)
	assert.Equal(t, verify.ClassMissing, report.Findings[0].Class)
	assert.Equal(t, 1, report.Problems)
}

func TestSweepReportsFinalMissingBlobAliveAndFixRelinks(t *testing.T) {
	t.Parallel()

	env := newSweepEnv(t)

	content := []byte("blob holds the only surviving copy")

	finalPath, blobPath := env.addDoneRow(t, 1, 12, "photo.jpg", int64(len(content)), "")
	writeBlob(t, blobPath, content)

	report, err := verify.Sweep(t.Context(), env.state, verify.Options{Root: env.root})
	require.NoError(t, err)

	require.Len(t, report.Findings, 1)
	assert.Equal(t, verify.ClassFinalMissingBlobAlive, report.Findings[0].Class)
	assert.Equal(t, int64(1), report.Findings[0].ChatID, "the finding carries its chat id")

	fixed, err := verify.Sweep(t.Context(), env.state, verify.Options{Root: env.root, Fix: true})
	require.NoError(t, err)

	assert.Equal(t, 1, fixed.Repair.Relinked, "the blob is linked back onto the final path")
	assert.Zero(t, fixed.Remaining)

	finalInfo, err := os.Stat(finalPath)
	require.NoError(t, err, "the final path exists again")

	blobInfo, err := os.Stat(blobPath)
	require.NoError(t, err)

	assert.True(t, os.SameFile(finalInfo, blobInfo), "the repaired final must be the blob's inode")
}

func TestSweepFixRequeuesMissingRow(t *testing.T) {
	t.Parallel()

	env := newSweepEnv(t)

	env.addDoneRow(t, 1, 13, "both-gone.bin", 8, "")

	report, err := verify.Sweep(t.Context(), env.state, verify.Options{Root: env.root, Fix: true})
	require.NoError(t, err)

	assert.Equal(t, 1, report.Repair.Requeued)

	item, exists, err := env.state.MediaByFile(t.Context(), "document", 13)
	require.NoError(t, err)
	require.True(t, exists)

	assert.Equal(t, store.StatusQueued, item.Status)
	assert.Zero(t, item.Attempts, "the retry budget is restored")
	assert.Zero(t, report.Remaining)
}

func TestSweepReportsSizeMismatchAndRequeuesOnFix(t *testing.T) {
	t.Parallel()

	env := newSweepEnv(t)

	finalPath, _ := env.addDoneRow(t, 1, 14, "short.bin", 64, "")
	writeFinal(t, finalPath, []byte("too short"))

	report, err := verify.Sweep(t.Context(), env.state, verify.Options{Root: env.root})
	require.NoError(t, err)

	require.Len(t, report.Findings, 1)
	assert.Equal(t, verify.ClassSizeMismatch, report.Findings[0].Class)

	fixed, err := verify.Sweep(t.Context(), env.state, verify.Options{Root: env.root, Fix: true})
	require.NoError(t, err)

	assert.Equal(t, 1, fixed.Repair.Requeued)

	item, exists, err := env.state.MediaByFile(t.Context(), "document", 14)
	require.NoError(t, err)
	require.True(t, exists)

	assert.Equal(t, store.StatusQueued, item.Status)
}

func TestSweepDeepDetectsHashMismatch(t *testing.T) {
	t.Parallel()

	env := newSweepEnv(t)

	content := []byte("bytes that no longer match the recorded digest")

	finalPath, _ := env.addDoneRow(t, 1, 15, "drift.bin", int64(len(content)),
		contentSum([]byte("original bytes")))
	writeFinal(t, finalPath, content)

	fast, err := verify.Sweep(t.Context(), env.state, verify.Options{Root: env.root})
	require.NoError(t, err)
	assert.Empty(t, fast.Findings, "fast mode never re-hashes")

	deep, err := verify.Sweep(t.Context(), env.state, verify.Options{Root: env.root, Deep: true})
	require.NoError(t, err)

	require.Len(t, deep.Findings, 1)
	assert.Equal(t, verify.ClassHashMismatch, deep.Findings[0].Class)
}

func TestSweepSkipsRowsWithoutPath(t *testing.T) {
	t.Parallel()

	env := newSweepEnv(t)

	queued, err := env.state.UpsertMedia(t.Context(), &store.MediaItem{
		ChatID: 1, MessageID: 200, MediaClass: "document", MediaID: 16,
		Status: store.StatusQueued, Attempts: 1,
	})
	require.NoError(t, err)
	require.True(t, queued)

	report, err := verify.Sweep(t.Context(), env.state, verify.Options{Root: env.root})
	require.NoError(t, err)

	assert.Equal(t, 1, report.SkippedNoPath)
	assert.Empty(t, report.Findings)
	assert.Zero(t, report.Problems)
}

func TestSweepListsOrphanBlobsAndFixGcsThem(t *testing.T) {
	t.Parallel()

	env := newSweepEnv(t)

	orphan := download.BlobPath(env.root, "photo", 800, ".jpg")
	writeBlob(t, orphan, []byte("no manifest row references this"))

	report, err := verify.Sweep(t.Context(), env.state, verify.Options{Root: env.root})
	require.NoError(t, err)

	assert.Equal(t, 1, report.Orphaned)
	require.Len(t, report.Findings, 1)
	assert.Equal(t, verify.ClassOrphanBlob, report.Findings[0].Class)
	assert.Equal(t, orphan, report.Findings[0].Path)

	fixed, err := verify.Sweep(t.Context(), env.state, verify.Options{Root: env.root, Fix: true})
	require.NoError(t, err)

	assert.Equal(t, int64(1), fixed.Repair.GcRemoved)
	assert.NoFileExists(t, orphan)
	assert.Zero(t, fixed.Remaining)
}

func TestSweepDeepValidatesZipFormat(t *testing.T) {
	t.Parallel()

	env := newSweepEnv(t)

	// A zip with a flipped byte mid-member fails the native CRC walk; the
	// recorded size matches the file so classification reaches the format
	// check.
	corrupt := corruptZip(t)

	finalPath, _ := env.addDoneRow(t, 1, 17, "takeout.zip", int64(len(corrupt)), "")
	writeFinal(t, finalPath, corrupt)

	deep, err := verify.Sweep(t.Context(), env.state, verify.Options{Root: env.root, Deep: true})
	require.NoError(t, err)

	require.Len(t, deep.Findings, 1)
	assert.Equal(t, verify.ClassFormatError, deep.Findings[0].Class)

	fixed, err := verify.Sweep(t.Context(), env.state, verify.Options{Root: env.root, Deep: true, Fix: true})
	require.NoError(t, err)

	assert.Equal(t, 1, fixed.Remaining, "format errors are not auto-repaired")
}

func TestSweepDeepMarksUnknownFormatsUncheckedWithoutFailing(t *testing.T) {
	t.Parallel()

	env := newSweepEnv(t)

	content := []byte("opaque binary payload")

	finalPath, _ := env.addDoneRow(t, 1, 18, "video.mkv", int64(len(content)), "")
	writeFinal(t, finalPath, content)

	deep, err := verify.Sweep(t.Context(), env.state, verify.Options{Root: env.root, Deep: true})
	require.NoError(t, err)

	assert.Equal(t, 1, deep.Classes[verify.ClassUncheckedFormat])
	assert.Empty(t, deep.Findings)
	assert.Zero(t, deep.Problems, "unchecked formats are coverage notes, not damage")
}

func TestSweepFiltersChats(t *testing.T) {
	t.Parallel()

	env := newSweepEnv(t)

	content := []byte("chat one row")

	finalPath, _ := env.addDoneRow(t, 1, 19, "one.bin", int64(len(content)), "")
	writeFinal(t, finalPath, content)

	env.addDoneRow(t, 2, 20, "two.bin", 8, "")

	report, err := verify.Sweep(t.Context(), env.state, verify.Options{Root: env.root, Chats: []int64{2}})
	require.NoError(t, err)

	assert.Equal(t, 1, report.Rows, "only the filtered chat's rows are walked")
	assert.Equal(t, 1, report.Classes[verify.ClassMissing])
}

// corruptZip builds a small zip archive and flips one byte inside the
// deflate stream so the native validator fails the member CRC.
func corruptZip(t *testing.T) []byte {
	t.Helper()

	var output bytes.Buffer

	zipWriter := zip.NewWriter(&output)

	entry, err := zipWriter.CreateHeader(&zip.FileHeader{Name: "a.txt", Method: zip.Deflate})
	require.NoError(t, err)

	_, err = entry.Write(bytes.Repeat([]byte("z"), 512))
	require.NoError(t, err)

	require.NoError(t, zipWriter.Close())

	raw := output.Bytes()
	require.Greater(t, len(raw), 48, "fixture must be large enough to corrupt")

	corrupted := make([]byte, len(raw))
	copy(corrupted, raw)

	corrupted[42] ^= 0xFF

	return corrupted
}
