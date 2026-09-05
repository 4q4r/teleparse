package scan_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/filters"
	"github.com/4q4r/teleparse/internal/scan"
	"github.com/4q4r/teleparse/internal/testutil/tlmock"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/telegram/query/dialogs"
	tg "github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file contains offline smoke tests: every Telegram flow runs end-to-end
// through a real *tg.Client backed by the tlmock invoker, driving the
// production scope resolver, history walker, feed factory and sender cache.
// Requests and responses cross the real TL wire codec, so assertions read
// exactly what the production RPC stack would transmit.

// dialogsBatch mirrors the production dialog batch size.
const dialogsBatch = 100

// smokeDialogs drives the production gotd dialog iterator over the mock API,
// mirroring scan's unexported queryDialogs lister wire-for-wire.
type smokeDialogs struct {
	api *tg.Client
}

func (l smokeDialogs) Each(ctx context.Context, visit func(*tg.Dialog, peer.Entities) error) error {
	iterator := dialogs.NewQueryBuilder(l.api).GetDialogs().BatchSize(dialogsBatch).Iter()

	for iterator.Next(ctx) {
		elem := iterator.Value()

		dialog, ok := elem.Dialog.(*tg.Dialog)
		if !ok {
			continue
		}

		if err := visit(dialog, elem.Entities); err != nil {
			return fmt.Errorf("visit dialog: %w", err)
		}
	}

	if err := iterator.Err(); err != nil {
		return fmt.Errorf("iterate dialogs: %w", err)
	}

	return nil
}

func smokeResolver(mock *tlmock.Mock) *scan.ScopeResolver {
	client := mock.Client()

	return scan.NewScopeResolver(scan.ScopeDeps{API: client, Dialogs: smokeDialogs{api: client}})
}

//nolint:ireturn // fixture pages must feed the bin.Object invoker seam
func popPage(pages *[]bin.Object, empty func() bin.Object) (bin.Object, error) {
	if len(*pages) == 0 {
		return empty(), nil
	}

	next := (*pages)[0]
	*pages = (*pages)[1:]

	return next, nil
}

func smokeUsers() []tg.UserClass {
	return []tg.UserClass{
		tlmock.User(10, "Alice", tlmock.WithUsername("alice"), tlmock.WithAccessHash(100)),
		tlmock.User(11, "Robot", tlmock.WithUsername("robot"), tlmock.WithAccessHash(101), tlmock.WithBot),
	}
}

func smokeChats() []tg.ChatClass {
	return []tg.ChatClass{tlmock.ChatSmall(20, "Old Group")}
}

func smokeChannels() []tg.ChatClass {
	return []tg.ChatClass{
		tlmock.Channel(30, "News", tlmock.WithBroadcast,
			tlmock.WithChannelUsername("news"), tlmock.WithChannelAccessHash(300)),
		tlmock.Channel(31, "Forum Home", tlmock.WithMegagroup, tlmock.WithForum,
			tlmock.WithChannelAccessHash(301)),
		tlmock.Channel(32, "Giga", tlmock.WithGigagroup, tlmock.WithChannelAccessHash(302)),
		tlmock.Channel(33, "Locked", tlmock.WithBroadcast, tlmock.WithNoforwards,
			tlmock.WithChannelAccessHash(303)),
	}
}

// smokeDialogMatrix returns the seven-dialog universe used across scope
// smokes: private user, archived bot, legacy group, broadcast channel,
// forum, gigagroup and a noforwards-protected channel.
func smokeDialogMatrix() ([]tg.DialogClass, []tg.MessageClass) {
	raw := []*tg.Dialog{
		tlmock.Dialog(&tg.PeerUser{UserID: 10}, 4010, 0),
		tlmock.Dialog(&tg.PeerUser{UserID: 11}, 4011, 1),
		tlmock.Dialog(&tg.PeerChat{ChatID: 20}, 4020, 0),
		tlmock.Dialog(&tg.PeerChannel{ChannelID: 30}, 4030, 0),
		tlmock.Dialog(&tg.PeerChannel{ChannelID: 31}, 4031, 0),
		tlmock.Dialog(&tg.PeerChannel{ChannelID: 32}, 4032, 0),
		tlmock.Dialog(&tg.PeerChannel{ChannelID: 33}, 4033, 0),
	}

	peers := []tg.PeerClass{
		&tg.PeerUser{UserID: 10}, &tg.PeerUser{UserID: 11}, &tg.PeerChat{ChatID: 20},
		&tg.PeerChannel{ChannelID: 30}, &tg.PeerChannel{ChannelID: 31},
		&tg.PeerChannel{ChannelID: 32}, &tg.PeerChannel{ChannelID: 33},
	}

	dialogsList := make([]tg.DialogClass, 0, len(raw))
	messages := make([]tg.MessageClass, 0, len(raw))

	for idx, dialog := range raw {
		dialogsList = append(dialogsList, dialog)
		messages = append(messages, tlmock.Msg(dialog.TopMessage,
			fmt.Sprintf("top-%d", idx), tlmock.WithPeer(peers[idx])))
	}

	return dialogsList, messages
}

