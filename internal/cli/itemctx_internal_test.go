package cli

import (
	"errors"
	"net"
	"path/filepath"
	"testing"

	"github.com/4q4r/teleparse/internal/config"
	"github.com/4q4r/teleparse/internal/download"
	"github.com/4q4r/teleparse/internal/filters"
	"github.com/4q4r/teleparse/internal/scan"
	"github.com/4q4r/teleparse/internal/store"

	tg "github.com/gotd/td/tg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// docMessageWith builds a document message carrying the given reference.
func docMessageWith(msgID, docID int64, reference string) *tg.Message {
	return &tg.Message{ID: int(msgID), Media: &tg.MessageMediaDocument{Document: &tg.Document{
		ID: docID, AccessHash: 99, FileReference: []byte(reference), DCID: 4,
	}}}
}

// cachedItemResolver builds a resolver over the fake API with chat 7 known
// as a channel peer, mirroring a run whose targets were resolved.
func cachedItemResolver(api *fakeRefetchAPI) *runResolver {
	return &runResolver{
		root:     "root",
		template: "{chat}/{msgid}_{filename}",
		cache:    newMessageCache(8),
		peers: map[int64]tg.InputPeerClass{
			7: &tg.InputPeerChannel{ChannelID: 7, AccessHash: 10},
		},
		api: api,
	}
}

// TestResolveHydratesCachedManifestItem pins the incremental-merge fix: a
// manifest row with no walked message context resolves lazily, refetches
// the message through the run's peer, and seeds the cache so a later
// Resolve reuses the fresh location without another request.
func TestResolveHydratesCachedManifestItem(t *testing.T) {
	t.Parallel()

	api := &fakeRefetchAPI{
		messages: &tg.MessagesChannelMessages{Messages: []tg.MessageClass{docMessageWith(100, 777, "fresh")}},
	}

	resolver := cachedItemResolver(api)

	item := store.MediaItem{ChatID: 7, MessageID: 100, MediaIndex: 0, Filename: ptr("doc.pdf")}

	first, err := resolver.Resolve(item)
	require.NoError(t, err)
	assert.Nil(t, first.Location, "a cache miss carries no location yet")
	require.NotNil(t, first.Refetch, "the refetch hook is always attached")
	assert.Equal(t, filepath.Join("root", "7", "100_0.pdf"), first.Path)

	location, err := first.Refetch(t.Context())
	require.NoError(t, err)

	docLoc, ok := location.(*tg.InputDocumentFileLocation)
	require.True(t, ok, "documents hydrate a document file location")
	assert.Equal(t, int64(777), docLoc.ID)
	assert.Equal(t, int64(99), docLoc.AccessHash)
	assert.Equal(t, []byte("fresh"), docLoc.FileReference)

	require.True(t, api.usedChannel, "the channel peer rides channels.getMessages")
	require.Len(t, api.channelReqs, 1, "one refetch must issue one request")

	request := api.channelReqs[0]

	channel, ok := request.Channel.(*tg.InputChannel)
	require.True(t, ok)
	assert.Equal(t, int64(7), channel.ChannelID)
	assert.Equal(t, int64(10), channel.AccessHash)

	require.Len(t, request.ID, 1)

	wanted, ok := request.ID[0].(*tg.InputMessageID)
	require.True(t, ok)
	assert.Equal(t, 100, wanted.ID, "the request names the manifest message id")

	second, err := resolver.Resolve(item)
	require.NoError(t, err)
	require.NotNil(t, second.Location, "the seeded cache answers without a second request")
	assert.Equal(t, 4, second.DC, "the seeded cache also carries the document DC")
	assert.Equal(t, filepath.Join("root", "7", "100_0.pdf"), second.Path,
		"a refetched entry keeps the store-only fallback path")

	assert.Len(t, api.channelReqs, 1, "the second Resolve must not refetch again")
}

// TestRefetchSeedingPreservesWalkContext pins that refreshing a walked
// message's file reference never discards its filter context: the next
// Resolve keeps the templated path while riding the fresh reference.
func TestRefetchSeedingPreservesWalkContext(t *testing.T) {
	t.Parallel()

	api := &fakeRefetchAPI{
		messages: &tg.MessagesChannelMessages{Messages: []tg.MessageClass{docMessageWith(100, 777, "fresh")}},
	}

	resolver := cachedItemResolver(api)

	fctx := filters.Context{
		Chat:    filters.Chat{ID: 7, Type: "channel", Title: "News"},
		Message: filters.Message{ID: 100},
		File:    &filters.FileInfo{Present: true, Kind: "document", Name: "a.bin"},
	}

	stale := docMessageWith(100, 777, "stale")
	resolver.cache.put(msgCacheKey{chatID: 7, msgID: 100}, walkedMessage{fctx: &fctx, msg: stale})

	resolved, err := resolver.Resolve(store.MediaItem{ChatID: 7, MessageID: 100})
	require.NoError(t, err)

	fresh, err := resolved.Refetch(t.Context())
	require.NoError(t, err)
	require.IsType(t, &tg.InputDocumentFileLocation{}, fresh)

	again, err := resolver.Resolve(store.MediaItem{ChatID: 7, MessageID: 100})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("root", "News", "100_a.bin"), again.Path,
		"the walked template context must survive the refresh")

	docLoc, ok := again.Location.(*tg.InputDocumentFileLocation)
	require.True(t, ok)
	assert.Equal(t, []byte("fresh"), docLoc.FileReference, "the refreshed message supplies the location")
}

