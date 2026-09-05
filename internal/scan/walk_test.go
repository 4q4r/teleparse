package scan_test

import (
	"context"
	"testing"

	"github.com/4q4r/teleparse/internal/filters"
	"github.com/4q4r/teleparse/internal/scan"

	tg "github.com/gotd/td/tg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeWalkAPI struct {
	historyReq  *tg.MessagesGetHistoryRequest
	searchReq   *tg.MessagesSearchRequest
	repliesReqs []*tg.MessagesGetRepliesRequest
	topicsReq   *tg.MessagesGetForumTopicsRequest
	history     tg.MessagesMessagesClass
	search      tg.MessagesMessagesClass
	replies     tg.MessagesMessagesClass
	forumTopics *tg.MessagesForumTopics
}

//nolint:ireturn // mirrors the tg.Client method signature under test
func (f *fakeWalkAPI) MessagesGetHistory(
	ctx context.Context, request *tg.MessagesGetHistoryRequest,
) (tg.MessagesMessagesClass, error) {
	f.historyReq = request

	if f.history != nil {
		return f.history, nil
	}

	return &tg.MessagesMessages{}, nil
}

//nolint:ireturn // mirrors the tg.Client method signature under test
func (f *fakeWalkAPI) MessagesSearch(
	ctx context.Context, request *tg.MessagesSearchRequest,
) (tg.MessagesMessagesClass, error) {
	f.searchReq = request

	if f.search != nil {
		return f.search, nil
	}

	return &tg.MessagesMessages{}, nil
}

//nolint:ireturn // mirrors the tg.Client method signature under test
func (f *fakeWalkAPI) MessagesGetReplies(
	ctx context.Context, request *tg.MessagesGetRepliesRequest,
) (tg.MessagesMessagesClass, error) {
	f.repliesReqs = append(f.repliesReqs, request)

	if f.replies != nil {
		return f.replies, nil
	}

	return &tg.MessagesMessages{}, nil
}

func (f *fakeWalkAPI) UsersGetUsers(ctx context.Context, id []tg.InputUserClass) ([]tg.UserClass, error) {
	return nil, nil
}

//nolint:ireturn // mirrors the tg.Client method signature under test
func (f *fakeWalkAPI) ChannelsGetChannels(
	ctx context.Context, id []tg.InputChannelClass,
) (tg.MessagesChatsClass, error) {
	return &tg.MessagesChats{}, nil
}

func (f *fakeWalkAPI) MessagesGetForumTopics(
	ctx context.Context, request *tg.MessagesGetForumTopicsRequest,
) (*tg.MessagesForumTopics, error) {
	f.topicsReq = request

	if f.forumTopics != nil {
		return f.forumTopics, nil
	}

	return &tg.MessagesForumTopics{}, nil
}

func textMessage(id int, fromUser int64, text string) *tg.Message {
	msg := &tg.Message{ID: id, Date: 1700000000 + id, Message: text}
	msg.PeerID = &tg.PeerChannel{ChannelID: 30}

	if fromUser > 0 {
		msg.SetFromID(&tg.PeerUser{UserID: fromUser})
	}

	return msg
}

func photoMessage(id int, fromUser int64, caption string) *tg.Message {
	msg := &tg.Message{ID: id, Date: 1700000000 + id, Message: caption}
	msg.PeerID = &tg.PeerChannel{ChannelID: 30}
	msg.SetFromID(&tg.PeerUser{UserID: fromUser})
	msg.SetMedia(&tg.MessageMediaPhoto{Photo: &tg.Photo{}})

	return msg
}

func messagesResult(messages []tg.MessageClass, users ...tg.UserClass) *tg.MessagesMessages {
	return &tg.MessagesMessages{Messages: messages, Users: users}
}

func walkTarget() scan.Target {
	return scan.Target{
		InputPeer: &tg.InputPeerChannel{ChannelID: 30, AccessHash: 300},
		Chat:      filters.Chat{ID: 30, Type: "channel", Title: "News"},
	}
}

func emitCollector() (scan.EmitFunc, *[]filters.Context, *[]*tg.Message) {
	contexts := &[]filters.Context{}
	messages := &[]*tg.Message{}

	emit := func(mctx filters.Context, msg *tg.Message) error {
		*contexts = append(*contexts, mctx)
		*messages = append(*messages, msg)

		return nil
	}

	return emit, contexts, messages
}

func newTestWalker(api *fakeWalkAPI, senders *scan.SenderCache) *scan.HistoryWalker {
	return scan.NewHistoryWalker(api, scan.HistoryFeeds(api), senders)
}