func smokeWalker(mock *tlmock.Mock) *scan.HistoryWalker {
	client := mock.Client()

	return scan.NewHistoryWalker(client, scan.HistoryFeeds(client), scan.NewSenderCache(client))
}

func newsTarget() scan.Target {
	return scan.Target{
		InputPeer: &tg.InputPeerChannel{ChannelID: 30, AccessHash: 300},
		Chat:      filters.Chat{ID: 30, Type: "channel", Title: "News"},
	}
}

func forumTarget() scan.Target {
	return scan.Target{
		InputPeer: &tg.InputPeerChannel{ChannelID: 31, AccessHash: 301},
		Chat:      filters.Chat{ID: 31, Type: "forum", Title: "Forum Home"},
	}
}

func emptyPlan(t *testing.T) *filters.Plan {
	t.Helper()

	plan, err := filters.Compile(&filters.Options{})
	require.NoError(t, err)

	return plan
}

func serveHistory(mock *tlmock.Mock, pages ...bin.Object) {
	tlmock.HandleFunc(mock,
		func(*tg.MessagesGetHistoryRequest) (bin.Object, error) {
			return popPage(&pages, func() bin.Object {
				return tlmock.HistoryPage(nil, nil, nil)
			})
		})
}

func TestSmokeResolveAllDialogsPaginated(t *testing.T) {
	t.Parallel()

	mock := tlmock.New()

	dialogsAll, topMessages := smokeDialogMatrix()

	pages := []bin.Object{
		tlmock.DialogSlicePage(len(dialogsAll), dialogsAll[:4],
			topMessages[:4], smokeUsers(), append(smokeChats(), smokeChannels()...)),
		tlmock.DialogPage(dialogsAll[4:], topMessages[4:], nil, smokeChannels()[1:]),
	}

	tlmock.HandleFunc(mock,
		func(*tg.MessagesGetDialogsRequest) (bin.Object, error) {
			return popPage(&pages, func() bin.Object {
				return tlmock.DialogPage(nil, nil, nil, nil)
			})
		})

	targets, err := smokeResolver(mock).Resolve(t.Context(), []string{"all"}, filters.Options{})
	require.NoError(t, err)
	require.Len(t, targets, 7)

	expect := []struct {
		id        int64
		typ       string
		archived  bool
		protected bool
	}{
		{10, "private", false, false},
		{11, "bot", true, false},
		{20, "group", false, false},
		{30, "channel", false, false},
		{31, "forum", false, false},
		{32, "supergroup", false, false},
		{33, "channel", false, true},
	}

	for idx, row := range expect {
		assert.Equal(t, row.id, targets[idx].Chat.ID, "target %d", idx)
		assert.Equal(t, row.typ, targets[idx].Chat.Type, "target %d", idx)
		assert.Equal(t, row.archived, targets[idx].Chat.Archived, "target %d", idx)
		assert.Equal(t, row.protected, targets[idx].Chat.Protected, "target %d", idx)
	}

	channel, ok := targets[3].InputPeer.(*tg.InputPeerChannel)
	require.True(t, ok)
	assert.Equal(t, int64(300), channel.AccessHash)

	requests := tlmock.Requests[tg.MessagesGetDialogsRequest](mock)
	// gotd's dialog iterator fires one terminal request after the closing
	// page; its response is decoded and discarded, so three calls in total.
	require.Len(t, requests, 3, "slice page must be followed by the closing page")
	assert.Zero(t, requests[0].OffsetID)
	assert.Equal(t, 4030, requests[1].OffsetID, "offset must advance to the last top message")

	offsetPeer, ok := requests[1].OffsetPeer.(*tg.InputPeerChannel)
	require.True(t, ok)
	assert.Equal(t, int64(30), offsetPeer.ChannelID)
	assert.Equal(t, int64(300), offsetPeer.AccessHash)
}

