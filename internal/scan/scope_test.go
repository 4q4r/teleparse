package scan_test

import (
	"context"
	"teleparse/internal/filters"
	"teleparse/internal/scan"
	"testing"

	"github.com/gotd/td/telegram/message/peer"
	tg "github.com/gotd/td/tg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeDialog struct {
	dialog   *tg.Dialog
	entities peer.Entities
}

type fakeDialogs struct {
	dialogs []fakeDialog
}

func (f fakeDialogs) Each(ctx context.Context, fn func(*tg.Dialog, peer.Entities) error) error {
	for _, entry := range f.dialogs {
		if err := fn(entry.dialog, entry.entities); err != nil {
			return err
		}
	}

	return nil
}

type fakeScopeAPI struct {
	resolved map[string]*tg.ContactsResolvedPeer
	contacts *tg.ContactsContacts
}

func (f fakeScopeAPI) ContactsResolveUsername(
	ctx context.Context, req *tg.ContactsResolveUsernameRequest,
) (*tg.ContactsResolvedPeer, error) {
	if result, ok := f.resolved[req.Username]; ok {
		return result, nil
	}

	return &tg.ContactsResolvedPeer{}, nil
}

//nolint:ireturn // mirrors the tg.Client method signature under test
func (f fakeScopeAPI) ContactsGetContacts(ctx context.Context, hash int64) (tg.ContactsContactsClass, error) {
	if f.contacts != nil {
		return f.contacts, nil
	}

	return &tg.ContactsContacts{}, nil
}

func sampleDialogs() fakeDialogs {
	users := map[int64]*tg.User{
		10: {ID: 10, FirstName: "Alice", Username: "alice", AccessHash: 100},
		11: {ID: 11, FirstName: "Robot", Bot: true, Username: "robot", AccessHash: 101},
	}
	chats := map[int64]*tg.Chat{20: {ID: 20, Title: "Old Group"}}
	channels := map[int64]*tg.Channel{
		30: {ID: 30, Title: "News", Broadcast: true, Username: "news", AccessHash: 300},
		31: {ID: 31, Title: "Forum Home", Megagroup: true, Forum: true, AccessHash: 301},
		32: {ID: 32, Title: "Giga", Gigagroup: true, AccessHash: 302},
		33: {ID: 33, Title: "Locked", Broadcast: true, Noforwards: true, AccessHash: 303},
	}

	return fakeDialogs{dialogs: []fakeDialog{
		{dialog: &tg.Dialog{Peer: &tg.PeerUser{UserID: 10}}, entities: peer.NewEntities(users, chats, channels)},
		{
			dialog:   &tg.Dialog{Peer: &tg.PeerUser{UserID: 11}, FolderID: 1},
			entities: peer.NewEntities(users, chats, channels),
		},
		{dialog: &tg.Dialog{Peer: &tg.PeerChat{ChatID: 20}}, entities: peer.NewEntities(users, chats, channels)},
		{dialog: &tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 30}}, entities: peer.NewEntities(users, chats, channels)},
		{dialog: &tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 31}}, entities: peer.NewEntities(users, chats, channels)},
		{dialog: &tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 32}}, entities: peer.NewEntities(users, chats, channels)},
		{dialog: &tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 33}}, entities: peer.NewEntities(users, chats, channels)},
	}}
}

func newResolver(api fakeScopeAPI, lister fakeDialogs) *scan.ScopeResolver {
	return scan.NewScopeResolver(scan.ScopeDeps{API: api, Dialogs: lister})
}

func TestResolveSavedSpec(t *testing.T) {
	t.Parallel()

	resolver := newResolver(fakeScopeAPI{}, sampleDialogs())

	targets, err := resolver.Resolve(t.Context(), []string{"saved"}, filters.Options{})
	require.NoError(t, err)
	require.Len(t, targets, 1)

	require.IsType(t, &tg.InputPeerSelf{}, targets[0].InputPeer)
	assert.True(t, targets[0].Chat.Saved)
	assert.Equal(t, "private", targets[0].Chat.Type)
}

