package scan_test

import (
	"testing"

	"github.com/4q4r/teleparse/internal/filters"

	"github.com/gotd/td/telegram/message/peer"
	tg "github.com/gotd/td/tg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveAllCarriesDialogNewestID(t *testing.T) {
	t.Parallel()

	users := map[int64]*tg.User{10: {ID: 10, FirstName: "Alice", AccessHash: 100}}
	channels := map[int64]*tg.Channel{
		30: {ID: 30, Title: "News", Broadcast: true, AccessHash: 300},
	}

	dialogs := fakeDialogs{dialogs: []fakeDialog{
		{
			dialog:   &tg.Dialog{Peer: &tg.PeerUser{UserID: 10}, TopMessage: 777},
			entities: peer.NewEntities(users, nil, channels),
		},
		{
			dialog:   &tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 30}, TopMessage: 4242},
			entities: peer.NewEntities(users, nil, channels),
		},
	}}

	resolver := newResolver(fakeScopeAPI{}, dialogs)

	targets, err := resolver.Resolve(t.Context(), []string{"all"}, filters.Options{})
	require.NoError(t, err)
	require.Len(t, targets, 2)

	byID := map[int64]int64{}
	for _, target := range targets {
		byID[target.Chat.ID] = target.NewestID
	}

	assert.Equal(t, int64(777), byID[10], "user dialog newest id comes free from the dialogs page")
	assert.Equal(t, int64(4242), byID[30], "channel dialog newest id comes free from the dialogs page")
}

func TestResolveNumericSpecCarriesDialogNewestID(t *testing.T) {
	t.Parallel()

	channels := map[int64]*tg.Channel{
		30: {ID: 30, Title: "News", Broadcast: true, AccessHash: 300},
	}

	dialogs := fakeDialogs{dialogs: []fakeDialog{
		{
			dialog:   &tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 30}, TopMessage: 99},
			entities: peer.NewEntities(nil, nil, channels),
		},
	}}

	resolver := newResolver(fakeScopeAPI{}, dialogs)

	targets, err := resolver.Resolve(t.Context(), []string{"30"}, filters.Options{})
	require.NoError(t, err)
	require.Len(t, targets, 1)

	assert.Equal(t, int64(99), targets[0].NewestID)
}

func TestResolveUsernameSpecHasNoDialogNewestID(t *testing.T) {
	t.Parallel()

	api := fakeScopeAPI{resolved: map[string]*tg.ContactsResolvedPeer{
		"durov": {
			Peer:  &tg.PeerUser{UserID: 10},
			Users: []tg.UserClass{&tg.User{ID: 10, FirstName: "Pavel", Username: "durov", AccessHash: 7}},
		},
	}}
	resolver := newResolver(api, sampleDialogs())

	targets, err := resolver.Resolve(t.Context(), []string{"@durov"}, filters.Options{})
	require.NoError(t, err)
	require.Len(t, targets, 1)

	assert.Zero(t, targets[0].NewestID, "username resolution carries no top message; the probe covers it")
}