func TestSmokeResolveAllPrefilters(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		opts  filters.Options
		count int
		title string
	}{
		{"chat_type", filters.Options{ChatType: []string{"channel"}}, 2, "News"},
		{"archived_only", filters.Options{Archived: "only"}, 1, "Robot"},
		{"glob", filters.Options{ChatGlob: "N*"}, 1, "News"},
		{"regex", filters.Options{ChatRegex: "^Forum"}, 1, "Forum Home"},
		{"username", filters.Options{ChatUsername: "alice"}, 1, "Alice"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mock := tlmock.New()
			dialogsAll, topMessages := smokeDialogMatrix()

			tlmock.HandleFunc(mock,
				func(*tg.MessagesGetDialogsRequest) (bin.Object, error) {
					return tlmock.DialogPage(dialogsAll, topMessages,
						smokeUsers(), append(smokeChats(), smokeChannels()...)), nil
				})

			targets, err := smokeResolver(mock).Resolve(t.Context(), []string{"all"}, tc.opts)
			require.NoError(t, err)
			require.Len(t, targets, tc.count)

			assert.Equal(t, tc.title, targets[0].Chat.Title)
		})
	}
}

func TestSmokeResolveAllContactsOnly(t *testing.T) {
	t.Parallel()

	mock := tlmock.New()
	dialogsAll, topMessages := smokeDialogMatrix()

	tlmock.HandleFunc(mock,
		func(*tg.MessagesGetDialogsRequest) (bin.Object, error) {
			return tlmock.DialogPage(dialogsAll, topMessages,
				smokeUsers(), append(smokeChats(), smokeChannels()...)), nil
		})

	tlmock.HandleFunc(mock,
		func(request *tg.ContactsGetContactsRequest) (bin.Object, error) {
			assert.Zero(t, request.Hash, "full contact list must be requested with hash 0")

			return tlmock.ContactsList(tlmock.User(10, "Alice")), nil
		})

	targets, err := smokeResolver(mock).Resolve(t.Context(), []string{"all"},
		filters.Options{ContactsOnly: true})
	require.NoError(t, err)
	require.Len(t, targets, 1)

	assert.Equal(t, "Alice", targets[0].Chat.Title)
	require.Len(t, tlmock.Requests[tg.ContactsGetContactsRequest](mock), 1)
}

func TestSmokeResolveUsernameNumericSavedAndUnknown(t *testing.T) {
	t.Parallel()

	mock := tlmock.New()
	dialogsAll, topMessages := smokeDialogMatrix()

	tlmock.HandleFunc(mock,
		func(*tg.MessagesGetDialogsRequest) (bin.Object, error) {
			return tlmock.DialogPage(dialogsAll, topMessages,
				smokeUsers(), append(smokeChats(), smokeChannels()...)), nil
		})

	tlmock.HandleFunc(mock,
		func(request *tg.ContactsResolveUsernameRequest) (bin.Object, error) {
			assert.Equal(t, "news", request.Username)

			return tlmock.ResolvedPeer(&tg.PeerChannel{ChannelID: 30},
				nil, []tg.ChatClass{tlmock.Channel(30, "News", tlmock.WithBroadcast,
					tlmock.WithChannelAccessHash(300))}), nil
		})

	resolver := smokeResolver(mock)

	targets, err := resolver.Resolve(t.Context(), []string{"@news"}, filters.Options{})
	require.NoError(t, err)
	require.Len(t, targets, 1)

	peerChannel, ok := targets[0].InputPeer.(*tg.InputPeerChannel)
	require.True(t, ok)
	assert.Equal(t, int64(30), peerChannel.ChannelID)
	assert.Equal(t, int64(300), peerChannel.AccessHash)
	require.Len(t, tlmock.Requests[tg.ContactsResolveUsernameRequest](mock), 1)

	saved, err := resolver.Resolve(t.Context(), []string{"saved"}, filters.Options{})
	require.NoError(t, err)
	require.Len(t, saved, 1)
	require.IsType(t, &tg.InputPeerSelf{}, saved[0].InputPeer)

	numeric, err := resolver.Resolve(t.Context(), []string{"-10031"}, filters.Options{})
	require.NoError(t, err)
	require.Len(t, numeric, 1)
	assert.Equal(t, "forum", numeric[0].Chat.Type)

	_, err = resolver.Resolve(t.Context(), []string{"9999"}, filters.Options{})
	require.ErrorIs(t, err, scan.ErrUnknownChat)
	assert.Contains(t, err.Error(), "teleparse chats list")
}

