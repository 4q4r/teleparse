package download_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/config"
	"github.com/4q4r/teleparse/internal/download"
	"github.com/4q4r/teleparse/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chatDirResolver maps every item onto <root>/chat_<chatID>/file_<msgID>.bin
// with sidecar metadata naming the chat, mirroring a {chat}/-templated run.
type chatDirResolver struct {
	root string
}

func (r chatDirResolver) Resolve(item store.MediaItem) (download.Resolved, error) {
	rel := fmt.Sprintf("chat_%d/file_%d.bin", item.ChatID, item.MessageID)

	return download.Resolved{
		Path: filepath.Join(r.root, rel),
		Meta: &download.SidecarMeta{
			ChatID: item.ChatID, ChatTitle: fmt.Sprintf("Chat %d", item.ChatID), MessageID: item.MessageID,
		},
	}, nil
}

// newChatManifestManager builds a Manager whose resolver nests files under
// a per-chat directory, the shape [output] metadata = "chat" aggregates.
func newChatManifestManager(t *testing.T, root string, cfg download.Config) *download.Manager {
	t.Helper()

	cfg.Root = root

	st := newTestStore(t)
	mgr := download.NewManager(st, newPacer(time.Minute), cfg, chatDirResolver{root: root}, download.NoopReporter{})
	mgr.Fetch = byteSource("data")

	return mgr
}

func manifestItems(chatID int64, msgIDs ...int64) []store.MediaItem {
	items := make([]store.MediaItem, 0, len(msgIDs))

	for _, msgID := range msgIDs {
		item := queuedItem(chatID, msgID, 4)

		name := "f" + strconv.FormatInt(msgID, 10) + ".bin"
		item.Filename = &name
		item.Date = strPtrOf("2026-01-02T03:04:05Z")
		sender := msgID + 100
		item.SenderID = &sender

		items = append(items, item)
	}

	return items
}

// readChatManifest loads and decodes a manifest.json.
func readChatManifest(t *testing.T, path string) download.ChatManifest {
	t.Helper()

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var manifest download.ChatManifest
	require.NoError(t, json.Unmarshal(raw, &manifest))

	return manifest
}

// TestChatManifestAggregatesCompletions pins [output] metadata = "chat":
// three completions in one chat produce ONE manifest.json in the chat's
// directory, numbered seq 1..N in completion order, paths relative to the
// manifest, sha256 present when hashing ran.
func TestChatManifestAggregatesCompletions(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	cfg := managerConfig(func(c *download.Config) {
		c.Output.Metadata = config.MetadataChat
		c.Output.Sha256 = true
	})

	mgr := newChatManifestManager(t, root, cfg)
	enqueue(t, mgr, manifestItems(5, 10, 11, 12)...)

	res, err := mgr.Run(context.Background(), "run-manifest")
	require.NoError(t, err)
	assert.EqualValues(t, 3, res.Downloaded)

	manifestPath := filepath.Join(root, "chat_5", "manifest.json")
	require.FileExists(t, manifestPath, "one manifest per chat directory")

	manifest := readChatManifest(t, manifestPath)

	assert.Equal(t, download.ChatManifestHeader{ID: 5, Title: "Chat 5"}, manifest.Chat)
	require.Len(t, manifest.Files, 3)

	msgIDs := map[int64]bool{}
	seqs := map[int]bool{}

	for _, entry := range manifest.Files {
		msgIDs[entry.MsgID] = true
		seqs[entry.Seq] = true

		assert.Equal(t, fmt.Sprintf("file_%d.bin", entry.MsgID), entry.Path,
			"paths are relative to the manifest")
		assert.Equal(t, "document", entry.MediaClass)
		assert.NotEmpty(t, entry.Sha256, "sha256 included when computed")
		assert.Equal(t, int64(4), derefInt64(entry.Size))
	}

	assert.True(t, msgIDs[10] && msgIDs[11] && msgIDs[12], "every completion landed: %v", msgIDs)
	assert.True(t, seqs[1] && seqs[2] && seqs[3], "seq numbering is 1..N in completion order: %v", seqs)
}