func TestPushdownFilterTable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		want tg.MessagesFilterClass
	}{
		{"photo", &tg.InputMessagesFilterPhotos{}},
		{"video", &tg.InputMessagesFilterVideo{}},
		{"photo_video", &tg.InputMessagesFilterPhotoVideo{}},
		{"document", &tg.InputMessagesFilterDocument{}},
		{"url", &tg.InputMessagesFilterURL{}},
		{"gif", &tg.InputMessagesFilterGif{}},
		{"voice", &tg.InputMessagesFilterVoice{}},
		{"music", &tg.InputMessagesFilterMusic{}},
		{"round_video", &tg.InputMessagesFilterRoundVideo{}},
		{"round_voice", &tg.InputMessagesFilterRoundVoice{}},
		{"geo", &tg.InputMessagesFilterGeo{}},
		{"contact", &tg.InputMessagesFilterContacts{}},
		{"pinned", &tg.InputMessagesFilterPinned{}},
		{"chat_photos", &tg.InputMessagesFilterChatPhotos{}},
		{"phone_calls", &tg.InputMessagesFilterPhoneCalls{}},
		{"my_mentions", &tg.InputMessagesFilterMyMentions{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := scan.PushdownFilter(tc.name)

			require.NotNil(t, got)
			assert.Equal(t, tc.want, got)
		})
	}

	assert.Nil(t, scan.PushdownFilter(""))
	assert.Nil(t, scan.PushdownFilter("sticker"))
}

func TestWalkEmitsMatchesWithSender(t *testing.T) {
	t.Parallel()

	api := &fakeWalkAPI{search: messagesResult(
		[]tg.MessageClass{
			textMessage(1, 10, "plain"),
			photoMessage(2, 10, "with photo"),
		},
		&tg.User{ID: 10, FirstName: "Alice", Username: "alice"},
	)}
	senders := scan.NewSenderCache(api)
	walker := newTestWalker(api, senders)

	plan, err := filters.Compile(&filters.Options{Media: []string{"photo"}})
	require.NoError(t, err)

	emit, contexts, messages := emitCollector()

	require.NoError(t, walker.Walk(t.Context(), walkTarget(), plan, filters.Options{}, emit))

	require.Len(t, *contexts, 1)
	assert.Equal(t, "photo", (*contexts)[0].File.Kind)
	assert.Equal(t, "with photo", (*contexts)[0].Message.Caption)
	assert.True(t, (*contexts)[0].Sender.Present)
	assert.Equal(t, "Alice", (*contexts)[0].Sender.Name)
	assert.Equal(t, "alice", (*contexts)[0].Sender.Username)
	require.NotNil(t, (*messages)[0])
	assert.Equal(t, 2, (*messages)[0].ID)
	assert.Nil(t, api.historyReq)
	require.NotNil(t, api.searchReq)
	searchPeer, ok := api.searchReq.Peer.(*tg.InputPeerChannel)
	require.True(t, ok)
	assert.Equal(t, int64(30), searchPeer.ChannelID)
}

func TestWalkPredicateGating(t *testing.T) {
	t.Parallel()

	api := &fakeWalkAPI{history: messagesResult([]tg.MessageClass{
		textMessage(1, 10, "small"),
		textMessage(2, 10, "match me please"),
		textMessage(3, 10, "other"),
	})}
	walker := newTestWalker(api, scan.NewSenderCache(api))

	plan := &filters.Plan{Predicates: []filters.NamedPredicate{
		{Name: "text", Fn: func(mctx *filters.Context) bool {
			return mctx.TextOrCaption() == "match me please"
		}},
	}}

	emit, contexts, messages := emitCollector()

	require.NoError(t, walker.Walk(t.Context(), walkTarget(), plan, filters.Options{}, emit))

	require.Len(t, *contexts, 1)
	assert.Equal(t, int64(2), (*contexts)[0].Message.ID)
	require.Len(t, *messages, 1)
}

func TestWalkServiceMessage(t *testing.T) {
	t.Parallel()

	service := &tg.MessageService{ID: 9, Date: 1700000099, Action: &tg.MessageActionChatCreate{Title: "x"}}
	service.PeerID = &tg.PeerChannel{ChannelID: 30}

	api := &fakeWalkAPI{history: messagesResult([]tg.MessageClass{service})}
	walker := newTestWalker(api, scan.NewSenderCache(api))

	emit, contexts, messages := emitCollector()

	require.NoError(t, walker.Walk(t.Context(), walkTarget(), &filters.Plan{}, filters.Options{}, emit))

	require.Len(t, *contexts, 1)
	assert.True(t, (*contexts)[0].Message.Service)
	assert.Nil(t, (*messages)[0])
}

