package tlmock_test

import (
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/testutil/tlmock"

	"github.com/gotd/td/bin"
	tg "github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// docID asserts the gotd-generated constructor id still matches the id
// cited in the fixture godoc, so a schema bump cannot pass silently.
func docID(t *testing.T, object bin.Object, want uint32) {
	t.Helper()

	var wire bin.Buffer

	require.NoError(t, object.Encode(&wire))

	id, err := wire.PeekID()
	require.NoError(t, err)
	assert.Equal(t, want, id, "%T constructor id drifted from the documented layer", object)
}

// wireOf encodes object, proving the fixture survives the real TL encoder.
func wirePtr(t *testing.T, object bin.Object) *bin.Buffer {
	t.Helper()

	wire := wireOf(t, object)

	return &wire
}

func wireOf(t *testing.T, object bin.Object) bin.Buffer {
	t.Helper()

	var wire bin.Buffer

	require.NoError(t, object.Encode(&wire))

	return wire
}

func TestDialogFixturesWireFidelity(t *testing.T) {
	t.Parallel()

	page := tlmock.DialogPage([]tg.DialogClass{
		tlmock.Dialog(&tg.PeerUser{UserID: 10}, 42, 1),
	}, []tg.MessageClass{tlmock.Msg(42, "top")}, []tg.UserClass{tlmock.User(10, "Alice")}, nil)

	docID(t, page, 0x15ba6c40)

	slice := tlmock.DialogSlicePage(3, []tg.DialogClass{
		tlmock.Dialog(&tg.PeerChat{ChatID: 20}, 7, 0),
	}, nil, nil, []tg.ChatClass{tlmock.ChatSmall(20, "Group")})

	docID(t, slice, 0x71e094f3)

	dialog := &tg.Dialog{}
	require.NoError(t, dialog.Decode(wirePtr(t,
		tlmock.Dialog(&tg.PeerChannel{ChannelID: 31}, 9, 1))))

	assert.Equal(t, 9, dialog.TopMessage)
	assert.Equal(t, 1, dialog.FolderID)

	peer, ok := dialog.Peer.(*tg.PeerChannel)
	require.True(t, ok)
	assert.Equal(t, int64(31), peer.ChannelID)
}

func TestMessageFixturesWireFidelity(t *testing.T) {
	t.Parallel()

	msg := tlmock.Msg(5, "caption")
	docID(t, msg, 0x7600b9d3)

	videoDoc := tlmock.Document(77, "video/mp4", 1024,
		tlmock.NamedFileAttr("clip.mp4"), tlmock.VideoAttr(1920, 1080, 42.5,
			tlmock.RoundMessage, tlmock.Nosound, tlmock.Streaming))
	docID(t, videoDoc, 0x8fd4c4d8)

	decodedDoc := &tg.Document{}
	require.NoError(t, decodedDoc.Decode(wirePtr(t, videoDoc)))
	require.Len(t, decodedDoc.Attributes, 2)

	filename, ok := decodedDoc.Attributes[0].(*tg.DocumentAttributeFilename)
	require.True(t, ok)
	assert.Equal(t, "clip.mp4", filename.FileName)

	video, ok := decodedDoc.Attributes[1].(*tg.DocumentAttributeVideo)
	require.True(t, ok)
	assert.True(t, video.RoundMessage)
	assert.True(t, video.Nosound)
	assert.True(t, video.SupportsStreaming)
	assert.Equal(t, 1920, video.W)

	audioDoc := tlmock.Document(78, "audio/ogg", 2048, tlmock.AudioAttr(30, tlmock.Voice))
	decodedAudio := &tg.Document{}
	require.NoError(t, decodedAudio.Decode(wirePtr(t, audioDoc)))
	require.Len(t, decodedAudio.Attributes, 1)

	audio, ok := decodedAudio.Attributes[0].(*tg.DocumentAttributeAudio)
	require.True(t, ok)
	assert.True(t, audio.Voice)
	assert.Equal(t, 30, audio.Duration)

	stickerDoc := tlmock.Document(79, "image/webp", 512,
		tlmock.StickerAttr("🙂"), tlmock.AnimatedAttr())
	decodedSticker := &tg.Document{}
	require.NoError(t, decodedSticker.Decode(wirePtr(t, stickerDoc)))
	require.Len(t, decodedSticker.Attributes, 2)

	sticker, ok := decodedSticker.Attributes[0].(*tg.DocumentAttributeSticker)
	require.True(t, ok)
	assert.Equal(t, "🙂", sticker.Alt)

	_, animated := decodedSticker.Attributes[1].(*tg.DocumentAttributeAnimated)
	assert.True(t, animated)

	service := tlmock.ServiceMsg(6, &tg.MessageActionChatCreate{Title: "Group", Users: []int64{1}})
	docID(t, service, 0x7a800e0a)

	decodedService := &tg.MessageService{}
	require.NoError(t, decodedService.Decode(wirePtr(t, service)))
	assert.NotNil(t, decodedService.Action)
}

func TestPhotoFixturesWireFidelity(t *testing.T) {
	t.Parallel()

	photo := tlmock.Photo(55,
		tlmock.PhotoSize("m", 320, 240, 10240),
		tlmock.ProgressiveSize("x", 1280, 960, 2048, 4096))
	docID(t, photo, 0xfb197a65)

	fresh := &tg.Photo{}
	require.NoError(t, fresh.Decode(wirePtr(t, photo)))
	require.Len(t, fresh.Sizes, 2)

	preview, ok := fresh.Sizes[0].(*tg.PhotoSize)
	require.True(t, ok)
	assert.Equal(t, "m", preview.Type)
	assert.Equal(t, 10240, preview.Size)

	full, ok := fresh.Sizes[1].(*tg.PhotoSizeProgressive)
	require.True(t, ok)
	assert.Equal(t, []int{2048, 4096}, full.Sizes)
}

func TestPeerFixturesWireFidelity(t *testing.T) {
	t.Parallel()

	user := tlmock.User(10, "Alice",
		tlmock.WithUsername("alice"), tlmock.WithAccessHash(100),
		tlmock.WithBot, tlmock.WithContact, tlmock.WithMutual,
		tlmock.WithPremium, tlmock.WithVerified, tlmock.WithScam, tlmock.WithDeleted)
	docID(t, user, 0xb1b8cc83)

	freshUser := &tg.User{}
	require.NoError(t, freshUser.Decode(wirePtr(t, user)))
	assert.True(t, freshUser.Bot && freshUser.Contact && freshUser.MutualContact)
	assert.True(t, freshUser.Premium && freshUser.Verified && freshUser.Scam && freshUser.Deleted)
	assert.Equal(t, "alice", freshUser.Username)
	assert.Equal(t, int64(100), freshUser.AccessHash)

	channel := tlmock.Channel(31, "Forum Home", tlmock.WithMegagroup,
		tlmock.WithForum, tlmock.WithChannelVerified, tlmock.WithNoforwards,
		tlmock.WithChannelUsername("forum"), tlmock.WithChannelAccessHash(301))
	docID(t, channel, 0xd49f34c6)

	freshChannel := &tg.Channel{}
	require.NoError(t, freshChannel.Decode(wirePtr(t, channel)))
	assert.True(t, freshChannel.Megagroup && freshChannel.Forum)
	assert.True(t, freshChannel.Verified && freshChannel.Noforwards)
	assert.Equal(t, "forum", freshChannel.Username)

	broadcast := tlmock.Channel(30, "News", tlmock.WithBroadcast, tlmock.WithGigagroup)
	freshBroadcast := &tg.Channel{}
	require.NoError(t, freshBroadcast.Decode(wirePtr(t, broadcast)))
	assert.True(t, freshBroadcast.Broadcast && freshBroadcast.Gigagroup)

	group := tlmock.ChatSmall(20, "Locked", tlmock.WithChatNoforwards)
	docID(t, group, 0x41cbf256)

	freshGroup := &tg.Chat{}
	require.NoError(t, freshGroup.Decode(wirePtr(t, group)))
	assert.True(t, freshGroup.Noforwards)
}

func TestHistoryAndTopicFixturesWireFidelity(t *testing.T) {
	t.Parallel()

	history := tlmock.HistoryPage([]tg.MessageClass{tlmock.Msg(1, "hi")}, nil, nil)
	docID(t, history, 0x1d73e7ea)

	channelHistory := tlmock.ChannelHistoryPage(9, []tg.MessageClass{tlmock.Msg(2, "hey")}, nil, nil)
	docID(t, channelHistory, 0xc776ba4e)

	freshHistory := &tg.MessagesChannelMessages{}
	require.NoError(t, freshHistory.Decode(wirePtr(t, channelHistory)))
	assert.Equal(t, 9, freshHistory.Count)

	topics := tlmock.Topics(2, tlmock.Topic(7, "Ideas"), tlmock.Topic(9, "Bugs"))
	docID(t, topics, 0x367617d3)

	topic := tlmock.Topic(7, "Ideas")
	docID(t, topic, 0xfcdad815)

	freshTopic := &tg.ForumTopic{}
	require.NoError(t, freshTopic.Decode(wirePtr(t, topic)))
	assert.Equal(t, "Ideas", freshTopic.Title)
	assert.Equal(t, 7, freshTopic.ID)
}

func TestResolverAndFileFixturesWireFidelity(t *testing.T) {
	t.Parallel()

	resolved := tlmock.ResolvedPeer(&tg.PeerUser{UserID: 10},
		[]tg.UserClass{tlmock.User(10, "Alice")}, nil)
	docID(t, resolved, 0x7f077ad9)

	freshResolved := &tg.ContactsResolvedPeer{}
	require.NoError(t, freshResolved.Decode(wirePtr(t, resolved)))
	require.Len(t, freshResolved.Users, 1)

	contacts := tlmock.ContactsList(tlmock.User(10, "Alice"), tlmock.User(11, "Bob"))
	docID(t, contacts, 0xeae87e42)

	freshContacts := &tg.ContactsContacts{}
	require.NoError(t, freshContacts.Decode(wirePtr(t, contacts)))
	assert.Len(t, freshContacts.Users, 2)

	chunk := tlmock.FileChunk(1700000500, []byte("partial-bytes"))
	docID(t, chunk, 0x96a18d5)

	freshChunk := &tg.UploadFile{}
	require.NoError(t, freshChunk.Decode(wirePtr(t, chunk)))
	assert.Equal(t, []byte("partial-bytes"), freshChunk.Bytes)
}

func TestErrorMakersMatchDocumentedCodes(t *testing.T) {
	t.Parallel()

	wait, ok := tgerr.AsFloodWait(tlmock.FloodWait(30))
	require.True(t, ok)
	assert.Equal(t, 30*time.Second, wait)

	private, ok := tgerr.As(tlmock.ChannelPrivate())
	require.True(t, ok)
	assert.Equal(t, 400, private.Code)
	assert.Equal(t, "CHANNEL_PRIVATE", private.Type)

	expired, ok := tgerr.As(tlmock.FileRefExpired())
	require.True(t, ok)
	assert.Equal(t, 400, expired.Code)
	assert.Equal(t, "FILE_REFERENCE_EXPIRED", expired.Type)
}
