package cli

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/4q4r/teleparse/internal/filters"
	"github.com/4q4r/teleparse/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errForTest is the sentinel the fake title source fails with.
var errForTest = errors.New("title source unavailable")

// fakeTitles is the resolver's chat-title source in tests.
type fakeTitles struct {
	titles map[int64]string
	err    error
}

func (f fakeTitles) ChatTitle(_ context.Context, chatID int64) (string, bool, error) {
	if f.err != nil {
		return "", false, f.err
	}

	title, ok := f.titles[chatID]

	return title, ok && title != "", nil
}

// manifestResolver builds a store-only resolver with the default template.
func manifestResolver(titles fakeTitles) *runResolver {
	return &runResolver{
		root:      "root",
		template:  "{chat}/{date:%Y-%m}/{filename}",
		cache:     newMessageCache(8),
		titleMemo: map[int64]string{},
		titles:    titles,
	}
}

// TestStoreOnlyResolveRendersTemplateFromManifest pins the core fix: a
// cached manifest row with no walked message context still resolves through
// the real templated path, built from the media row's filename and date plus
// the chats table title.
func TestStoreOnlyResolveRendersTemplateFromManifest(t *testing.T) {
	t.Parallel()

	resolver := manifestResolver(fakeTitles{titles: map[int64]string{7: "Проект Альфа"}})

	item := store.MediaItem{
		ChatID:     7,
		MessageID:  100,
		MediaIndex: 0,
		MediaClass: "document",
		Filename:   ptr("отчёт за март.pdf"),
		Date:       ptr("2024-03-05T10:00:00Z"),
	}

	resolved, err := resolver.Resolve(item)
	require.NoError(t, err)

	assert.Equal(t, filepath.Join("root", "Проект Альфа", "2024-03", "отчёт за март.pdf"), resolved.Path)
	require.NotNil(t, resolved.Meta)
	assert.Equal(t, "Проект Альфа", resolved.Meta.ChatTitle)
	assert.Equal(t, "отчёт за март.pdf", resolved.Meta.Filename)
}

// TestStoreOnlyResolveBlankTitleFallsBackToChatID pins that a missing or
// blank stored title renders the chat directory as chat_<id>.
func TestStoreOnlyResolveBlankTitleFallsBackToChatID(t *testing.T) {
	t.Parallel()

	for name, titles := range map[string]fakeTitles{
		"no title row": {titles: map[int64]string{}},
		"blank title":  {titles: map[int64]string{9: ""}},
		"nil source":   {},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			resolver := manifestResolver(titles)

			item := store.MediaItem{
				ChatID: 9, MessageID: 5, Filename: ptr("a.pdf"),
				Date: ptr("2024-03-05T10:00:00Z"),
			}

			resolved, err := resolver.Resolve(item)
			require.NoError(t, err)

			assert.Equal(t, filepath.Join("root", "chat_9", "2024-03", "a.pdf"), resolved.Path)
		})
	}
}

// TestStoreOnlyResolveWithoutFilenameKeepsLegacyFallback pins the last
// resort: a manifest row carrying no filename at all keeps the legacy
// <chatID>/<msgID>_<index><ext> path.
func TestStoreOnlyResolveWithoutFilenameKeepsLegacyFallback(t *testing.T) {
	t.Parallel()

	resolver := manifestResolver(fakeTitles{titles: map[int64]string{7: "News"}})

	item := store.MediaItem{ChatID: 7, MessageID: 100, MediaIndex: 2, Filename: ptr("clip.mp4")}
	item.Filename = nil

	resolved, err := resolver.Resolve(item)
	require.NoError(t, err)

	assert.Equal(t, filepath.Join("root", "7", "100_2"), resolved.Path,
		"a row without even a filename keeps the legacy fallback, ext included only when known")
}

// TestNamingMsgIDShapesFreshAndCachedPaths pins [output] naming = "msgid":
// the file component becomes <msgID>_<index><ext> for both fresh walked
// items and cached manifest rows, under the same template directories.
func TestNamingMsgIDShapesFreshAndCachedPaths(t *testing.T) {
	t.Parallel()

	newResolver := func() *runResolver {
		resolver := manifestResolver(fakeTitles{titles: map[int64]string{7: "News"}})
		resolver.naming = namingMsgID

		return resolver
	}

	t.Run("cached", func(t *testing.T) {
		t.Parallel()

		resolved, err := newResolver().Resolve(store.MediaItem{
			ChatID: 7, MessageID: 100, MediaIndex: 1,
			Filename: ptr("photo album.jpeg"), Date: ptr("2024-03-05T10:00:00Z"),
		})
		require.NoError(t, err)

		assert.Equal(t, filepath.Join("root", "News", "2024-03", "100_1.jpeg"), resolved.Path)
	})

	t.Run("fresh", func(t *testing.T) {
		t.Parallel()

		resolver := newResolver()

		fctx := filters.Context{
			Chat:    filters.Chat{ID: 7, Title: "News"},
			Message: filters.Message{ID: 101, Date: 1709632800},
			File:    &filters.FileInfo{Present: true, Kind: "document", Name: "report.bin", Ext: ".bin"},
		}

		resolver.cache.put(msgCacheKey{chatID: 7, msgID: 101},
			walkedMessage{fctx: &fctx, msg: docMessageWith(101, 778, "ref")})

		resolved, err := resolver.Resolve(store.MediaItem{ChatID: 7, MessageID: 101, MediaIndex: 3})
		require.NoError(t, err)

		assert.Equal(t, filepath.Join("root", "News", "2024-03", "101_3.bin"), resolved.Path)
		assert.Equal(t, "report.bin", resolved.Meta.Filename,
			"metadata keeps the Telegram original name, only the path renames")
	})
}

// TestStoreOnlyResolveSurfacesTitleReadError pins that a broken title read
// fails the resolve loudly instead of silently degrading the path.
func TestStoreOnlyResolveSurfacesTitleReadError(t *testing.T) {
	t.Parallel()

	resolver := manifestResolver(fakeTitles{err: errForTest})

	_, err := resolver.Resolve(store.MediaItem{ChatID: 7, MessageID: 1, Filename: ptr("a.pdf")})
	require.ErrorIs(t, err, errForTest)
}
