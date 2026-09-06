package cli_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/4q4r/teleparse/internal/config"
	"github.com/4q4r/teleparse/internal/download"
	"github.com/4q4r/teleparse/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// verifyEnv is one disposable CLI environment: an isolated config/data
// home, an open store seeded by the test, and a downloads root.
type verifyEnv struct {
	t       *testing.T
	root    string
	cfgPath string
	state   *store.Store
}

// newVerifyEnv isolates XDG directories and opens the store the CLI will
// use. Tests cannot run in parallel because of t.Setenv.
func newVerifyEnv(t *testing.T) *verifyEnv {
	t.Helper()

	dataHome := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", dataHome)

	cfgPath := filepath.Join(t.TempDir(), "config.toml")

	_, paths, err := config.Load(cfgPath)
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(filepath.Dir(paths.StateDB), 0o700))

	state, err := store.Open(paths.StateDB)
	require.NoError(t, err)

	t.Cleanup(func() { _ = state.Close() })

	return &verifyEnv{t: t, root: t.TempDir(), cfgPath: cfgPath, state: state}
}

// run executes the verify command with the env's root and config.
func (e *verifyEnv) run(args ...string) (string, error) {
	e.t.Helper()

	return execute(e.t, append([]string{"--root", e.root, "--config", e.cfgPath, "verify"}, args...)...)
}

// addDoneRow seeds one done manifest row and returns the final and blob
// paths derived exactly like the download tree lays them out.
func (e *verifyEnv) addDoneRow(chatID, mediaID int64, name string, size int64) (string, string) {
	e.t.Helper()

	finalPath := filepath.Join(e.root, "chat", name)
	blobPath := download.BlobPath(e.root, "document", mediaID, filepath.Ext(name))

	path := finalPath

	stored, err := e.state.UpsertMedia(e.t.Context(), &store.MediaItem{
		ChatID: chatID, MessageID: 100, MediaClass: "document", MediaID: mediaID,
		Filename: &name, Size: &size, Status: store.StatusDone, Path: &path,
	})
	require.NoError(e.t, err)
	require.True(e.t, stored)

	return finalPath, blobPath
}

// writeFile creates the file with parent directories.
func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, content, 0o600))
}

func TestVerifyCommandRegistered(t *testing.T) {
	t.Parallel()

	out, err := execute(t, "--help")
	require.NoError(t, err)
	assert.Contains(t, out, "verify", "root help should list the verify command")

	_, err = execute(t, "verify", "--help")
	require.NoError(t, err)
}

func TestVerifyCleanTreeExitsZero(t *testing.T) {
	env := newVerifyEnv(t)

	finalPath, _ := env.addDoneRow(1, 10, "ok.bin", 8)
	writeFile(t, finalPath, []byte("all good"))

	out, err := env.run()
	require.NoError(t, err, "a clean tree must exit zero")
	assert.Contains(t, out, "ok")
	assert.Contains(t, out, "problems: 0")
}

func TestVerifyReportsMissingAndExitsNonZero(t *testing.T) {
	env := newVerifyEnv(t)

	env.addDoneRow(1, 11, "gone.bin", 8)

	out, err := env.run()
	require.Error(t, err, "problems without --fix must exit non-zero")
	assert.Contains(t, err.Error(), "verify")
	assert.Contains(t, out, "missing")
	assert.Contains(t, out, "gone.bin")
}

func TestVerifyFixRelinksFinalFromBlobWithoutNetwork(t *testing.T) {
	env := newVerifyEnv(t)

	content := []byte("the blob survived")

	finalPath, blobPath := env.addDoneRow(1, 12, "photo.jpg", int64(len(content)))
	writeFile(t, blobPath, content)

	_, err := env.run("--fix")
	require.NoError(t, err, "a relinked row leaves no remaining problems")

	finalInfo, err := os.Stat(finalPath)
	require.NoError(t, err, "the final path was restored")

	blobInfo, err := os.Stat(blobPath)
	require.NoError(t, err)

	assert.True(t, os.SameFile(finalInfo, blobInfo), "the repair hardlinks the blob inode")
}

func TestVerifyFixRequeuesMissingRow(t *testing.T) {
	env := newVerifyEnv(t)

	env.addDoneRow(1, 13, "both-gone.bin", 8)

	_, err := env.run("--fix")
	require.NoError(t, err)

	item, exists, err := env.state.MediaByFile(t.Context(), "document", 13)
	require.NoError(t, err)
	require.True(t, exists)

	assert.Equal(t, store.StatusQueued, item.Status)
	assert.Zero(t, item.Attempts)
}

func TestVerifyListsAndGcsOrphanBlobs(t *testing.T) {
	env := newVerifyEnv(t)

	orphan := download.BlobPath(env.root, "photo", 800, ".jpg")
	writeFile(t, orphan, []byte("orphaned bytes"))

	out, err := env.run()
	require.Error(t, err, "an orphan blob is a problem")
	assert.Contains(t, out, "orphan-blob")

	fixedOut, err := env.run("--fix")
	require.NoError(t, err)
	assert.Contains(t, fixedOut, "gc")
	assert.NoFileExists(t, orphan)
}