func TestResolveSavedOnlyOptionShortCircuits(t *testing.T) {
	t.Parallel()

	resolver := newResolver(fakeScopeAPI{}, fakeDialogs{})

	targets, err := resolver.Resolve(t.Context(), []string{"all"}, filters.Options{SavedOnly: true})
	require.NoError(t, err)
	require.Len(t, targets, 1)

	assert.True(t, targets[0].Chat.Saved)
}

func TestResolveUsernameForms(t *testing.T) {
	t.Parallel()

	api := fakeScopeAPI{resolved: map[string]*tg.ContactsResolvedPeer{
		"durov": {
			Peer:  &tg.PeerUser{UserID: 10},
			Users: []tg.UserClass{&tg.User{ID: 10, FirstName: "Pavel", Username: "durov", AccessHash: 7}},
		},
	}}
	resolver := newResolver(api, sampleDialogs())

	for _, spec := range []string{"@durov", "durov", "t.me/durov", "https://t.me/durov"} {
		targets, err := resolver.Resolve(t.Context(), []string{spec}, filters.Options{})
		require.NoError(t, err, "spec %q", spec)
		require.Len(t, targets, 1, "spec %q", spec)

		peerUser, ok := targets[0].InputPeer.(*tg.InputPeerUser)
		require.True(t, ok, "spec %q", spec)
		assert.Equal(t, int64(10), peerUser.UserID)
		assert.Equal(t, int64(7), peerUser.AccessHash)
		assert.Equal(t, "durov", targets[0].Chat.Username)
		assert.Equal(t, "Pavel", targets[0].Chat.Title)
	}
}

func TestResolveNumericSpecs(t *testing.T) {
	t.Parallel()

	resolver := newResolver(fakeScopeAPI{}, sampleDialogs())

	targets, err := resolver.Resolve(t.Context(), []string{"-10030", "20"}, filters.Options{})
	require.NoError(t, err)
	require.Len(t, targets, 2)

	channel, ok := targets[0].InputPeer.(*tg.InputPeerChannel)
	require.True(t, ok)
	assert.Equal(t, int64(30), channel.ChannelID)
	assert.Equal(t, int64(300), channel.AccessHash)
	assert.Equal(t, "channel", targets[0].Chat.Type)

	group, ok := targets[1].InputPeer.(*tg.InputPeerChat)
	require.True(t, ok)
	assert.Equal(t, int64(20), group.ChatID)
	assert.Equal(t, "group", targets[1].Chat.Type)
}

func TestResolveNumericSpecUnknown(t *testing.T) {
	t.Parallel()

	resolver := newResolver(fakeScopeAPI{}, sampleDialogs())

	_, err := resolver.Resolve(t.Context(), []string{"9999"}, filters.Options{})
	require.ErrorIs(t, err, scan.ErrUnknownChat)
	assert.Contains(t, err.Error(), "teleparse chats list")
}

func TestResolveAllDialogs(t *testing.T) {
	t.Parallel()

	resolver := newResolver(fakeScopeAPI{}, sampleDialogs())

	targets, err := resolver.Resolve(t.Context(), []string{"all"}, filters.Options{})
	require.NoError(t, err)
	require.Len(t, targets, 7)

	expect := []struct {
		id   int64
		typ  string
		want tg.InputPeerClass
	}{
		{10, "private", &tg.InputPeerUser{UserID: 10, AccessHash: 100}},
		{11, "bot", &tg.InputPeerUser{UserID: 11, AccessHash: 101}},
		{20, "group", &tg.InputPeerChat{ChatID: 20}},
		{30, "channel", &tg.InputPeerChannel{ChannelID: 30, AccessHash: 300}},
		{31, "forum", &tg.InputPeerChannel{ChannelID: 31, AccessHash: 301}},
		{32, "supergroup", &tg.InputPeerChannel{ChannelID: 32, AccessHash: 302}},
		{33, "channel", &tg.InputPeerChannel{ChannelID: 33, AccessHash: 303}},
	}

	for idx, row := range expect {
		assert.Equal(t, row.id, targets[idx].Chat.ID, "target %d", idx)
		assert.Equal(t, row.typ, targets[idx].Chat.Type, "target %d", idx)
		assert.Equal(t, row.want, targets[idx].InputPeer, "target %d", idx)
	}

	assert.True(t, targets[1].Chat.Archived)
	assert.False(t, targets[0].Chat.Archived)
	assert.True(t, targets[6].Chat.Protected)
	assert.Equal(t, "news", targets[3].Chat.Username)
}