// TestChatManifestGrowsAcrossRuns pins the incremental update: a later run
// against the same output root appends its completion as seq N+1 to the
// existing manifest instead of replacing it.
func TestChatManifestGrowsAcrossRuns(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	cfg := managerConfig(func(c *download.Config) { c.Output.Metadata = config.MetadataChat })

	first := newChatManifestManager(t, root, cfg)
	enqueue(t, first, manifestItems(5, 10, 11, 12)...)

	_, err := first.Run(context.Background(), "run-grow-1")
	require.NoError(t, err)

	second := newChatManifestManager(t, root, cfg)
	enqueue(t, second, manifestItems(5, 13)...)

	_, err = second.Run(context.Background(), "run-grow-2")
	require.NoError(t, err)

	manifest := readChatManifest(t, filepath.Join(root, "chat_5", "manifest.json"))
	require.Len(t, manifest.Files, 4)

	last := manifest.Files[3]
	assert.Equal(t, 4, last.Seq, "the new completion appends as seq N+1")
	assert.Equal(t, int64(13), last.MsgID)
}

// TestChatManifestAtRootWithoutChatDirectory pins the no-{chat} layout: a
// flat rendered path keeps the manifest at the downloads root as
// manifest-<chatID>.json with the file path relative to it.
func TestChatManifestAtRootWithoutChatDirectory(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	root := t.TempDir()
	cfg := managerConfig(func(c *download.Config) {
		c.Output.Metadata = config.MetadataChat
		c.Root = root
	})

	mgr := download.NewManager(st, newPacer(time.Minute), cfg,
		fixedResolver{root: root, name: "flat.bin", meta: &download.SidecarMeta{ChatID: 8, ChatTitle: "Flat"}},
		download.NoopReporter{})
	mgr.Fetch = byteSource("data")

	enqueue(t, mgr, manifestItems(8, 40)...)

	_, err := mgr.Run(context.Background(), "run-flat")
	require.NoError(t, err)

	manifest := readChatManifest(t, filepath.Join(root, "manifest-8.json"))
	require.Len(t, manifest.Files, 1)
	assert.Equal(t, "flat.bin", manifest.Files[0].Path)
	assert.Equal(t, download.ChatManifestHeader{ID: 8, Title: "Flat"}, manifest.Chat)
}

// TestMetadataOffWritesNothing pins [output] metadata = "off": neither a
// chat manifest nor a legacy sidecar appears.
func TestMetadataOffWritesNothing(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	cfg := managerConfig(func(c *download.Config) { c.Output.Metadata = config.MetadataOff })

	mgr := newChatManifestManager(t, root, cfg)
	enqueue(t, mgr, manifestItems(5, 10)...)

	res, err := mgr.Run(context.Background(), "run-off")
	require.NoError(t, err)
	assert.EqualValues(t, 1, res.Downloaded)

	assert.NoFileExists(t, filepath.Join(root, "chat_5", "manifest.json"))
	assert.NoFileExists(t, filepath.Join(root, "chat_5", "file_10.bin.json"))
}

// TestMetadataFileKeepsLegacySidecar pins [output] metadata = "file": the
// legacy per-file sidecar fires and no chat manifest is written.
func TestMetadataFileKeepsLegacySidecar(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	cfg := managerConfig(func(c *download.Config) { c.Output.Metadata = config.MetadataFile })

	mgr := newChatManifestManager(t, root, cfg)
	enqueue(t, mgr, manifestItems(5, 10)...)

	_, err := mgr.Run(context.Background(), "run-file")
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(root, "chat_5", "file_10.bin.json"))
	assert.NoFileExists(t, filepath.Join(root, "chat_5", "manifest.json"))
}

func derefInt64(value *int64) int64 {
	if value == nil {
		return 0
	}

	return *value
}

// strPtrOf boxes a string, mirroring store rows' optional fields.
func strPtrOf(text string) *string { return &text }
