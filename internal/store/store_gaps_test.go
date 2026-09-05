package store_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordAttemptKeepsRowUnclaimable(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	st := openStore(t)

	item := media(7, 1, "document", 101)
	item.Status = store.StatusQueued
	require.NoError(t, st.UpsertMedia(ctx, item))

	claimed, err := st.ClaimPending(ctx, 7, 10, 3)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	require.NoError(t, st.RecordAttempt(ctx, 7, 1, 0, "transient boom", 1))

	got, found, err := st.MediaByFile(ctx, "document", 101)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, store.StatusDownloading, got.Status, "an in-flight retry ladder must keep its claim")
	require.NotNil(t, got.LastError)
	assert.Equal(t, "transient boom", *got.LastError)
	assert.Equal(t, 1, got.Attempts)

	reclaimed, err := st.ClaimPending(ctx, 7, 10, 3)
	require.NoError(t, err)
	assert.Empty(t, reclaimed, "a mid-ladder row must never be handed to a second worker")

	require.NoError(t, st.RecordAttempt(ctx, 7, 1, 0, "", 2))

	got, found, err = st.MediaByFile(ctx, "document", 101)
	require.NoError(t, err)
	require.True(t, found)
	assert.Nil(t, got.LastError, "empty error text keeps the column NULL")
	assert.Equal(t, 2, got.Attempts)
}

func TestClaimPendingExcludesQueuedRowsAtMaxAttempts(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	st := openStore(t)

	exhausted := media(3, 1, "photo", 11)
	exhausted.Status = store.StatusQueued
	exhausted.Attempts = 5
	require.NoError(t, st.UpsertMedia(ctx, exhausted))

	claimed, err := st.ClaimPending(ctx, 3, 10, 5)
	require.NoError(t, err)
	assert.Empty(t, claimed, "queued rows at max attempts are as exhausted as failed ones")
}

func TestResetDownloadingOnlyTouchesDownloadingRows(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	st := openStore(t)

	seed := func(chat int64, msg int64, fileID int64, status string) *store.MediaItem {
		item := media(chat, msg, "photo", fileID)
		item.Status = status

		return item
	}

	for _, item := range []*store.MediaItem{
		seed(1, 1, 101, store.StatusDownloading),
		seed(1, 2, 102, store.StatusQueued),
		seed(1, 3, 103, store.StatusFailed),
		seed(1, 4, 104, store.StatusDone),
	} {
		require.NoError(t, st.UpsertMedia(ctx, item))
	}

	reset, err := st.ResetDownloading(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), reset, "only the dangling download is requeued")

	for fileID, want := range map[int64]string{
		101: store.StatusQueued,
		102: store.StatusQueued,
		103: store.StatusFailed,
		104: store.StatusDone,
	} {
		got, found, err := st.MediaByFile(ctx, "photo", fileID)
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, want, got.Status)
	}
}

func TestCleanupFinishedRunsBoundary(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	st := openStore(t)

	require.NoError(t, st.CreateRun(ctx, &store.Run{RunID: "running", Account: "a", FilterJSON: "{}"}))
	require.NoError(t, st.CreateRun(ctx, &store.Run{RunID: "parked", Account: "a", FilterJSON: "{}"}))
	require.NoError(t, st.SetResumeAt(ctx, "parked", time.Now().Add(time.Minute)))
	require.NoError(t, st.FinishRun(ctx, "parked", store.StatusParked, ""))

	cutoff := time.Now().Add(-time.Hour)

	finishedAtCutoff := &store.Run{RunID: "edge", Account: "a", FilterJSON: "{}"}
	require.NoError(t, st.CreateRun(ctx, finishedAtCutoff))
	require.NoError(t, st.FinishRun(ctx, "edge", store.StatusDone, ""))

	removed, err := st.CleanupFinishedRuns(ctx, cutoff)
	require.NoError(t, err)
	assert.Zero(t, removed, "runs finished after the cutoff survive")

	runs, err := st.ListRuns(ctx, 0)
	require.NoError(t, err)
	assert.Len(t, runs, 3, "running, parked and freshly finished runs all survive")

	strict, err := st.CleanupFinishedRuns(ctx, time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(1), strict, "only the finished run is old enough now")

	survived, err := st.ListRuns(ctx, 0)
	require.NoError(t, err)
	require.Len(t, survived, 2)
	assert.Equal(t, map[string]bool{"running": true, "parked": true}, map[string]bool{
		survived[0].RunID: true, survived[1].RunID: true,
	}, "parked and running runs never age out")
}

