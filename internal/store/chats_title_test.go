package store_test

import (
	"testing"

	"github.com/4q4r/teleparse/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestChatTitle pins the read the manifest-driven path resolver depends on:
// stored titles surface verbatim (UTF-8 included), while NULL, blank and
// unknown chats report not-found so callers fall back to chat_<id>.
func TestChatTitle(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	st := openStore(t)

	require.NoError(t, st.UpsertChat(ctx, store.Chat{ChatID: 1, Type: "channel", Title: ptr("Проект Альфа")}))
	require.NoError(t, st.UpsertChat(ctx, store.Chat{ChatID: 2, Type: "private", Title: ptr("")}))
	require.NoError(t, st.UpsertChat(ctx, store.Chat{ChatID: 3, Type: "group"}))

	title, ok, err := st.ChatTitle(ctx, 1)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "Проект Альфа", title)

	for _, chatID := range []int64{2, 3, 4} {
		title, ok, err := st.ChatTitle(ctx, chatID)
		require.NoError(t, err)

		assert.False(t, ok, "chat %d has no usable title", chatID)
		assert.Empty(t, title)
	}
}
