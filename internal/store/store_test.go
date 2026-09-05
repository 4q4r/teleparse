package store_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()

	path := filepath.Join(t.TempDir(), "state.db")

	st, err := store.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	return st
}

func ptr[T any](v T) *T { return &v }

func TestOpenIsIdempotent(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.db")

	first, err := store.Open(path)
	require.NoError(t, err)
	require.NoError(t, first.Close())

	second, err := store.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })

	wm, err := second.Watermark(t.Context(), 42)
	require.NoError(t, err)
	assert.Zero(t, wm, "reopened database must serve queries")
}

func TestChatWatermarkLifecycle(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	st := openStore(t)
	chat := store.Chat{ChatID: -1001234, Type: "supergroup", Title: ptr("News"), Username: ptr("news")}

	require.NoError(t, st.UpsertChat(ctx, chat))

	wm, err := st.Watermark(ctx, chat.ChatID)
	require.NoError(t, err)
	assert.Zero(t, wm, "fresh chat starts at watermark zero")

	missing, err := st.Watermark(ctx, -999)
	require.NoError(t, err)
	assert.Zero(t, missing, "unknown chat has watermark zero")

	require.NoError(t, st.AdvanceWatermark(ctx, chat.ChatID, 500, time.Now()))

	wm, err = st.Watermark(ctx, chat.ChatID)
	require.NoError(t, err)
	assert.Equal(t, int64(500), wm)

	chat.Title = ptr("News 2")
	require.NoError(t, st.UpsertChat(ctx, chat))

	wm, err = st.Watermark(ctx, chat.ChatID)
	require.NoError(t, err)
	assert.Equal(t, int64(500), wm, "chat metadata refresh must keep the watermark")

	require.NoError(t, st.AdvanceWatermark(ctx, chat.ChatID, 300, time.Now()))

	wm, err = st.Watermark(ctx, chat.ChatID)
	require.NoError(t, err)
	assert.Equal(t, int64(500), wm, "lower ids must never regress the watermark")
}

func TestRunLifecycle(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	st := openStore(t)

	first := &store.Run{RunID: "r1", Account: "main", Profile: ptr("videos"), FilterJSON: `{"media":["photo"]}`}
	require.NoError(t, st.CreateRun(ctx, first))
	assert.Equal(t, store.StatusRunning, first.Status)
	assert.NotEmpty(t, first.StartedAt)

	got, found, err := st.GetRun(ctx, "r1")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "main", got.Account)
	assert.Equal(t, "videos", *got.Profile)
	assert.JSONEq(t, `{"media":["photo"]}`, got.FilterJSON)
	assert.Equal(t, store.StatusRunning, got.Status)
	assert.Nil(t, got.FinishedAt)
	assert.Nil(t, got.ResumeAt)
	assert.Nil(t, got.Error)

	resume := time.Now()
	require.NoError(t, st.SetResumeAt(ctx, "r1", resume))

	got, found, err = st.GetRun(ctx, "r1")
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, got.ResumeAt)

	parsed, err := time.Parse(time.RFC3339Nano, *got.ResumeAt)
	require.NoError(t, err)
	assert.WithinDuration(t, resume, parsed, time.Second)

	require.NoError(t, st.FinishRun(ctx, "r1", store.StatusDone, ""))

	got, found, err = st.GetRun(ctx, "r1")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, store.StatusDone, got.Status)
	assert.NotNil(t, got.FinishedAt)
	assert.Nil(t, got.Error)

	require.NoError(t, st.CreateRun(ctx, &store.Run{RunID: "r2", Account: "main", FilterJSON: "{}"}))
	require.NoError(t, st.CreateRun(ctx, &store.Run{RunID: "r3", Account: "spare", FilterJSON: "{}"}))
	require.NoError(t, st.FinishRun(ctx, "r3", store.StatusFailed, "dial tcp: refused"))

	runs, err := st.ListRuns(ctx, 0)
	require.NoError(t, err)
	require.Len(t, runs, 3)
	assert.Equal(t, []string{"r3", "r2", "r1"}, []string{runs[0].RunID, runs[1].RunID, runs[2].RunID})

	limited, err := st.ListRuns(ctx, 2)
	require.NoError(t, err)
	require.Len(t, limited, 2)
	assert.Equal(t, "r3", limited[0].RunID)

	failed, found, err := st.GetRun(ctx, "r3")
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, failed.Error)
	assert.Equal(t, "dial tcp: refused", *failed.Error)

	absent, found, err := st.GetRun(ctx, "nope")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Nil(t, absent)
}