func TestSetResumeAtZeroTimeClearsDeadline(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	st := openStore(t)

	require.NoError(t, st.CreateRun(ctx, &store.Run{RunID: "r", Account: "a", FilterJSON: "{}"}))
	require.NoError(t, st.SetResumeAt(ctx, "r", time.Now().Add(time.Hour)))
	require.NoError(t, st.SetResumeAt(ctx, "r", time.Time{}))

	run, found, err := st.GetRun(ctx, "r")
	require.NoError(t, err)
	require.True(t, found)
	assert.Nil(t, run.ResumeAt, "a zero resume deadline clears the column")
}

func TestAdvanceWatermarkZeroTimeStampsNull(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	st := openStore(t)

	chat := store.Chat{ChatID: 5, Type: "channel", Title: ptr("News")}
	require.NoError(t, st.UpsertChat(ctx, chat))
	require.NoError(t, st.AdvanceWatermark(ctx, 5, 900, time.Time{}))

	wm, err := st.Watermark(ctx, 5)
	require.NoError(t, err)
	assert.Equal(t, int64(900), wm, "the watermark advances without a sync stamp")
}

func TestClosedStoreFailsLoudly(t *testing.T) {
	t.Parallel()

	st := openStore(t)
	require.NoError(t, st.Close())

	ctx := t.Context()

	for name, call := range map[string]func() error{
		"upsert chat":     func() error { return st.UpsertChat(ctx, store.Chat{ChatID: 1, Type: "channel"}) },
		"watermark":       func() error { _, err := st.Watermark(ctx, 1); return err },
		"advance wm":      func() error { return st.AdvanceWatermark(ctx, 1, 2, time.Now()) },
		"upsert media":    func() error { return st.UpsertMedia(ctx, media(1, 1, "photo", 1)) },
		"media by file":   func() error { _, _, err := st.MediaByFile(ctx, "photo", 1); return err },
		"claim pending":   func() error { _, err := st.ClaimPending(ctx, 1, 1, 1); return err },
		"mark done":       func() error { return st.MarkDone(ctx, 1, 1, 0, "p", nil) },
		"mark failed":     func() error { return st.MarkFailed(ctx, 1, 1, 0, "e", 1) },
		"record attempt":  func() error { return st.RecordAttempt(ctx, 1, 1, 0, "e", 1) },
		"counts":          func() error { _, err := st.Counts(ctx); return err },
		"stats per chat":  func() error { _, err := st.StatsPerChat(ctx); return err },
		"reset downloads": func() error { _, err := st.ResetDownloading(ctx); return err },
		"list media rows": func() error { _, err := st.ListMediaRows(ctx, 0); return err },
		"create run":      func() error { return st.CreateRun(ctx, &store.Run{RunID: "x", FilterJSON: "{}"}) },
		"finish run":      func() error { return st.FinishRun(ctx, "x", store.StatusDone, "") },
		"set resume at":   func() error { return st.SetResumeAt(ctx, "x", time.Now()) },
		"list runs":       func() error { _, err := st.ListRuns(ctx, 0); return err },
		"get run":         func() error { _, _, err := st.GetRun(ctx, "x"); return err },
		"cleanup runs":    func() error { _, err := st.CleanupFinishedRuns(ctx, time.Now()); return err },
	} {
		assert.Error(t, call(), "%s on a closed store must fail loudly", name)
	}
}

func TestOpenRejectsCorruptDatabase(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.db")
	require.NoError(t, os.WriteFile(path, []byte("definitely not a sqlite database"), 0o600))

	_, err := store.Open(path)
	require.Error(t, err, "a corrupt file must fail migrations, never be silently recreated")
}