func TestSmokeSearchPushdownDocumentAndDate(t *testing.T) {
	t.Parallel()

	mock := tlmock.New()

	plan, err := filters.Compile(&filters.Options{
		Media: []string{"document"},
		After: "7d",
	})
	require.NoError(t, err)

	freshDate := int(time.Now().Add(-time.Hour).Unix())

	tlmock.HandleFunc(mock,
		func(request *tg.MessagesSearchRequest) (bin.Object, error) {
			if request.OffsetID > 0 {
				return tlmock.HistoryPage(nil, nil, nil), nil
			}

			return tlmock.HistoryPage([]tg.MessageClass{
				tlmock.Msg(21, "report.zip", tlmock.WithDate(freshDate),
					tlmock.WithDocument(tlmock.Document(77,
						"application/zip", 1024, tlmock.NamedFileAttr("report.zip")))),
			}, nil, nil), nil
		})

	var emitted []filters.Context

	require.NoError(t, smokeWalker(mock).Walk(t.Context(), newsTarget(), plan,
		filters.Options{}, func(mctx filters.Context, _ *tg.Message) error {
			emitted = append(emitted, mctx)

			return nil
		}))

	require.Len(t, emitted, 1)
	require.NotNil(t, emitted[0].File)
	assert.Equal(t, "document", emitted[0].File.Kind)
	assert.Equal(t, ".zip", emitted[0].File.Ext)

	searches := tlmock.Requests[tg.MessagesSearchRequest](mock)
	// One content page plus gotd's terminal no-more-messages request.
	require.Len(t, searches, 2)

	document, ok := searches[0].Filter.(*tg.InputMessagesFilterDocument)
	require.True(t, ok, "captured filter must decode to inputMessagesFilterDocument")
	assert.Equal(t, &tg.InputMessagesFilterDocument{}, document)
	assert.Positive(t, searches[0].MinDate, "after=7d must push a MinDate bound server-side")
	assert.Empty(t, searches[0].Q)
	assert.Empty(t, tlmock.Requests[tg.MessagesGetHistoryRequest](mock),
		"constrained walks must use messages.search, not messages.getHistory")
}

func idRange(from, to int) []tg.MessageClass {
	messages := make([]tg.MessageClass, 0, from-to+1)

	for id := from; id >= to; id-- {
		messages = append(messages, tlmock.Msg(id, fmt.Sprintf("m%d", id)))
	}

	return messages
}

func TestSmokeHistoryWindowAndPagination(t *testing.T) {
	t.Parallel()

	mock := tlmock.New()

	pages := []bin.Object{
		tlmock.ChannelHistoryPage(103, idRange(200, 101), nil, nil),
		tlmock.ChannelHistoryPage(103, idRange(100, 98), nil, nil),
	}

	serveHistory(mock, pages...)

	var emitted []filters.Context

	require.NoError(t, smokeWalker(mock).Walk(t.Context(), newsTarget(), emptyPlan(t),
		filters.Options{MinID: 50, MaxID: 95},
		func(mctx filters.Context, _ *tg.Message) error {
			emitted = append(emitted, mctx)

			return nil
		}))

	require.Len(t, emitted, 103, "both pages must be walked")
	assert.Equal(t, int64(200), emitted[0].Message.ID)
	assert.Equal(t, int64(98), emitted[len(emitted)-1].Message.ID)

	requests := tlmock.Requests[tg.MessagesGetHistoryRequest](mock)
	// Two content pages plus gotd's terminal request after the short page.
	require.Len(t, requests, 3, "a full page must trigger exactly one follow-up page")
	assert.Equal(t, 50, requests[0].MinID)
	assert.Equal(t, 95, requests[0].MaxID)
	assert.Zero(t, requests[0].OffsetID)
	assert.Equal(t, 101, requests[1].OffsetID, "second page must resume at the lowest seen id")
	assert.Equal(t, 1700000000, requests[1].OffsetDate)
}