// TestRefetchErrorsClassifyHonestly pins that every refetch failure names
// its real cause — message gone, media absent or a retryable transport
// error — and never collapses into a missing-location error.
func TestRefetchErrorsClassifyHonestly(t *testing.T) {
	t.Parallel()

	item := store.MediaItem{ChatID: 7, MessageID: 100}

	t.Run("deleted message", func(t *testing.T) {
		t.Parallel()

		resolver := cachedItemResolver(&fakeRefetchAPI{messages: &tg.MessagesChannelMessages{}})

		resolved, err := resolver.Resolve(item)
		require.NoError(t, err)

		_, err = resolved.Refetch(t.Context())
		require.ErrorIs(t, err, download.ErrMessageGone)
		require.NotErrorIs(t, err, download.ErrNoLocation)
	})

	t.Run("message without media", func(t *testing.T) {
		t.Parallel()

		bare := &tg.Message{ID: 100}
		resolver := cachedItemResolver(&fakeRefetchAPI{
			messages: &tg.MessagesChannelMessages{Messages: []tg.MessageClass{bare}},
		})

		resolved, err := resolver.Resolve(item)
		require.NoError(t, err)

		_, err = resolved.Refetch(t.Context())
		require.ErrorIs(t, err, download.ErrMediaGone)
		assert.Contains(t, err.Error(), "carries no downloadable media",
			"the failure must name the media absence")
		require.NotErrorIs(t, err, download.ErrNoLocation)
	})

	t.Run("network failure stays retryable", func(t *testing.T) {
		t.Parallel()

		dropped := &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset")}
		resolver := cachedItemResolver(&fakeRefetchAPI{err: dropped})

		resolved, err := resolver.Resolve(item)
		require.NoError(t, err)

		_, err = resolved.Refetch(t.Context())
		require.ErrorIs(t, err, dropped)
		require.NotErrorIs(t, err, download.ErrMessageGone)
		require.NotErrorIs(t, err, download.ErrMediaGone)
		require.NotErrorIs(t, err, download.ErrNoLocation)
	})
}

// TestWithCachedPendingSkipsFreshWalkedTargets pins the target filter:
// pending rows of chats walked fully this run (no watermark) never merge,
// because their in-run walk already produced every owed row.
func TestWithCachedPendingSkipsFreshWalkedTargets(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	state := openTestStore(t)

	for _, chatID := range []int64{30, 31} {
		require.NoError(t, state.UpsertChat(ctx, store.Chat{ChatID: chatID, Type: "channel"}))
	}

	for _, row := range []store.MediaItem{
		{ChatID: 30, MessageID: 5, MediaClass: "photo", MediaID: 5, Status: store.StatusDiscovered},
		{ChatID: 31, MessageID: 6, MediaClass: "photo", MediaID: 6, Status: store.StatusDiscovered},
	} {
		require.NoError(t, func() error { _, err := state.UpsertMedia(ctx, &row); return err }())
	}

	app := &App{cfg: config.Default(), paths: &config.Paths{Downloads: t.TempDir()}}

	_, collector := newRunResolver(app, &fakeHistoryAPI{})
	collector.incremental[30] = true

	merged, err := withCachedPending(ctx, state,
		[]scan.Target{walkTestTarget(30, 0), walkTestTarget(31, 0)}, collector)
	require.NoError(t, err)

	require.Len(t, merged, 1)
	assert.Equal(t, int64(30), merged[0].ChatID, "only incrementally walked chats contribute pending rows")
}