func TestWalkLimitStopsScanning(t *testing.T) {
	t.Parallel()

	api := &fakeWalkAPI{history: messagesResult([]tg.MessageClass{
		textMessage(1, 0, "one"),
		textMessage(2, 0, "two"),
		textMessage(3, 0, "three"),
	})}
	walker := newTestWalker(api, scan.NewSenderCache(api))

	emit, contexts, _ := emitCollector()

	require.NoError(t, walker.Walk(t.Context(), walkTarget(), &filters.Plan{}, filters.Options{Limit: 2}, emit))

	require.Len(t, *contexts, 2)
}

func TestWalkReverseEmitsOldestFirst(t *testing.T) {
	t.Parallel()

	api := &fakeWalkAPI{history: messagesResult([]tg.MessageClass{
		textMessage(1, 0, "old"),
		textMessage(2, 0, "mid"),
		textMessage(3, 0, "new"),
	})}
	walker := newTestWalker(api, scan.NewSenderCache(api))

	emit, contexts, _ := emitCollector()

	require.NoError(t, walker.Walk(t.Context(), walkTarget(), &filters.Plan{}, filters.Options{Reverse: true}, emit))

	require.Len(t, *contexts, 3)
	assert.Equal(t, int64(1), (*contexts)[0].Message.ID)
	assert.Equal(t, int64(2), (*contexts)[1].Message.ID)
	assert.Equal(t, int64(3), (*contexts)[2].Message.ID)
}

func TestWalkForumTopics(t *testing.T) {
	t.Parallel()

	target := scan.Target{
		InputPeer: &tg.InputPeerChannel{ChannelID: 31, AccessHash: 301},
		Chat:      filters.Chat{ID: 31, Type: "forum", Title: "Forum Home"},
	}
	api := &fakeWalkAPI{
		forumTopics: &tg.MessagesForumTopics{Topics: []tg.ForumTopicClass{
			&tg.ForumTopic{ID: 1},
			&tg.ForumTopic{ID: 5},
		}},
		replies: messagesResult([]tg.MessageClass{textMessage(50, 0, "topic msg")}),
	}
	walker := newTestWalker(api, scan.NewSenderCache(api))

	emit, contexts, _ := emitCollector()

	require.NoError(t, walker.Walk(
		t.Context(), target, &filters.Plan{}, filters.Options{Recursion: filters.Recursion{Topics: true}}, emit,
	))

	firstTopicReqs := 0
	secondTopicReqs := 0
	for _, req := range api.repliesReqs {
		switch req.MsgID {
		case 1:
			firstTopicReqs++
		case 5:
			secondTopicReqs++
		default:
			t.Errorf("unexpected topic id %d", req.MsgID)
		}
	}
	assert.Positive(t, firstTopicReqs)
	assert.Positive(t, secondTopicReqs)
	assert.Nil(t, api.historyReq)
	assert.Len(t, *contexts, 2)
}

func TestWalkPushdownUsesSearch(t *testing.T) {
	t.Parallel()

	api := &fakeWalkAPI{}
	walker := newTestWalker(api, scan.NewSenderCache(api))

	plan := &filters.Plan{Pushdown: filters.Pushdown{
		MessagesFilter: "photo",
		MinDate:        1600000000,
		MaxDate:        1700000000,
	}}

	emit, _, _ := emitCollector()

	require.NoError(t, walker.Walk(
		t.Context(), walkTarget(), plan, filters.Options{MinID: 10, MaxID: 100}, emit,
	))

	require.Nil(t, api.historyReq)
	require.NotNil(t, api.searchReq)
	assert.Equal(t, &tg.InputMessagesFilterPhotos{}, api.searchReq.Filter)
	assert.Equal(t, 1600000000, api.searchReq.MinDate)
	assert.Equal(t, 1700000000, api.searchReq.MaxDate)
	assert.Equal(t, 10, api.searchReq.MinID)
	assert.Equal(t, 100, api.searchReq.MaxID)
}

func TestWalkErrorFromEmitPropagates(t *testing.T) {
	t.Parallel()

	api := &fakeWalkAPI{history: messagesResult([]tg.MessageClass{textMessage(1, 0, "x")})}
	walker := newTestWalker(api, scan.NewSenderCache(api))

	failing := func(mctx filters.Context, msg *tg.Message) error {
		return assert.AnError
	}

	require.ErrorIs(t, walker.Walk(t.Context(), walkTarget(), &filters.Plan{}, filters.Options{}, failing), assert.AnError)
}

func TestSenderCacheSeedAndMiss(t *testing.T) {
	t.Parallel()

	api := &fakeWalkAPI{}
	senders := scan.NewSenderCache(api)

	fromPeer := &tg.PeerUser{UserID: 10}
	sender, err := senders.Get(t.Context(), fromPeer)
	require.NoError(t, err)
	assert.False(t, sender.Present)
}
