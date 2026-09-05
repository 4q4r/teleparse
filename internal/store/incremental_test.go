package store_test

import (
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResetWatermark(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	st := openStore(t)
	chat := store.Chat{ChatID: -1001234, Type: "channel", Title: ptr("News")}

	require.NoError(t, st.UpsertChat(ctx, chat))
	require.NoError(t, st.AdvanceWatermark(ctx, chat.ChatID, 900, time.Now()))

	wm, err := st.Watermark(ctx, chat.ChatID)
	require.NoError(t, err)
	require.Equal(t, int64(900), wm)

	require.NoError(t, st.ResetWatermark(ctx, chat.ChatID))

	wm, err = st.Watermark(ctx, chat.ChatID)
	require.NoError(t, err)
	assert.Zero(t, wm, "reset must clear the watermark for a full re-walk")

	require.NoError(t, st.ResetWatermark(ctx, -999), "resetting an unknown chat is a no-op")
}

func TestCachedMatchedExcludesFailedRows(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	st := openStore(t)

	rows := []store.MediaItem{
		{ChatID: 7, MessageID: 1, MediaIndex: 0, MediaClass: "photo", MediaID: 1, Status: store.StatusDiscovered},
		{ChatID: 7, MessageID: 2, MediaIndex: 0, MediaClass: "photo", MediaID: 2, Status: store.StatusDone},
		{ChatID: 7, MessageID: 3, MediaIndex: 0, MediaClass: "photo", MediaID: 3, Status: store.StatusQueued},
		{ChatID: 7, MessageID: 4, MediaIndex: 0, MediaClass: "photo", MediaID: 4, Status: store.StatusFailed},
		{ChatID: 8, MessageID: 1, MediaIndex: 0, MediaClass: "photo", MediaID: 5, Status: store.StatusDone},
	}

	for idx := range rows {
		require.NoError(t, func() error { _, err := st.UpsertMedia(ctx, &rows[idx]); return err }())
	}

	cached, err := st.CachedMatched(ctx, 7)
	require.NoError(t, err)
	assert.Equal(t, 3, cached, "discovered, done and queued count; failed does not")

	cached, err = st.CachedMatched(ctx, 8)
	require.NoError(t, err)
	assert.Equal(t, 1, cached)

	cached, err = st.CachedMatched(ctx, -999)
	require.NoError(t, err)
	assert.Zero(t, cached, "unknown chat has no cached matches")
}

func TestPendingMediaReturnsUndownloadedRows(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	st := openStore(t)

	rows := []store.MediaItem{
		{ChatID: 7, MessageID: 1, MediaIndex: 0, MediaClass: "photo", MediaID: 1, Status: store.StatusDiscovered},
		{ChatID: 7, MessageID: 2, MediaIndex: 0, MediaClass: "document", MediaID: 2, Status: store.StatusQueued},
		{ChatID: 7, MessageID: 3, MediaIndex: 0, MediaClass: "photo", MediaID: 3, Status: store.StatusFailed},
		{ChatID: 7, MessageID: 4, MediaIndex: 0, MediaClass: "photo", MediaID: 4, Status: store.StatusDone},
		{ChatID: 7, MessageID: 5, MediaIndex: 0, MediaClass: "photo", MediaID: 5, Status: store.StatusSkipped},
		{ChatID: 8, MessageID: 1, MediaIndex: 0, MediaClass: "photo", MediaID: 6, Status: store.StatusDiscovered},
	}

	for idx := range rows {
		require.NoError(t, func() error { _, err := st.UpsertMedia(ctx, &rows[idx]); return err }())
	}

	pending, err := st.PendingMedia(ctx, 7)
	require.NoError(t, err)
	require.Len(t, pending, 3, "discovered, queued and failed are re-offerable; done and skipped are not")

	gotIDs := make([]int64, 0, len(pending))
	for _, item := range pending {
		gotIDs = append(gotIDs, item.MessageID)
	}

	assert.Equal(t, []int64{1, 2, 3}, gotIDs, "pending rows come back in message order")

	pending, err = st.PendingMedia(ctx, -999)
	require.NoError(t, err)
	assert.Empty(t, pending)
}