func TestCleanupFinishedRuns(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	st := openStore(t)

	require.NoError(t, st.CreateRun(ctx, &store.Run{RunID: "kept-running", Account: "a", FilterJSON: "{}"}))
	require.NoError(t, st.CreateRun(ctx, &store.Run{RunID: "done", Account: "a", FilterJSON: "{}"}))
	require.NoError(t, st.FinishRun(ctx, "done", store.StatusDone, ""))
	require.NoError(t, st.CreateRun(ctx, &store.Run{RunID: "failed", Account: "a", FilterJSON: "{}"}))
	require.NoError(t, st.FinishRun(ctx, "failed", store.StatusFailed, "x"))

	removed, err := st.CleanupFinishedRuns(ctx, time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(2), removed)

	runs, err := st.ListRuns(ctx, 0)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, "kept-running", runs[0].RunID)

	removed, err = st.CleanupFinishedRuns(ctx, time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Zero(t, removed)
}

func media(chat int64, msg int64, class string, fileID int64) *store.MediaItem {
	return &store.MediaItem{
		ChatID:     chat,
		MessageID:  msg,
		MediaIndex: 0,
		MediaClass: class,
		MediaID:    fileID,
		Status:     store.StatusDiscovered,
	}
}

func TestUpsertMediaIdempotencyKeepsMetadata(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	st := openStore(t)

	item := media(1, 10, "photo", 555)
	item.Mime = ptr("image/jpeg")
	item.Size = ptr(int64(2048))
	item.Filename = ptr("a.jpg")

	_, err := st.UpsertMedia(ctx, item)
	require.NoError(t, err)

	got, found, err := st.MediaByFile(ctx, "photo", 555)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "image/jpeg", *got.Mime)
	assert.Equal(t, int64(2048), *got.Size)
	assert.Equal(t, "a.jpg", *got.Filename)
	assert.Equal(t, store.StatusDiscovered, got.Status)

	refresh := media(1, 10, "photo", 555)
	refresh.Mime = ptr("image/png")
	refresh.Size = ptr(int64(1))
	refresh.Filename = ptr("renamed.jpg")
	refresh.Status = store.StatusQueued
	refresh.Attempts = 2
	refresh.BytesDone = 512

	_, err = st.UpsertMedia(ctx, refresh)
	require.NoError(t, err)

	got, found, err = st.MediaByFile(ctx, "photo", 555)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "image/jpeg", *got.Mime, "discovery metadata must stay stable")
	assert.Equal(t, int64(2048), *got.Size, "discovery metadata must stay stable")
	assert.Equal(t, "a.jpg", *got.Filename, "discovery metadata must stay stable")
	assert.Equal(t, store.StatusQueued, got.Status, "mutable progress must update")
	assert.Equal(t, 2, got.Attempts, "mutable progress must update")
	assert.Equal(t, int64(512), got.BytesDone, "mutable progress must update")
}

