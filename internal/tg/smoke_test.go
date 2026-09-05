package tg_test

import (
	"fmt"
	"testing"

	"github.com/4q4r/teleparse/internal/testutil/tlmock"
	"github.com/4q4r/teleparse/internal/tg"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/telegram/query/dialogs"
	gotdtg "github.com/gotd/td/tg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Offline smoke tests over the TL mock. The exported Chats, Contacts and
// FindChat helpers take a *telegram.Client, whose API surface is only wired
// inside client.Run (gotd keeps the tg.Client construction private), so the
// flows below drive the same production building blocks those helpers use —
// the gotd dialogs query builder and ChatInfoFromElem — over a mock-backed
// *tg.Client. Login, Logout and WhoAmI share the same client boundary and
// are additionally constrained by client.Auth()/session internals that gotd
// does not expose; their prompter and validation logic is covered in
// login_prompt_internal_test.go. The untestable remainder is a thin shell
// left to online verification.

// smokeChatBatch mirrors the production dialog batch size.
const smokeChatBatch = 2

func smokeUserEntities() []gotdtg.UserClass {
	return []gotdtg.UserClass{
		tlmock.User(10, "Alice", tlmock.WithUsername("alice"), tlmock.WithAccessHash(100)),
		tlmock.User(11, "Arch Bot", tlmock.WithUsername("archbot"),
			tlmock.WithAccessHash(101), tlmock.WithBot),
	}
}

func smokeChatEntities() []gotdtg.ChatClass {
	return []gotdtg.ChatClass{
		tlmock.ChatSmall(20, "Old Group"),
		tlmock.Channel(30, "News", tlmock.WithBroadcast,
			tlmock.WithChannelUsername("news"), tlmock.WithChannelAccessHash(300)),
		tlmock.Channel(31, "Forum Home", tlmock.WithMegagroup, tlmock.WithForum,
			tlmock.WithChannelAccessHash(301)),
		tlmock.Channel(32, "Locked", tlmock.WithBroadcast, tlmock.WithNoforwards,
			tlmock.WithChannelAccessHash(302)),
	}
}

func smokeDialogsPage() ([]gotdtg.DialogClass, []gotdtg.MessageClass) {
	raw := []*gotdtg.Dialog{
		tlmock.Dialog(&gotdtg.PeerUser{UserID: 10}, 4010, 0),
		tlmock.Dialog(&gotdtg.PeerUser{UserID: 11}, 4011, 1),
		tlmock.Dialog(&gotdtg.PeerChat{ChatID: 20}, 4020, 0),
		tlmock.Dialog(&gotdtg.PeerChannel{ChannelID: 30}, 4030, 0),
		tlmock.Dialog(&gotdtg.PeerChannel{ChannelID: 31}, 4031, 0),
		tlmock.Dialog(&gotdtg.PeerChannel{ChannelID: 32}, 4032, 0),
	}

	peers := []gotdtg.PeerClass{
		&gotdtg.PeerUser{UserID: 10}, &gotdtg.PeerUser{UserID: 11}, &gotdtg.PeerChat{ChatID: 20},
		&gotdtg.PeerChannel{ChannelID: 30}, &gotdtg.PeerChannel{ChannelID: 31},
		&gotdtg.PeerChannel{ChannelID: 32},
	}

	dialogsList := make([]gotdtg.DialogClass, 0, len(raw))
	messages := make([]gotdtg.MessageClass, 0, len(raw))

	for idx, dialog := range raw {
		dialogsList = append(dialogsList, dialog)
		messages = append(messages, tlmock.Msg(dialog.TopMessage,
			fmt.Sprintf("top-%d", idx), tlmock.WithPeer(peers[idx])))
	}

	return dialogsList, messages
}

func TestSmokeDialogIterationAndChatInfo(t *testing.T) {
	t.Parallel()

	mock := tlmock.New()

	dialogsAll, topMessages := smokeDialogsPage()

	pages := []bin.Object{
		tlmock.DialogSlicePage(len(dialogsAll), dialogsAll[:smokeChatBatch],
			topMessages[:smokeChatBatch], smokeUserEntities(), smokeChatEntities()),
		tlmock.DialogPage(dialogsAll[smokeChatBatch:], topMessages[smokeChatBatch:],
			nil, smokeChatEntities()),
	}

	tlmock.HandleFunc(mock,
		func(*gotdtg.MessagesGetDialogsRequest) (bin.Object, error) {
			if len(pages) == 0 {
				return tlmock.DialogPage(nil, nil, nil, nil), nil
			}

			next := pages[0]
			pages = pages[1:]

			return next, nil
		})

	client := mock.Client()

	iterator := dialogs.NewQueryBuilder(client).GetDialogs().
		BatchSize(smokeChatBatch).Iter()

	chats := make([]tg.ChatInfo, 0)

	for iterator.Next(t.Context()) {
		elem := iterator.Value()

		dialog, ok := elem.Dialog.(*gotdtg.Dialog)
		require.True(t, ok)

		info, ok := tg.ChatInfoFromElem(dialog, elem.Entities)
		require.True(t, ok, "dialog %v must map with its page entities", dialog.Peer)

		chats = append(chats, info)
	}

	require.NoError(t, iterator.Err())
	require.Len(t, chats, 6)

	expect := []struct {
		id        int64
		typ       string
		archived  bool
		protected bool
	}{
		{10, tg.ChatTypePrivate, false, false},
		{11, tg.ChatTypeBot, true, false},
		{20, tg.ChatTypeGroup, false, false},
		{30, tg.ChatTypeChannel, false, false},
		{31, tg.ChatTypeForum, false, false},
		{32, tg.ChatTypeChannel, false, true},
	}

	for idx, row := range expect {
		assert.Equal(t, row.id, chats[idx].ID, "chat %d", idx)
		assert.Equal(t, row.typ, chats[idx].Type, "chat %d", idx)
		assert.Equal(t, row.archived, chats[idx].Archived, "chat %d", idx)
		assert.Equal(t, row.protected, chats[idx].Protected, "chat %d", idx)
	}

	requests := tlmock.Requests[gotdtg.MessagesGetDialogsRequest](mock)
	require.Len(t, requests, 3, "two content pages plus gotd's terminal request")
	assert.Equal(t, 4011, requests[1].OffsetID, "offset advances to the last top message")

	offsetPeer, ok := requests[1].OffsetPeer.(*gotdtg.InputPeerUser)
	require.True(t, ok)
	assert.Equal(t, int64(11), offsetPeer.UserID)
	assert.Equal(t, int64(101), offsetPeer.AccessHash)
}

func TestSmokeContactsFetchAndMapping(t *testing.T) {
	t.Parallel()

	mock := tlmock.New()

	tlmock.HandleFunc(mock,
		func(request *gotdtg.ContactsGetContactsRequest) (bin.Object, error) {
			assert.Zero(t, request.Hash, "the full list is requested with hash 0")

			return tlmock.ContactsList(
				tlmock.User(10, "Alice", tlmock.WithUsername("alice")),
				tlmock.User(11, "Robot", tlmock.WithBot),
			), nil
		})

	result, err := mock.Client().ContactsGetContacts(t.Context(), 0)
	require.NoError(t, err)

	list, ok := result.(*gotdtg.ContactsContacts)
	require.True(t, ok)
	require.Len(t, list.Users, 2)

	// Mirror the Contacts mapping: every contact renders as a private chat.
	chats := make([]tg.ChatInfo, 0, len(list.Users))

	for _, userClass := range list.Users {
		user, valid := userClass.(*gotdtg.User)
		require.True(t, valid)

		info, mapped := tg.ChatInfoFromElem(&gotdtg.Dialog{Peer: &gotdtg.PeerUser{UserID: user.ID}},
			peer.NewEntities(map[int64]*gotdtg.User{user.ID: user}, nil, nil))
		require.True(t, mapped)

		chats = append(chats, info)
	}

	require.Len(t, chats, 2)
	assert.Equal(t, tg.ChatTypePrivate, chats[0].Type)
	assert.Equal(t, "Alice", chats[0].Title)
	assert.Equal(t, "alice", chats[0].Username)
	assert.Equal(t, tg.ChatTypeBot, chats[1].Type)
}

func TestSmokeResolveUsernameAndFindByID(t *testing.T) {
	t.Parallel()

	mock := tlmock.New()

	dialogsAll, topMessages := smokeDialogsPage()

	tlmock.HandleFunc(mock,
		func(*gotdtg.MessagesGetDialogsRequest) (bin.Object, error) {
			return tlmock.DialogPage(dialogsAll, topMessages,
				smokeUserEntities(), smokeChatEntities()), nil
		})

	tlmock.HandleFunc(mock,
		func(request *gotdtg.ContactsResolveUsernameRequest) (bin.Object, error) {
			assert.Equal(t, "news", request.Username)

			return tlmock.ResolvedPeer(&gotdtg.PeerChannel{ChannelID: 30},
				nil, []gotdtg.ChatClass{tlmock.Channel(30, "News", tlmock.WithBroadcast,
					tlmock.WithChannelUsername("news"), tlmock.WithChannelAccessHash(300))}), nil
		})

	client := mock.Client()

	// The FindChat username path: resolve, then map through the entity set.
	resolved, err := client.ContactsResolveUsername(t.Context(),
		&gotdtg.ContactsResolveUsernameRequest{Username: "news"})
	require.NoError(t, err)

	entities := peer.EntitiesFromResult(resolved)

	info, ok := tg.ChatInfoFromElem(&gotdtg.Dialog{Peer: resolved.Peer}, entities)
	require.True(t, ok)
	assert.Equal(t, tg.ChatTypeChannel, info.Type)
	assert.Equal(t, "News", info.Title)

	// The findChatByID path: scan dialogs for the numeric id.
	var found *tg.ChatInfo

	iterator := dialogs.NewQueryBuilder(client).GetDialogs().BatchSize(smokeChatBatch).Iter()

	for iterator.Next(t.Context()) {
		elem := iterator.Value()

		dialog, valid := elem.Dialog.(*gotdtg.Dialog)
		require.True(t, valid)

		candidate, mapped := tg.ChatInfoFromElem(dialog, elem.Entities)
		if !mapped || candidate.ID != 31 {
			continue
		}

		found = &candidate

		break
	}

	require.NoError(t, iterator.Err())
	require.NotNil(t, found)
	assert.Equal(t, tg.ChatTypeForum, found.Type)
}
