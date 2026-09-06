package cli

import (
	"testing"

	"github.com/4q4r/teleparse/internal/config"
	"github.com/4q4r/teleparse/internal/filters"
	"github.com/4q4r/teleparse/internal/scan"
	"github.com/4q4r/teleparse/internal/store"
	"github.com/4q4r/teleparse/internal/testutil/tlmock"

	"github.com/gotd/td/bin"
	tg "github.com/gotd/td/tg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newGetFixture opens a temp store plus a collector wired like executeRun,
// with the target chat already registered.
func newGetFixture(t *testing.T) *walkCollector {
	t.Helper()

	state := openTestStore(t)
	require.NoError(t, state.UpsertChat(t.Context(), store.Chat{ChatID: 30, Type: "channel", Title: ptr("News")}))

	app := &App{cfg: config.Default(), paths: &config.Paths{Downloads: t.TempDir()}}
	_, collector := newRunResolver(app, &fakeRefetchAPI{}, state)

	return collector
}

func getTestTarget() scan.Target {
	return scan.Target{
		InputPeer: &tg.InputPeerChannel{ChannelID: 30, AccessHash: 300},
		Chat:      filters.Chat{ID: 30, Type: "channel", Title: "News"},
	}
}

// photoMessage builds a channel message carrying a photo document.
func photoMessage(id int, groupedID int64) *tg.Message {
	return tlmock.Msg(id, "caption", tlmock.WithGroupedID(groupedID), tlmock.WithPhoto(tlmock.Photo(
		int64(id)*10, tlmock.PhotoSize("x", 128, 128, 512*id),
	)))
}

func TestGetFetchesExplicitIDsThroughChannelSeam(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	collector := newGetFixture(t)

	mock := tlmock.New()
	tlmock.HandleFunc[tg.ChannelsGetMessagesRequest](mock, func(
		request *tg.ChannelsGetMessagesRequest,
	) (bin.Object, error) {
		return tlmock.ChannelHistoryPage(1,
			[]tg.MessageClass{photoMessage(100, 0)}, nil, nil), nil
	})

	plan, err := filters.Compile(&filters.Options{})
	require.NoError(t, err)

	require.NoError(t, fetchTargetMessages(ctx, mock.Client(), getFetches(getTestTarget(),
		linkTarget{PeerSpec: "t.me/news", MsgIDs: []int{100}}), plan, collector, false))

	requests := tlmock.Requests[tg.ChannelsGetMessagesRequest](mock)
	require.Len(t, requests, 1, "one explicit-id fetch per chunk")
	channel, valid := requests[0].Channel.(*tg.InputChannel)
	require.True(t, valid)
	assert.Equal(t, int64(30), channel.ChannelID)

	require.Len(t, requests[0].ID, 1)

	requested, valid := requests[0].ID[0].(*tg.InputMessageID)
	require.True(t, valid)
	assert.Equal(t, 100, requested.ID)

	require.Len(t, collector.items, 1)
	assert.Equal(t, int64(100), collector.items[0].MessageID)
	assert.Equal(t, "photo", collector.items[0].MediaClass)
}

func TestGetAppliesFiltersToLinkItems(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	collector := newGetFixture(t)

	mock := tlmock.New()

	video := tlmock.Msg(100, "v", tlmock.WithDocument(tlmock.Document(777, "video/mp4", 4096,
		tlmock.VideoAttr(64, 64, 5), tlmock.NamedFileAttr("clip.mp4"))))
	photo := photoMessage(101, 0)

	tlmock.HandleFunc[tg.ChannelsGetMessagesRequest](mock, func(
		request *tg.ChannelsGetMessagesRequest,
	) (bin.Object, error) {
		return tlmock.ChannelHistoryPage(2, []tg.MessageClass{video, photo}, nil, nil), nil
	})

	opts := filters.Options{Media: []string{"photo"}}

	plan, err := filters.Compile(&opts)
	require.NoError(t, err)

	require.NoError(t, fetchTargetMessages(ctx, mock.Client(), getFetches(getTestTarget(),
		linkTarget{PeerSpec: "t.me/news", MsgIDs: []int{100, 101}}), plan, collector, false))

	require.Len(t, collector.items, 1, "the video must be filtered out, the photo kept")
	assert.Equal(t, int64(101), collector.items[0].MessageID)
}

func TestGetAlbumExpansionFetchesSiblings(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	collector := newGetFixture(t)

	mock := tlmock.New()

	// Album of three: 99, 100, 101 share grouped_id 9; the sibling window
	// fetch answers with the two neighbors.
	tlmock.HandleFunc[tg.ChannelsGetMessagesRequest](mock, func(
		request *tg.ChannelsGetMessagesRequest,
	) (bin.Object, error) {
		messages := []tg.MessageClass{photoMessage(100, 9)}

		for _, entry := range request.ID {
			requested, valid := entry.(*tg.InputMessageID)
			require.True(t, valid)

			id := requested.ID

			switch id {
			case 99, 101:
				messages = append(messages, photoMessage(id, 9))
			case 98:
				messages = append(messages, photoMessage(id, 0))
			}
		}

		return tlmock.ChannelHistoryPage(len(messages), messages, nil, nil), nil
	})

	plan, err := filters.Compile(&filters.Options{})
	require.NoError(t, err)

	require.NoError(t, fetchTargetMessages(ctx, mock.Client(), getFetches(getTestTarget(),
		linkTarget{PeerSpec: "t.me/news", MsgIDs: []int{100}}), plan, collector, true))

	requests := tlmock.Requests[tg.ChannelsGetMessagesRequest](mock)
	require.Len(t, requests, 2, "the grouped message must trigger one sibling fetch")

	window := map[int]bool{}

	for _, entry := range requests[1].ID {
		requested, valid := entry.(*tg.InputMessageID)
		require.True(t, valid)

		window[requested.ID] = true
	}

	assert.Len(t, window, albumSpan*2, "the window covers id-9..id+9 minus fetched ids")
	assert.True(t, window[99] && window[101], "the sibling ids are inside the window")

	ids := make([]int64, 0, len(collector.items))

	for idx := range collector.items {
		ids = append(ids, collector.items[idx].MessageID)
	}

	assert.ElementsMatch(t, []int64{99, 100, 101}, ids, "same-group siblings join the manifest")
}

func TestGetAlbumExpansionDisabled(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	collector := newGetFixture(t)

	mock := tlmock.New()
	tlmock.HandleFunc[tg.ChannelsGetMessagesRequest](mock, func(
		request *tg.ChannelsGetMessagesRequest,
	) (bin.Object, error) {
		return tlmock.ChannelHistoryPage(1, []tg.MessageClass{photoMessage(100, 9)}, nil, nil), nil
	})

	plan, err := filters.Compile(&filters.Options{})
	require.NoError(t, err)

	require.NoError(t, fetchTargetMessages(ctx, mock.Client(), getFetches(getTestTarget(),
		linkTarget{PeerSpec: "t.me/news", MsgIDs: []int{100}}), plan, collector, false))

	assert.Len(t, tlmock.Requests[tg.ChannelsGetMessagesRequest](mock), 1, "--group=false keeps the fetch explicit")
	require.Len(t, collector.items, 1)
}

func TestGetTopicContextUsesGetRepliesWindow(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	collector := newGetFixture(t)

	mock := tlmock.New()
	tlmock.HandleFunc[tg.MessagesGetRepliesRequest](mock, func(
		request *tg.MessagesGetRepliesRequest,
	) (bin.Object, error) {
		noise := tlmock.Msg(549, "before")
		wanted := photoMessage(550, 0)

		return tlmock.HistoryPage([]tg.MessageClass{wanted, noise}, nil, nil), nil
	})

	plan, err := filters.Compile(&filters.Options{})
	require.NoError(t, err)

	require.NoError(t, fetchTargetMessages(ctx, mock.Client(), getFetches(getTestTarget(),
		linkTarget{PeerSpec: "t.me/news", TopicID: 500, MsgIDs: []int{550}}), plan, collector, false))

	requests := tlmock.Requests[tg.MessagesGetRepliesRequest](mock)
	require.NotEmpty(t, requests)
	assert.Equal(t, 500, requests[0].MsgID, "the topic id maps to the getReplies context")
	assert.Equal(t, 549, requests[0].MinID, "the window starts one below the wanted id")
	assert.Equal(t, 551, requests[0].MaxID, "the window ends one above the wanted id")

	require.Len(t, collector.items, 1, "only the wanted comment id joins the manifest")
	assert.Equal(t, int64(550), collector.items[0].MessageID)
}

func TestGetUserChatUsesMessagesGetMessages(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	collector := newGetFixture(t)

	mock := tlmock.New()
	tlmock.HandleFunc[tg.MessagesGetMessagesRequest](mock, func(
		request *tg.MessagesGetMessagesRequest,
	) (bin.Object, error) {
		return tlmock.HistoryPage([]tg.MessageClass{photoMessage(100, 0)}, nil, nil), nil
	})

	userTarget := scan.Target{
		InputPeer: &tg.InputPeerUser{UserID: 10, AccessHash: 100},
		Chat:      filters.Chat{ID: 10, Type: "private", Title: "Ann"},
	}

	plan, err := filters.Compile(&filters.Options{})
	require.NoError(t, err)

	require.NoError(t, fetchTargetMessages(ctx, mock.Client(), getFetches(userTarget,
		linkTarget{PeerSpec: "t.me/ann", MsgIDs: []int{100}}), plan, collector, false))

	assert.NotEmpty(t, tlmock.Requests[tg.MessagesGetMessagesRequest](mock),
		"non-channel peers fetch through messages.getMessages")
	require.Len(t, collector.items, 1)
}

// getFetches wraps targets and contexts into the peerFetch pairs
// fetchTargetMessages consumes.
func getFetches(target scan.Target, contexts ...linkTarget) []peerFetch {
	return []peerFetch{{target: target, contexts: contexts}}
}
