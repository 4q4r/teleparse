package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/config"
	"github.com/4q4r/teleparse/internal/download"
	"github.com/4q4r/teleparse/internal/filters"
	"github.com/4q4r/teleparse/internal/scan"
	"github.com/4q4r/teleparse/internal/store"

	tg "github.com/gotd/td/tg"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// exportFixtureJSON mirrors the Telegram Desktop result.json shape: chat
// identity at the top level, flat photo/file paths per message.
const exportFixtureJSON = `{
  "type": "public_channel",
  "id": 30,
  "name": "News",
  "messages": [
    {
      "type": "message",
      "id": 100,
      "date": "2026-09-06T10:00:00",
      "date_unixtime": "1788688800",
      "photo": "photos/photo_100@06-09-2026_10-00-00.jpg",
      "width": 1280,
      "height": 720,
      "grouped_id": 77
    },
    {
      "type": "message",
      "id": 101,
      "date": "2026-09-06T10:01:00",
      "date_unixtime": "1788688860",
      "file": "documents/document_1.mp4",
      "media_type": "document"
    },
    {
      "type": "message",
      "id": 102,
      "date": "2026-09-06T10:02:00",
      "date_unixtime": "1788688920",
      "text": "no media here"
    },
    {
      "type": "service",
      "id": 103,
      "date": "2026-09-06T10:03:00",
      "date_unixtime": "1788688980",
      "action": "phone_call"
    }
  ]
}`

// writeExportFixture writes the fixture result.json plus one real media file
// under the export root, returning the result.json path.
func writeExportFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "photos"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "result.json"), []byte(exportFixtureJSON), 0o600))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "photos", "photo_100@06-09-2026_10-00-00.jpg"),
		[]byte("photo-bytes"), 0o600))

	return filepath.Join(root, "result.json")
}

// newAdoptFixture opens a store/resolver/collector plus command wired like
// executeRun, with the export chat already registered.
func newAdoptFixture(t *testing.T) (*cobra.Command, *store.Store, *runResolver, *walkCollector, *App) {
	t.Helper()

	state := openTestStore(t)
	require.NoError(t, state.UpsertChat(t.Context(), store.Chat{ChatID: 30, Type: "channel", Title: ptr("News")}))

	app := &App{cfg: config.Default(), paths: &config.Paths{Downloads: t.TempDir()}}
	resolver, collector := newRunResolver(app, &fakeRefetchAPI{}, state, false)

	cmd, _ := newOutCmd()

	return cmd, state, resolver, collector, app
}

func adoptTestTarget() scan.Target {
	return scan.Target{
		InputPeer: &tg.InputPeerChannel{ChannelID: 30, AccessHash: 300},
		Chat:      filters.Chat{ID: 30, Type: "channel", Title: "News"},
	}
}

func TestParseDesktopExportRows(t *testing.T) {
	t.Parallel()

	exportPath := writeExportFixture(t)

	job, err := parseDesktopExport(exportPath, "", "")
	require.NoError(t, err)

	assert.Equal(t, "30", job.chatSpec, "a public channel export resolves to its bare id")
	assert.Equal(t, "News", job.label)
	assert.Equal(t, filepath.Dir(exportPath), job.mediaDir)
	require.Len(t, job.rows, 2, "only file-bearing messages become rows")

	photo := job.rows[0]
	assert.Equal(t, int64(100), photo.item.MessageID)
	assert.Equal(t, "photo", photo.item.MediaClass)
	require.NotNil(t, photo.item.Filename)
	assert.Equal(t, "photo_100@06-09-2026_10-00-00.jpg", *photo.item.Filename)
	require.NotNil(t, photo.item.GroupedID)
	assert.Equal(t, int64(77), *photo.item.GroupedID)
	require.NotNil(t, photo.item.Date)
	assert.Equal(t, time.Unix(1788688800, 0).UTC().Format(time.RFC3339), *photo.item.Date)
	assert.Equal(t, filepath.Join("photos", "photo_100@06-09-2026_10-00-00.jpg"), photo.src)
	assert.Negative(t, photo.item.MediaID, "export rows carry deterministic pseudo ids")

	doc := job.rows[1]
	assert.Equal(t, int64(101), doc.item.MessageID)
	assert.Equal(t, "video", doc.item.MediaClass, "extension inference maps .mp4 to video")
	require.NotNil(t, doc.item.Filename)
	assert.Equal(t, "document_1.mp4", *doc.item.Filename)
}

