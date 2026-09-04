package tg_test

import (
	"teleparse/internal/tg"
	"testing"

	"github.com/gotd/td/telegram/message/peer"
	gotdtg "github.com/gotd/td/tg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func entitiesWith(users map[int64]*gotdtg.User, chats map[int64]*gotdtg.Chat, channels map[int64]*gotdtg.Channel) peer.Entities {
	return peer.NewEntities(users, chats, channels)
}

func TestChatInfoFromElemUsers(t *testing.T) {
	t.Parallel()

	entities := entitiesWith(map[int64]*gotdtg.User{
		10: {ID: 10, FirstName: "Alice", LastName: "Smith", Username: "alice", Phone: "+15550001"},
		20: {ID: 20, FirstName: "Robot", Bot: true, Username: "helperbot"},
	}, nil, nil)

	private, ok := tg.ChatInfoFromElem(&gotdtg.Dialog{Peer: &gotdtg.PeerUser{UserID: 10}}, entities)
	require.True(t, ok)
	assert.Equal(t, tg.ChatTypePrivate, private.Type)
	assert.Equal(t, "Alice Smith", private.Title)
	assert.Equal(t, "alice", private.Username)
	assert.False(t, private.Archived)
	assert.False(t, private.Protected)

	bot, ok := tg.ChatInfoFromElem(&gotdtg.Dialog{Peer: &gotdtg.PeerUser{UserID: 20}}, entities)
	require.True(t, ok)
	assert.Equal(t, tg.ChatTypeBot, bot.Type)
	assert.Equal(t, "Robot", bot.Title)

	missing, ok := tg.ChatInfoFromElem(&gotdtg.Dialog{Peer: &gotdtg.PeerUser{UserID: 99}}, entities)
	assert.False(t, ok)
	assert.Empty(t, missing.Title)
}

func TestChatInfoFromElemGroup(t *testing.T) {
	t.Parallel()

	entities := entitiesWith(nil, map[int64]*gotdtg.Chat{
		30: {ID: 30, Title: "Old Group"},
		31: {ID: 31, Title: "Locked Group", Noforwards: true},
	}, nil)

	group, ok := tg.ChatInfoFromElem(&gotdtg.Dialog{Peer: &gotdtg.PeerChat{ChatID: 30}}, entities)
	require.True(t, ok)
	assert.Equal(t, tg.ChatTypeGroup, group.Type)
	assert.Equal(t, "Old Group", group.Title)
	assert.False(t, group.Protected)

	locked, ok := tg.ChatInfoFromElem(&gotdtg.Dialog{Peer: &gotdtg.PeerChat{ChatID: 31}}, entities)
	require.True(t, ok)
	assert.True(t, locked.Protected)
}

func TestChatInfoFromElemChannels(t *testing.T) {
	t.Parallel()

	entities := entitiesWith(nil, nil, map[int64]*gotdtg.Channel{
		40: {ID: 40, Title: "News", Broadcast: true, Username: "news"},
		41: {ID: 41, Title: "Big Chat", Megagroup: true},
		42: {ID: 42, Title: "Forum Home", Megagroup: true, Forum: true},
		43: {ID: 43, Title: "Private Feed", Broadcast: true, Noforwards: true},
		44: {ID: 44, Title: "Giga", Gigagroup: true},
	})

	channel, ok := tg.ChatInfoFromElem(&gotdtg.Dialog{Peer: &gotdtg.PeerChannel{ChannelID: 40}}, entities)
	require.True(t, ok)
	assert.Equal(t, tg.ChatTypeChannel, channel.Type)
	assert.Equal(t, "news", channel.Username)

	supergroup, ok := tg.ChatInfoFromElem(&gotdtg.Dialog{Peer: &gotdtg.PeerChannel{ChannelID: 41}}, entities)
	require.True(t, ok)
	assert.Equal(t, tg.ChatTypeSupergroup, supergroup.Type)

	forum, ok := tg.ChatInfoFromElem(&gotdtg.Dialog{Peer: &gotdtg.PeerChannel{ChannelID: 42}}, entities)
	require.True(t, ok)
	assert.Equal(t, tg.ChatTypeForum, forum.Type)

	protected, ok := tg.ChatInfoFromElem(&gotdtg.Dialog{Peer: &gotdtg.PeerChannel{ChannelID: 43}}, entities)
	require.True(t, ok)
	assert.True(t, protected.Protected)

	giga, ok := tg.ChatInfoFromElem(&gotdtg.Dialog{Peer: &gotdtg.PeerChannel{ChannelID: 44}}, entities)
	require.True(t, ok)
	assert.Equal(t, tg.ChatTypeChannel, giga.Type)
}

func TestChatInfoFromElemArchived(t *testing.T) {
	t.Parallel()

	entities := entitiesWith(map[int64]*gotdtg.User{10: {ID: 10, FirstName: "Arch"}}, nil, nil)

	dialog := &gotdtg.Dialog{Peer: &gotdtg.PeerUser{UserID: 10}, FolderID: 1}

	info, ok := tg.ChatInfoFromElem(dialog, entities)
	require.True(t, ok)
	assert.True(t, info.Archived)

	dialog.FolderID = 0
	info, ok = tg.ChatInfoFromElem(dialog, entities)
	require.True(t, ok)
	assert.False(t, info.Archived)
}

func TestValidatePhone(t *testing.T) {
	t.Parallel()

	for _, phone := range []string{"+15551234567", "+79001234567", "+85212345678"} {
		require.NoError(t, tg.ValidatePhone(phone), "phone %q", phone)
	}

	for _, phone := range []string{"5551234567", "+05551234", "abc", "+1 555 123 4567", "", "+155512345678901234"} {
		assert.ErrorIs(t, tg.ValidatePhone(phone), tg.ErrBadPhone, "phone %q", phone)
	}
}