func TestResolveAllPrefilters(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		opts  filters.Options
		count int
		title string
	}{
		{"chat_type", filters.Options{ChatType: []string{"channel"}}, 2, "News"},
		{"chat_type_supergroup", filters.Options{ChatType: []string{"supergroup"}}, 1, "Giga"},
		{"chat_type_bot", filters.Options{ChatType: []string{"bot"}}, 1, "Robot"},
		{"archived_only", filters.Options{Archived: "only"}, 1, "Robot"},
		{"archived_exclude", filters.Options{Archived: "exclude"}, 6, "Alice"},
		{"glob", filters.Options{ChatGlob: "N*"}, 1, "News"},
		{"regex", filters.Options{ChatRegex: "^Forum"}, 1, "Forum Home"},
		{"username", filters.Options{ChatUsername: "alice"}, 1, "Alice"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resolver := newResolver(fakeScopeAPI{}, sampleDialogs())

			targets, err := resolver.Resolve(t.Context(), []string{"all"}, tc.opts)
			require.NoError(t, err)
			require.Len(t, targets, tc.count)

			assert.Equal(t, tc.title, targets[0].Chat.Title)
		})
	}
}

func TestResolveContactsOnly(t *testing.T) {
	t.Parallel()

	api := fakeScopeAPI{contacts: &tg.ContactsContacts{Users: []tg.UserClass{
		&tg.User{ID: 10, FirstName: "Alice"},
	}}}
	resolver := newResolver(api, sampleDialogs())

	targets, err := resolver.Resolve(t.Context(), []string{"all"}, filters.Options{ContactsOnly: true})
	require.NoError(t, err)
	require.Len(t, targets, 1)

	assert.Equal(t, "Alice", targets[0].Chat.Title)
}

func TestResolveMixedSpecsDedupe(t *testing.T) {
	t.Parallel()

	api := fakeScopeAPI{resolved: map[string]*tg.ContactsResolvedPeer{
		"alice": {
			Peer:  &tg.PeerUser{UserID: 10},
			Users: []tg.UserClass{&tg.User{ID: 10, FirstName: "Alice", Username: "alice", AccessHash: 100}},
		},
	}}
	resolver := newResolver(api, sampleDialogs())

	targets, err := resolver.Resolve(t.Context(), []string{"saved", "@alice", "alice", "30"}, filters.Options{})
	require.NoError(t, err)
	require.Len(t, targets, 3)

	assert.True(t, targets[0].Chat.Saved)
	assert.Equal(t, "Alice", targets[1].Chat.Title)
	assert.Equal(t, "News", targets[2].Chat.Title)
}

func TestResolveEmptySpecs(t *testing.T) {
	t.Parallel()

	resolver := newResolver(fakeScopeAPI{}, sampleDialogs())

	targets, err := resolver.Resolve(t.Context(), nil, filters.Options{})
	require.NoError(t, err)

	assert.Empty(t, targets)
}

func TestResolveEmptySpec(t *testing.T) {
	t.Parallel()

	resolver := newResolver(fakeScopeAPI{}, sampleDialogs())

	_, err := resolver.Resolve(t.Context(), []string{""}, filters.Options{})
	require.ErrorIs(t, err, scan.ErrUnknownChat)
}