func TestParseDesktopExportChatSpec(t *testing.T) {
	t.Parallel()

	exportPath := writeExportFixture(t)

	withOverride, err := parseDesktopExport(exportPath, "", "@durov")
	require.NoError(t, err)
	assert.Equal(t, "@durov", withOverride.chatSpec, "--chat overrides the export identity")

	withMediaDir, err := parseDesktopExport(exportPath, "/media/elsewhere", "")
	require.NoError(t, err)
	assert.Equal(t, "/media/elsewhere", withMediaDir.mediaDir)

	broken := filepath.Join(t.TempDir(), "result.json")
	require.NoError(t, os.WriteFile(broken, []byte("{not json"), 0o600))

	_, err = parseDesktopExport(broken, "", "")
	assert.Error(t, err, "malformed JSON fails loudly")
}

func TestAdoptExportRowsAdoptsPresentFiles(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	cmd, state, resolver, collector, app := newAdoptFixture(t)

	exportPath := writeExportFixture(t)

	job, err := parseDesktopExport(exportPath, "", "")
	require.NoError(t, err)

	plan, err := filters.Compile(&filters.Options{})
	require.NoError(t, err)

	require.NoError(t, adoptExportRows(ctx, cmd, app, state, resolver,
		collector, job, plan, runMode{}, adoptTestTarget()))

	// The present photo was adopted: hard-linked into the blob store and the
	// final path, marked done with an immediate sha256.
	adopted, exists, err := state.MediaByFile(t.Context(), "photo", job.rows[0].item.MediaID)
	require.NoError(t, err)
	require.True(t, exists)
	assert.Equal(t, store.StatusDone, adopted.Status)
	require.NotNil(t, adopted.Path)

	blob := download.BlobPath(app.paths.Downloads, "photo", job.rows[0].item.MediaID, ".jpg")

	sum := sha256.Sum256([]byte("photo-bytes"))

	require.NotNil(t, adopted.Sha256)
	assert.Equal(t, hex.EncodeToString(sum[:]), *adopted.Sha256)

	src := filepath.Join(job.mediaDir, filepath.FromSlash(job.rows[0].src))
	assertSameFile(t, src, blob)
	assertSameFile(t, src, *adopted.Path)

	// The absent document went to the queue untouched.
	require.Len(t, collector.items, 1)
	assert.Equal(t, int64(101), collector.items[0].MessageID)
	assert.Equal(t, store.StatusDiscovered, collector.items[0].Status)
}

func TestAdoptExportRowsSizeMismatchQueued(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	cmd, state, resolver, collector, app := newAdoptFixture(t)

	exportPath := writeExportFixture(t)

	job, err := parseDesktopExport(exportPath, "", "")
	require.NoError(t, err)

	// A recorded size that disagrees with the on-disk bytes must force the
	// queue, never a blind link.
	wrongSize := int64(999999)
	job.rows[0].item.Size = &wrongSize

	plan, err := filters.Compile(&filters.Options{})
	require.NoError(t, err)

	require.NoError(t, adoptExportRows(ctx, cmd, app, state, resolver,
		collector, job, plan, runMode{}, adoptTestTarget()))

	require.Len(t, collector.items, 2, "the mismatched photo is queued like an absent file")
}

func TestAdoptExportRowsDryRunWritesNothing(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	cmd, state, resolver, collector, app := newAdoptFixture(t)

	exportPath := writeExportFixture(t)

	job, err := parseDesktopExport(exportPath, "", "")
	require.NoError(t, err)

	plan, err := filters.Compile(&filters.Options{})
	require.NoError(t, err)

	require.NoError(t, adoptExportRows(ctx, cmd, app, state, resolver,
		collector, job, plan, runMode{dryRun: true}, adoptTestTarget()))

	require.Len(t, collector.items, 2, "dry-run queues every row and links nothing")

	_, exists, err := state.MediaByFile(t.Context(), "photo", job.rows[0].item.MediaID)
	require.NoError(t, err)
	assert.False(t, exists, "dry-run writes no manifest rows")
}

func TestAdoptExportRowsAppliesFilters(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	cmd, state, resolver, collector, app := newAdoptFixture(t)

	exportPath := writeExportFixture(t)

	job, err := parseDesktopExport(exportPath, "", "")
	require.NoError(t, err)

	opts := filters.Options{Media: []string{"video"}}

	plan, err := filters.Compile(&opts)
	require.NoError(t, err)

	require.NoError(t, adoptExportRows(ctx, cmd, app, state, resolver,
		collector, job, plan, runMode{}, adoptTestTarget()))

	require.Len(t, collector.items, 1)
	assert.Equal(t, int64(101), collector.items[0].MessageID, "the photo row is filtered out")
}

// assertSameFile fails unless both paths resolve to one inode.
func assertSameFile(t *testing.T, first, second string) {
	t.Helper()

	firstInfo, err := os.Stat(first)
	require.NoError(t, err)

	secondInfo, err := os.Stat(second)
	require.NoError(t, err)

	assert.True(t, os.SameFile(firstInfo, secondInfo), "%s and %s must share one inode", first, second)
}
