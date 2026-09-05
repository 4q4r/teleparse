package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/config"
	"github.com/4q4r/teleparse/internal/filters"
	"github.com/4q4r/teleparse/internal/scan"
	"github.com/4q4r/teleparse/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPreviewRunDuplicateFilesNeverAbort reproduces the count-only crash:
// the same Telegram file forwarded into two chats must not abort a preview
// run, and preview modes must never touch the downloads root.
func TestPreviewRunDuplicateFilesNeverAbort(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = state.Close() })

	downloads := filepath.Join(t.TempDir(), "downloads")
	app := &App{
		style:    NewStyler(false),
		errStyle: NewStyler(false),
		paths:    &config.Paths{Downloads: downloads},
	}

	cmd, out := newOutCmd()

	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)

	collector := &walkCollector{items: []store.MediaItem{
		{ChatID: 1, MessageID: 10, MediaClass: "photo", MediaID: 555},
		{ChatID: 2, MessageID: 20, MediaClass: "photo", MediaID: 555},
	}}
	targets := []scan.Target{
		{Chat: filters.Chat{ID: 1, Title: "News"}},
		{Chat: filters.Chat{ID: 2, Title: ""}},
	}

	require.NoError(t, state.CreateRun(ctx, &store.Run{RunID: "r-preview", Account: "main", FilterJSON: "{}"}))

	require.NoError(t, previewRun(ctx, cmd, app, state, "r-preview", collector, targets,
		runMode{countOnly: true}, time.Second))

	assert.Contains(t, out.String(), "TOTAL", "counts table renders")
	assert.Contains(t, errBuf.String(), "duplicates: 1", "the summary surfaces the duplicate")
	assert.NoDirExists(t, downloads, "count-only runs never create the downloads root")

	run, found, err := state.GetRun(ctx, "r-preview")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, store.StatusDone, run.Status, "duplicate files must not fail the run")

	row, found, err := state.MediaByFile(ctx, "photo", 555)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, int64(1), row.ChatID, "the first sighting owns the row")
}