func TestUpsertMediaDuplicateFileIsBenignSkip(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	st := openStore(t)

	first := media(1, 10, "photo", 555)
	first.Status = store.StatusQueued

	stored, err := st.UpsertMedia(ctx, first)
	require.NoError(t, err)
	assert.True(t, stored, "first sighting writes the row")

	// The same unique file forwarded into a second chat: the upsert must
	// never abort the run; the existing row stays untouched (first wins).
	second := media(2, 99, "photo", 555)
	second.Status = store.StatusQueued

	stored, err = st.UpsertMedia(ctx, second)
	require.NoError(t, err, "a duplicate file must be a benign skip, not an error")
	assert.False(t, stored, "the duplicate sighting reports nothing stored")

	got, found, err := st.MediaByFile(ctx, "photo", 555)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, int64(1), got.ChatID, "original row wins")
	assert.Equal(t, int64(10), got.MessageID, "original row wins")

	// Re-sighting the SAME message stays a progress update, not a duplicate.
	refresh := media(1, 10, "photo", 555)
	refresh.Status = store.StatusDone

	stored, err = st.UpsertMedia(ctx, refresh)
	require.NoError(t, err)
	assert.True(t, stored, "same-message re-sighting updates the row")

	got, found, err = st.MediaByFile(ctx, "photo", 555)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, store.StatusDone, got.Status)

	// Different media classes may reuse the same numeric id.
	other := media(1, 11, "document", 555)
	stored, err = st.UpsertMedia(ctx, other)
	require.NoError(t, err)
	assert.True(t, stored, "class-scoped uniqueness keeps distinct rows")

	absent, found, err := st.MediaByFile(ctx, "video", 555)
	require.NoError(t, err)
	assert.False(t, found)
	assert.Nil(t, absent)
}

func TestClaimResetMarkFlows(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	st := openStore(t)
	const chat = int64(7)

	m1 := media(chat, 1, "photo", 101)
	m1.Status = store.StatusQueued
	m2 := media(chat, 2, "photo", 102)
	m2.Status = store.StatusQueued
	m3 := media(chat, 3, "document", 103)
	m3.Status = store.StatusDone
	m4 := media(chat, 4, "document", 104)
	m4.Status = store.StatusFailed
	m4.Attempts = 2

	for _, item := range []*store.MediaItem{m1, m2, m3, m4} {
		_, err := st.UpsertMedia(ctx, item)
		require.NoError(t, err)
	}

	other := media(9, 1, "photo", 901)
	other.Status = store.StatusQueued
	_, err := st.UpsertMedia(ctx, other)
	require.NoError(t, err)

	claimed, err := st.ClaimPending(ctx, chat, 2, 3)
	require.NoError(t, err)
	require.Len(t, claimed, 2, "limit bounds the claim")
	assert.Equal(t, int64(1), claimed[0].MessageID)
	assert.Equal(t, int64(2), claimed[1].MessageID)

	for _, item := range claimed {
		assert.Equal(t, store.StatusDownloading, item.Status)
	}

	claimed, err = st.ClaimPending(ctx, chat, 10, 3)
	require.NoError(t, err)
	require.Len(t, claimed, 1, "failed rows below max attempts are reclaimed")
	assert.Equal(t, int64(4), claimed[0].MessageID)

	require.NoError(t, st.MarkDone(ctx, chat, 1, 0, "out/p101.jpg", ptr("deadbeef")))
	require.NoError(t, st.MarkDone(ctx, chat, 3, 0, "out/p103.jpg", nil))
	require.NoError(t, st.MarkFailed(ctx, chat, 2, 0, "boom", 2))

	got, found, err := st.MediaByFile(ctx, "photo", 102)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, store.StatusFailed, got.Status)
	require.NotNil(t, got.LastError)
	assert.Equal(t, "boom", *got.LastError)
	assert.Equal(t, 2, got.Attempts)

	got, found, err = st.MediaByFile(ctx, "photo", 101)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, store.StatusDone, got.Status)
	require.NotNil(t, got.Path)
	assert.Equal(t, "out/p101.jpg", *got.Path)
	require.NotNil(t, got.Sha256)
	assert.Equal(t, "deadbeef", *got.Sha256)

	got, found, err = st.MediaByFile(ctx, "document", 103)
	require.NoError(t, err)
	require.True(t, found)
	assert.Nil(t, got.Sha256, "empty hash column must stay NULL")

	claimed, err = st.ClaimPending(ctx, chat, 10, 3)
	require.NoError(t, err)
	require.Len(t, claimed, 1, "failed-at-2 is still claimable below max attempts 3")
	require.NoError(t, st.MarkFailed(ctx, chat, 2, 0, "still broken", 3))

	claimed, err = st.ClaimPending(ctx, chat, 10, 3)
	require.NoError(t, err)
	assert.Empty(t, claimed, "attempts at max must exhaust retries")

	counts, err := st.Counts(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{
		store.StatusDiscovered:  0,
		store.StatusQueued:      1,
		store.StatusDownloading: 1,
		store.StatusDone:        2,
		store.StatusFailed:      1,
		store.StatusSkipped:     0,
	}, counts)

	reset, err := st.ResetDownloading(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), reset, "crash recovery requeues the dangling download")

	got, found, err = st.MediaByFile(ctx, "document", 104)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, store.StatusQueued, got.Status)
}