// verifyPayload decodes just the fields the tests assert on, keeping JSON
// numbers as ints instead of floats.
type verifyPayload struct {
	Rows      int            `json:"rows"`
	Problems  int            `json:"problems"`
	Remaining int            `json:"remaining"`
	Classes   map[string]int `json:"classes"`
	Findings  []struct {
		Class    string `json:"class"`
		Filename string `json:"filename"`
	} `json:"findings"`
	Repair struct {
		Relinked int `json:"relinked"`
	} `json:"repair"`
}

// decodeVerifyPayload parses the command's JSON output.
func decodeVerifyPayload(t *testing.T, out string) verifyPayload {
	t.Helper()

	var payload verifyPayload

	require.NoError(t, json.Unmarshal([]byte(out), &payload))

	return payload
}

func TestVerifyJSONEnvelopSchema(t *testing.T) {
	env := newVerifyEnv(t)

	env.addDoneRow(1, 14, "missing.bin", 8)

	out, err := env.run("--format", "json")
	require.Error(t, err, "json output still exits non-zero on problems")

	payload := decodeVerifyPayload(t, out)

	assert.Equal(t, 1, payload.Rows)
	assert.Equal(t, 1, payload.Problems)
	assert.Equal(t, 1, payload.Remaining)
	assert.Equal(t, 1, payload.Classes["missing"])

	require.Len(t, payload.Findings, 1)
	assert.Equal(t, "missing", payload.Findings[0].Class)
	assert.Equal(t, "missing.bin", payload.Findings[0].Filename)
	assert.Zero(t, payload.Repair.Relinked, "the repair object decodes with its keys intact")
}

func TestVerifyPlainFormat(t *testing.T) {
	env := newVerifyEnv(t)

	env.addDoneRow(1, 15, "plain.bin", 8)

	out, err := env.run("--format", "plain")
	require.Error(t, err)
	assert.Contains(t, out, "problems: 1")
	assert.Contains(t, out, "class: missing")
	assert.Contains(t, out, "path: ")
}

func TestVerifyChatFiltersByIDAndTitle(t *testing.T) {
	env := newVerifyEnv(t)

	kept, _ := env.addDoneRow(1, 16, "kept.bin", 2)
	writeFile(t, kept, []byte("ok"))

	env.addDoneRow(2, 17, "other.bin", 8)

	title := "News Feed"
	require.NoError(t, env.state.UpsertChat(t.Context(), store.Chat{ChatID: 3, Type: "channel", Title: &title}))

	threaded, _ := env.addDoneRow(3, 18, "thread.zip", 2)
	writeFile(t, threaded, []byte("ok"))

	out, err := env.run("--format", "json", "1")
	require.NoError(t, err, "the intact chat 1 passes; the damaged chat 2 is filtered out")

	payload := decodeVerifyPayload(t, out)
	assert.Equal(t, 1, payload.Rows)

	out, err = env.run("--format", "json", "news")
	require.NoError(t, err, "the title filter matches the intact News Feed chat only")

	payload = decodeVerifyPayload(t, out)
	assert.Equal(t, 1, payload.Rows)
	assert.Zero(t, payload.Problems)

	out, err = env.run("--format", "json", "all")
	require.Error(t, err, "all walks every chat including the missing row")

	payload = decodeVerifyPayload(t, out)
	assert.Equal(t, 3, payload.Rows)
}

func TestVerifyDeepDetectsCorruptZip(t *testing.T) {
	env := newVerifyEnv(t)

	// Build a valid zip, then flip one byte inside the deflate stream so
	// the native CRC walk fails under --deep.
	var (
		buffer bytes.Buffer
		body   []byte
	)

	writer := zip.NewWriter(&buffer)

	entry, err := writer.CreateHeader(&zip.FileHeader{Name: "a.txt", Method: zip.Deflate})
	require.NoError(t, err)

	_, err = entry.Write(bytes.Repeat([]byte("z"), 512))
	require.NoError(t, err)

	require.NoError(t, writer.Close())

	body = buffer.Bytes()
	body[42] ^= 0xFF

	finalPath, _ := env.addDoneRow(1, 19, "takeout.zip", int64(len(body)))
	writeFile(t, finalPath, body)

	fastOut, fastErr := env.run()
	require.NoError(t, fastErr, "fast mode stops at existence and size")
	assert.NotContains(t, fastOut, "format-error")

	deepOut, deepErr := env.run("--deep")
	require.Error(t, deepErr, "deep mode catches the corrupt member")
	assert.Contains(t, deepOut, "takeout.zip")
	assert.Contains(t, deepOut, "format-error")

	fixedOut, fixErr := env.run("--deep", "--fix")
	require.Error(t, fixErr, "format errors are not auto-repaired")
	assert.Contains(t, fixedOut, "format-error")
}