func TestSmokeFloodWaitOnSecondPageSurfaces(t *testing.T) {
	t.Parallel()

	mock := tlmock.New()

	calls := 0

	tlmock.HandleFunc(mock,
		func(*tg.MessagesGetHistoryRequest) (bin.Object, error) {
			calls++

			if calls == 1 {
				return tlmock.ChannelHistoryPage(200, idRange(200, 101), nil, nil), nil
			}

			return nil, tlmock.FloodWait(30)
		})

	err := smokeWalker(mock).Walk(t.Context(), newsTarget(), emptyPlan(t),
		filters.Options{}, func(filters.Context, *tg.Message) error { return nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "News")

	wait, ok := tgerr.AsFloodWait(err)
	require.True(t, ok, "the ratelimit error must surface through the walker wrap")
	assert.Equal(t, 30*time.Second, wait)
}

func TestSmokeForumTopicWalkPerTopicReplies(t *testing.T) {
	t.Parallel()

	mock := tlmock.New()

	tlmock.HandleFunc(mock,
		func(*tg.MessagesGetForumTopicsRequest) (bin.Object, error) {
			return tlmock.Topics(2, tlmock.Topic(7, "Ideas"), tlmock.Topic(9, "Bugs")), nil
		})

	tlmock.HandleFunc(mock,
		func(request *tg.MessagesGetRepliesRequest) (bin.Object, error) {
			if request.OffsetID > 0 {
				return tlmock.HistoryPage(nil, nil, nil), nil
			}

			return tlmock.HistoryPage([]tg.MessageClass{
				tlmock.Msg(request.MsgID*100, fmt.Sprintf("topic-%d", request.MsgID),
					tlmock.WithFrom(10)),
			}, []tg.UserClass{tlmock.User(10, "Alice")}, nil), nil
		})

	tlmock.HandleFunc(mock,
		func(*tg.UsersGetUsersRequest) (bin.Object, error) {
			return &tg.UserClassVector{Elems: []tg.UserClass{tlmock.User(10, "Alice")}}, nil
		})

	var emitted []filters.Context

	require.NoError(t, smokeWalker(mock).Walk(t.Context(), forumTarget(), emptyPlan(t),
		filters.Options{MinID: 5, Recursion: filters.Recursion{Topics: true}},
		func(mctx filters.Context, _ *tg.Message) error {
			emitted = append(emitted, mctx)

			return nil
		}))

	require.Len(t, emitted, 2)
	assert.Equal(t, int64(700), emitted[0].Message.ID)
	assert.Equal(t, int64(900), emitted[1].Message.ID)

	replies := tlmock.Requests[tg.MessagesGetRepliesRequest](mock)
	// One content page plus gotd's terminal request per topic.
	require.Len(t, replies, 4)

	topicIDs := make([]int, 0, len(replies))

	for _, request := range replies {
		topicIDs = append(topicIDs, request.MsgID)
	}

	assert.Equal(t, []int{7, 7, 9, 9}, topicIDs, "each topic thread is walked in order")
	assert.Equal(t, 5, replies[0].MinID, "the id window must travel into topic iteration")
	assert.Positive(t, replies[1].OffsetID, "the terminal topic request resumes after the last reply")

	// messages.getReplies carries no filter parameter (see
	// core.telegram.org/method/messages.getReplies), so topic iteration must
	// never issue a media-filtered search or a flat history request.
	assert.Empty(t, tlmock.Requests[tg.MessagesSearchRequest](mock))
	assert.Empty(t, tlmock.Requests[tg.MessagesGetHistoryRequest](mock))
}

func TestSmokeSenderCacheResolution(t *testing.T) {
	t.Parallel()

	mock := tlmock.New()

	// Telegram history pages arrive newest-first, and the gotd iterator
	// re-sorts descending, so fixtures list highest ids first.
	page := tlmock.HistoryPage([]tg.MessageClass{
		tlmock.Msg(6, "negative-cache", tlmock.WithFrom(12)),
		tlmock.Msg(5, "missing", tlmock.WithFrom(12)),
		tlmock.Msg(4, "channel", tlmock.WithFromChannel(31)),
		tlmock.Msg(3, "cached", tlmock.WithFrom(11)),
		tlmock.Msg(2, "resolved", tlmock.WithFrom(11)),
		tlmock.Msg(1, "seeded", tlmock.WithFrom(10)),
	}, []tg.UserClass{tlmock.User(10, "Alice")}, nil)

	serveHistory(mock, page)

	tlmock.HandleFunc(mock,
		func(request *tg.UsersGetUsersRequest) (bin.Object, error) {
			require.Len(t, request.ID, 1)

			input, ok := request.ID[0].(*tg.InputUser)
			require.True(t, ok)

			if input.UserID == 12 {
				return nil, tlmock.FileRefExpired()
			}

			return &tg.UserClassVector{Elems: []tg.UserClass{
				tlmock.User(input.UserID, "Bob", tlmock.WithUsername("bob")),
			}}, nil
		})

	tlmock.HandleFunc(mock,
		func(request *tg.ChannelsGetChannelsRequest) (bin.Object, error) {
			require.Len(t, request.ID, 1)

			return &tg.MessagesChats{Chats: []tg.ChatClass{tlmock.Channel(31, "Chan")}}, nil
		})

	var emitted []filters.Context

	require.NoError(t, smokeWalker(mock).Walk(t.Context(), newsTarget(), emptyPlan(t),
		filters.Options{}, func(mctx filters.Context, _ *tg.Message) error {
			emitted = append(emitted, mctx)

			return nil
		}))

	require.Len(t, emitted, 6)

	assert.False(t, emitted[0].Sender.Present, "failed resolution must be negatively cached")
	assert.False(t, emitted[1].Sender.Present, "failed resolution must degrade to absent")
	assert.Equal(t, "Chan", emitted[2].Sender.Name)
	assert.Equal(t, "Bob", emitted[3].Sender.Name)
	assert.Equal(t, "Bob", emitted[4].Sender.Name)
	assert.Equal(t, "Alice", emitted[5].Sender.Name, "entity-seeded sender needs no RPC")

	userCalls := tlmock.Requests[tg.UsersGetUsersRequest](mock)
	require.Len(t, userCalls, 2, "resolved and failed users each cost exactly one call")

	for idx, want := range []int64{12, 11} {
		input, ok := userCalls[idx].ID[0].(*tg.InputUser)
		require.True(t, ok, "call %d", idx)
		assert.Equal(t, want, input.UserID, "call %d", idx)
	}
	require.Len(t, tlmock.Requests[tg.ChannelsGetChannelsRequest](mock), 1)
}

func TestSmokeAlbumAndServiceMessages(t *testing.T) {
	t.Parallel()

	mock := tlmock.New()

	albumDoc := tlmock.Document(77, "application/zip", 1024, tlmock.NamedFileAttr("a.zip"))

	page := tlmock.HistoryPage([]tg.MessageClass{
		tlmock.ServiceMsg(203, &tg.MessageActionChatCreate{Title: "Old Group", Users: []int64{1}}),
		tlmock.Msg(202, "two", tlmock.WithGroupedID(5), tlmock.WithDocument(albumDoc)),
		tlmock.Msg(201, "one", tlmock.WithGroupedID(5), tlmock.WithDocument(albumDoc)),
	}, nil, nil)

	serveHistory(mock, page)

	tlmock.HandleFunc(mock,
		func(*tg.UsersGetUsersRequest) (bin.Object, error) {
			return &tg.UserClassVector{}, nil
		})

	var (
		contexts []filters.Context
		raws     []*tg.Message
	)

	require.NoError(t, smokeWalker(mock).Walk(t.Context(), newsTarget(), emptyPlan(t),
		filters.Options{}, func(mctx filters.Context, msg *tg.Message) error {
			contexts = append(contexts, mctx)
			raws = append(raws, msg)

			return nil
		}))

	require.Len(t, contexts, 3)

	assert.True(t, contexts[0].Message.Service, "service messages must map to Service=true")
	assert.Nil(t, contexts[0].File, "service messages carry no downloadable media")
	assert.Nil(t, raws[0], "the raw service message is nil at the emit seam")

	assert.Equal(t, int64(5), contexts[1].Message.GroupedID)
	assert.Equal(t, int64(5), contexts[2].Message.GroupedID, "both album halves must be emitted")

	for _, mctx := range contexts[1:] {
		require.NotNil(t, mctx.File)
		assert.Equal(t, "document", mctx.File.Kind)
		assert.Equal(t, "a.zip", mctx.File.Name)
		assert.Equal(t, ".zip", mctx.File.Ext)
		assert.Equal(t, int64(1024), mctx.File.Size)
		assert.Equal(t, "application/zip", mctx.File.Mime)
	}
}