func TestStatsPerChat(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	st := openStore(t)

	require.NoError(t, st.UpsertChat(ctx, store.Chat{ChatID: 1, Type: "channel", Title: ptr("Alpha")}))
	require.NoError(t, st.UpsertChat(ctx, store.Chat{ChatID: 2, Type: "private", Title: ptr("Beta")}))

	done := func(chat int64, msg int64, fileID int64, size int64) *store.MediaItem {
		item := media(chat, msg, "photo", fileID)
		item.Status = store.StatusDone
		item.Size = ptr(size)

		return item
	}

	for _, item := range []*store.MediaItem{
		done(1, 1, 11, 10),
		done(1, 2, 12, 20),
		done(2, 1, 21, 5),
		done(3, 1, 31, 7),
		media(1, 3, "photo", 13),
	} {
		_, err := st.UpsertMedia(ctx, item)
		require.NoError(t, err)
	}

	failed := media(1, 4, "photo", 14)
	failed.Status = store.StatusFailed
	failed.Size = ptr(int64(999))
	_, err := st.UpsertMedia(ctx, failed)
	require.NoError(t, err)

	stats, err := st.StatsPerChat(ctx)
	require.NoError(t, err)
	assert.Equal(t, []store.ChatStats{
		{ChatID: 1, Title: "Alpha", DoneCount: 2, DoneBytes: 30},
		{ChatID: 2, Title: "Beta", DoneCount: 1, DoneBytes: 5},
		{ChatID: 3, Title: "", DoneCount: 1, DoneBytes: 7},
	}, stats)
}

func TestListMediaRows(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	st := openStore(t)

	full := media(1, 10, "photo", 555)
	full.Mime = ptr("image/jpeg")
	full.Size = ptr(int64(1024))
	full.Date = ptr("2026-09-01T10:00:00Z")
	full.SenderID = ptr(int64(42))
	full.Status = store.StatusDone
	full.Path = ptr("out/a.jpg")

	bare := media(2, 11, "document", 666)
	bare.Filename = ptr("b.bin")
	bare.Status = store.StatusQueued

	_, err := st.UpsertMedia(ctx, full)
	require.NoError(t, err)
	_, err = st.UpsertMedia(ctx, bare)
	require.NoError(t, err)

	rows, err := st.ListMediaRows(ctx, 0)
	require.NoError(t, err)
	require.Len(t, rows, 2)

	first := rows[0]
	assert.Equal(t, int64(1), first.ChatID)
	assert.Equal(t, int64(10), first.MessageID)
	assert.Equal(t, "photo", first.MediaClass)
	assert.Equal(t, int64(555), first.MediaID)
	require.NotNil(t, first.Mime)
	assert.Equal(t, "image/jpeg", *first.Mime)
	require.NotNil(t, first.Size)
	assert.Equal(t, int64(1024), *first.Size)
	require.NotNil(t, first.SenderID)
	assert.Equal(t, int64(42), *first.SenderID)
	assert.Equal(t, store.StatusDone, first.Status)
	require.NotNil(t, first.Path)
	assert.Equal(t, "out/a.jpg", *first.Path)

	scoped, err := st.ListMediaRows(ctx, 2)
	require.NoError(t, err)
	require.Len(t, scoped, 1)
	assert.Equal(t, int64(2), scoped[0].ChatID)
	assert.Nil(t, scoped[0].Mime)
	assert.Nil(t, scoped[0].Size)
	require.NotNil(t, scoped[0].Filename)
	assert.Equal(t, "b.bin", *scoped[0].Filename)
}
