package scan_test

import (
	"testing"

	"github.com/4q4r/teleparse/internal/filters"
	"github.com/4q4r/teleparse/internal/scan"
	"github.com/4q4r/teleparse/internal/testutil/tlmock"

	"github.com/gotd/td/telegram/message/peer"
	tg "github.com/gotd/td/tg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testChat() filters.Chat {
	return filters.Chat{ID: 100, Type: "private", Title: "Test"}
}

func docWith(attrs ...tg.DocumentAttributeClass) *tg.Document {
	return &tg.Document{ID: 55, MimeType: "application/octet-stream", Size: 4096, Attributes: attrs}
}

func requireFile(t *testing.T, ctx filters.Context) *filters.FileInfo {
	t.Helper()

	require.NotNil(t, ctx.File)
	require.True(t, ctx.File.Present)

	return ctx.File
}

func TestMapMessagePlainText(t *testing.T) {
	t.Parallel()

	msg := &tg.Message{ID: 7, Date: 1700000000, Message: "hello world"}
	ctx := scan.MapMessage(msg, testChat(), filters.Sender{})

	assert.Equal(t, "hello world", ctx.Message.Text)
	assert.Empty(t, ctx.Message.Caption)
	assert.Nil(t, ctx.File)
	assert.Equal(t, int64(7), ctx.Message.ID)
	assert.Equal(t, int64(1700000000), ctx.Message.Date)
	assert.False(t, ctx.Message.Service)
	assert.Equal(t, "private", ctx.Chat.Type)
}

func TestMapMessageCaptionForMedia(t *testing.T) {
	t.Parallel()

	msg := &tg.Message{ID: 8, Message: "the caption", Media: &tg.MessageMediaPhoto{Photo: &tg.Photo{}}}
	ctx := scan.MapMessage(msg, testChat(), filters.Sender{})

	assert.Empty(t, ctx.Message.Text)
	assert.Equal(t, "the caption", ctx.Message.Caption)
	require.NotNil(t, ctx.File)
}

func TestMapMessageEntities(t *testing.T) {
	t.Parallel()

	msg := &tg.Message{
		ID:      9,
		Message: "see https://x.dev by @bob /start #tag a@b.io +1555 spam [link] none",
		Entities: []tg.MessageEntityClass{
			&tg.MessageEntityURL{Offset: 4, Length: 13},
			&tg.MessageEntityMention{Offset: 21, Length: 4},
			&tg.MessageEntityBotCommand{Offset: 26, Length: 6},
			&tg.MessageEntityHashtag{Offset: 33, Length: 4},
			&tg.MessageEntityEmail{Offset: 38, Length: 6},
			&tg.MessageEntityPhone{Offset: 45, Length: 5},
			&tg.MessageEntitySpoiler{Offset: 51, Length: 4},
			&tg.MessageEntityCode{Offset: 56, Length: 2},
			&tg.MessageEntityPre{Offset: 58, Length: 3, Language: "go"},
			&tg.MessageEntityBlockquote{Offset: 63, Length: 4},
			&tg.MessageEntityTextURL{Offset: 0, Length: 3, URL: "https://hidden.dev"},
			&tg.MessageEntityMentionName{Offset: 4, Length: 4, UserID: 42},
			&tg.MessageEntityCustomEmoji{Offset: 21, Length: 3, DocumentID: 777},
			&tg.MessageEntityBold{Offset: 33, Length: 2},
		},
	}

	ctx := scan.MapMessage(msg, testChat(), filters.Sender{})
	entities := ctx.Message.Entities

	require.Len(t, entities, 13)

	expect := []filters.Entity{
		{Kind: "url", Text: "https://x.dev"},
		{Kind: "mention", Text: "@bob"},
		{Kind: "bot_command", Text: "/start"},
		{Kind: "hashtag", Text: "#tag"},
		{Kind: "email", Text: "a@b.io"},
		{Kind: "phone", Text: "+1555"},
		{Kind: "spoiler", Text: "spam"},
		{Kind: "code", Text: "[l"},
		{Kind: "pre", Text: "ink"},
		{Kind: "quote", Text: "none"},
		{Kind: "text_link", Text: "see", Data: "https://hidden.dev"},
		{Kind: "text_mention", Text: "http", Data: "42"},
		{Kind: "custom_emoji", Text: "@bo", Data: "777"},
	}

	for idx, want := range expect {
		assert.Equal(t, want.Kind, entities[idx].Kind, "entity %d kind", idx)
		assert.Equal(t, want.Text, entities[idx].Text, "entity %d text", idx)
		assert.Equal(t, want.Data, entities[idx].Data, "entity %d data", idx)
	}
}

func TestMapMessageEntitySliceClamped(t *testing.T) {
	t.Parallel()

	msg := &tg.Message{
		ID:       10,
		Message:  "short",
		Entities: []tg.MessageEntityClass{&tg.MessageEntityURL{Offset: 2, Length: 100}},
	}

	ctx := scan.MapMessage(msg, testChat(), filters.Sender{})

	require.Len(t, ctx.Message.Entities, 1)
	assert.Equal(t, "ort", ctx.Message.Entities[0].Text)
}

func TestMapMessageFlagsAndCounts(t *testing.T) {
	t.Parallel()

	msg := &tg.Message{
		ID: 11, Out: true, Pinned: true, Silent: true, Mentioned: true,
		Date: 1700000100, GroupedID: 33,
	}
	msg.SetEditDate(1700000200)
	msg.SetViews(500)
	msg.SetForwards(12)
	msg.SetTTLPeriod(3600)

	ctx := scan.MapMessage(msg, testChat(), filters.Sender{})

	assert.True(t, ctx.Message.Out)
	assert.True(t, ctx.Message.Pinned)
	assert.True(t, ctx.Message.Silent)
	assert.True(t, ctx.Message.Mentioned)
	assert.True(t, ctx.Message.HasEdit)
	assert.Equal(t, int64(1700000200), ctx.Message.EditDate)
	assert.Equal(t, int64(33), ctx.Message.GroupedID)
	assert.Equal(t, int64(500), ctx.Message.Views)
	assert.Equal(t, int64(12), ctx.Message.Forwards)
	assert.Equal(t, 3600, ctx.Message.TTLSeconds)
}

func TestMapMessageForwardVariants(t *testing.T) {
	t.Parallel()

	fromUserHeader := tg.MessageFwdHeader{Date: 1699990000}
	fromUserHeader.SetFromID(&tg.PeerUser{UserID: 10})

	fromUser := &tg.Message{ID: 12}
	fromUser.SetFwdFrom(fromUserHeader)
	ctx := scan.MapMessage(fromUser, testChat(), filters.Sender{})

	require.NotNil(t, ctx.Message.Forward)
	assert.True(t, ctx.Message.Forward.Present)
	assert.Equal(t, int64(10), ctx.Message.Forward.FromID)
	assert.Equal(t, int64(1699990000), ctx.Message.Forward.Date)
	assert.False(t, ctx.Message.Forward.Hidden)

	fromChannelHeader := tg.MessageFwdHeader{Date: 1699990001}
	fromChannelHeader.SetFromID(&tg.PeerChannel{ChannelID: 20})

	fromChannel := &tg.Message{ID: 13}
	fromChannel.SetFwdFrom(fromChannelHeader)
	ctx = scan.MapMessage(fromChannel, testChat(), filters.Sender{})
	require.NotNil(t, ctx.Message.Forward)
	assert.Equal(t, int64(20), ctx.Message.Forward.FromID)

	hiddenHeader := tg.MessageFwdHeader{Date: 1699990002}
	hiddenHeader.SetFromName("Hidden One")

	hidden := &tg.Message{ID: 14}
	hidden.SetFwdFrom(hiddenHeader)
	ctx = scan.MapMessage(hidden, testChat(), filters.Sender{})

	require.NotNil(t, ctx.Message.Forward)
	assert.True(t, ctx.Message.Forward.Present)
	assert.True(t, ctx.Message.Forward.Hidden)
	assert.Equal(t, "Hidden One", ctx.Message.Forward.FromName)
	assert.Equal(t, int64(0), ctx.Message.Forward.FromID)

	plain := &tg.Message{ID: 15}
	ctx = scan.MapMessage(plain, testChat(), filters.Sender{})

	assert.Nil(t, ctx.Message.Forward)
}

func TestMapMessageReply(t *testing.T) {
	t.Parallel()

	msg := &tg.Message{ID: 16}
	msg.SetReplyTo(&tg.MessageReplyHeader{ReplyToMsgID: 55})
	ctx := scan.MapMessage(msg, testChat(), filters.Sender{})

	require.NotNil(t, ctx.Message.Reply)
	assert.True(t, ctx.Message.Reply.Present)
	assert.Equal(t, int64(55), ctx.Message.Reply.ToMsgID)

	ctx = scan.MapMessage(&tg.Message{ID: 17}, testChat(), filters.Sender{})

	assert.Nil(t, ctx.Message.Reply)
}

func TestMapMessageReactions(t *testing.T) {
	t.Parallel()

	msg := &tg.Message{ID: 18}
	msg.SetReactions(tg.MessageReactions{
		Results: []tg.ReactionCount{
			{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 3},
			{Reaction: &tg.ReactionCustomEmoji{DocumentID: 9}, Count: 2},
		},
	})

	ctx := scan.MapMessage(msg, testChat(), filters.Sender{})

	require.Len(t, ctx.Message.Reactions, 2)
	assert.Equal(t, "👍", ctx.Message.Reactions[0].Emoji)
	assert.Equal(t, 3, ctx.Message.Reactions[0].Count)
	assert.Empty(t, ctx.Message.Reactions[1].Emoji)
	assert.Equal(t, 5, ctx.Message.TotalReactions)
}

func TestMapMessagePhotoSizes(t *testing.T) {
	t.Parallel()

	photo := &tg.MessageMediaPhoto{
		Photo: &tg.Photo{Sizes: []tg.PhotoSizeClass{
			&tg.PhotoStrippedSize{Type: "i"},
			&tg.PhotoSize{Type: "m", W: 320, H: 240, Size: 22000},
			&tg.PhotoSizeProgressive{Type: "x", W: 1280, H: 720, Sizes: []int{8000, 90000}},
		}},
	}

	ctx := scan.MapMessage(&tg.Message{ID: 19, Media: photo}, testChat(), filters.Sender{})
	file := requireFile(t, ctx)

	assert.Equal(t, "photo", file.Kind)
	assert.Equal(t, "image/jpeg", file.Mime)
	assert.Equal(t, 1280, file.Width)
	assert.Equal(t, 720, file.Height)
	assert.Equal(t, int64(90000), file.Size)
	assert.False(t, ctx.Message.Spoiler)

	spoilerPhoto := &tg.MessageMediaPhoto{Photo: &tg.Photo{}, Spoiler: true}
	ctx = scan.MapMessage(&tg.Message{ID: 20, Media: spoilerPhoto}, testChat(), filters.Sender{})

	assert.True(t, ctx.Message.Spoiler)
}

func TestMapMessageVideo(t *testing.T) {
	t.Parallel()

	document := docWith(
		&tg.DocumentAttributeFilename{FileName: "clip.mp4"},
		&tg.DocumentAttributeVideo{Duration: 12.7, W: 1920, H: 1080, SupportsStreaming: true},
	)
	document.MimeType = "video/mp4"

	ctx := scan.MapMessage(&tg.Message{ID: 21, Media: &tg.MessageMediaDocument{Document: document}}, testChat(), filters.Sender{})
	file := requireFile(t, ctx)

	assert.Equal(t, "video", file.Kind)
	assert.Equal(t, "video/mp4", file.Mime)
	assert.Equal(t, 12, file.Duration)
	assert.Equal(t, 1920, file.Width)
	assert.Equal(t, 1080, file.Height)
	assert.True(t, file.Streamable)
	assert.Equal(t, "clip.mp4", file.Name)
	assert.Equal(t, ".mp4", file.Ext)
	assert.Equal(t, int64(4096), file.Size)
}

func TestMapMessageRoundVideo(t *testing.T) {
	t.Parallel()

	document := docWith(&tg.DocumentAttributeVideo{RoundMessage: true, Duration: 30, W: 400, H: 400})

	ctx := scan.MapMessage(&tg.Message{ID: 22, Media: &tg.MessageMediaDocument{Document: document}}, testChat(), filters.Sender{})
	file := requireFile(t, ctx)

	assert.Equal(t, "video-note", file.Kind)
	assert.False(t, file.Streamable)
}

func TestMapMessageMutedVideo(t *testing.T) {
	t.Parallel()

	document := docWith(&tg.DocumentAttributeVideo{Nosound: true, Duration: 5, W: 640, H: 360})

	ctx := scan.MapMessage(&tg.Message{ID: 23, Media: &tg.MessageMediaDocument{Document: document}}, testChat(), filters.Sender{})

	assert.True(t, requireFile(t, ctx).NoSound)
}

func TestMapMessageVoiceAndAudio(t *testing.T) {
	t.Parallel()

	voice := docWith(&tg.DocumentAttributeAudio{Voice: true, Duration: 7, Waveform: []byte{1, 2, 3}})

	ctx := scan.MapMessage(&tg.Message{ID: 24, Media: &tg.MessageMediaDocument{Document: voice}}, testChat(), filters.Sender{})
	file := requireFile(t, ctx)

	assert.Equal(t, "voice", file.Kind)
	assert.Equal(t, 7, file.Duration)
	assert.True(t, file.Waveform)

	audio := docWith(&tg.DocumentAttributeAudio{Duration: 200, Title: "Song", Performer: "Artist"})

	ctx = scan.MapMessage(&tg.Message{ID: 25, Media: &tg.MessageMediaDocument{Document: audio}}, testChat(), filters.Sender{})
	file = requireFile(t, ctx)

	assert.Equal(t, "audio", file.Kind)
	assert.Equal(t, 200, file.Duration)
	assert.Equal(t, "Song", file.Name)
	assert.False(t, file.Waveform)

	named := docWith(
		&tg.DocumentAttributeFilename{FileName: "track.mp3"},
		&tg.DocumentAttributeAudio{Duration: 100, Title: "Ignored"},
	)

	ctx = scan.MapMessage(&tg.Message{ID: 26, Media: &tg.MessageMediaDocument{Document: named}}, testChat(), filters.Sender{})
	file = requireFile(t, ctx)

	assert.Equal(t, "audio", file.Kind)
	assert.Equal(t, "track.mp3", file.Name)
}

func TestMapMessageStickerKinds(t *testing.T) {
	t.Parallel()

	sticker := docWith(&tg.DocumentAttributeSticker{Alt: "🔥"})
	ctx := scan.MapMessage(&tg.Message{ID: 27, Media: &tg.MessageMediaDocument{Document: sticker}}, testChat(), filters.Sender{})
	file := requireFile(t, ctx)

	assert.Equal(t, "sticker", file.Kind)
	assert.Equal(t, "static", file.StickerKind)
	assert.Equal(t, "🔥", file.Name)

	animated := docWith(
		&tg.DocumentAttributeSticker{Alt: "🎉"},
		&tg.DocumentAttributeAnimated{},
	)
	ctx = scan.MapMessage(&tg.Message{ID: 28, Media: &tg.MessageMediaDocument{Document: animated}}, testChat(), filters.Sender{})

	assert.Equal(t, "animated", requireFile(t, ctx).StickerKind)

	videoSticker := docWith(
		&tg.DocumentAttributeSticker{Alt: "🙂"},
		&tg.DocumentAttributeVideo{Duration: 0},
	)
	ctx = scan.MapMessage(&tg.Message{ID: 29, Media: &tg.MessageMediaDocument{Document: videoSticker}}, testChat(), filters.Sender{})

	assert.Equal(t, "video", requireFile(t, ctx).StickerKind)
}

func TestMapMessageGifAndDocument(t *testing.T) {
	t.Parallel()

	gif := docWith(&tg.DocumentAttributeFilename{FileName: "cat.gif"}, &tg.DocumentAttributeAnimated{})
	gif.MimeType = "video/mp4"

	ctx := scan.MapMessage(&tg.Message{ID: 30, Media: &tg.MessageMediaDocument{Document: gif}}, testChat(), filters.Sender{})

	assert.Equal(t, "gif", requireFile(t, ctx).Kind)

	report := docWith(&tg.DocumentAttributeFilename{FileName: "report.PDF"}, &tg.DocumentAttributeImageSize{W: 600, H: 800})
	report.MimeType = "application/pdf"

	ctx = scan.MapMessage(&tg.Message{ID: 31, Media: &tg.MessageMediaDocument{Document: report}}, testChat(), filters.Sender{})
	file := requireFile(t, ctx)

	assert.Equal(t, "document", file.Kind)
	assert.Equal(t, "application/pdf", file.Mime)
	assert.Equal(t, "report.PDF", file.Name)
	assert.Equal(t, ".pdf", file.Ext)
	assert.Equal(t, 600, file.Width)
	assert.Equal(t, 800, file.Height)
}

func TestMapMessageMediaKinds(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		media tg.MessageMediaClass
		kind  string
	}{
		{"webpage", &tg.MessageMediaWebPage{}, "webpage"},
		{"geo", &tg.MessageMediaGeo{}, "geo"},
		{"geolive", &tg.MessageMediaGeoLive{}, "geo"},
		{"venue", &tg.MessageMediaVenue{}, "geo"},
		{"contact", &tg.MessageMediaContact{}, "contact"},
		{"poll", &tg.MessageMediaPoll{}, "poll"},
		{"dice", &tg.MessageMediaDice{}, "dice"},
		{"game", &tg.MessageMediaGame{}, "game"},
		{"invoice", &tg.MessageMediaInvoice{}, "invoice"},
		{"story", &tg.MessageMediaStory{}, "story"},
		{"paid", &tg.MessageMediaPaidMedia{}, "paid"},
		{"unsupported", &tg.MessageMediaUnsupported{}, "unsupported"},
		{"giveaway", &tg.MessageMediaGiveaway{}, "unsupported"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := scan.MapMessage(&tg.Message{ID: 32, Media: tc.media}, testChat(), filters.Sender{})
			file := requireFile(t, ctx)

			assert.Equal(t, tc.kind, file.Kind)
		})
	}
}

func TestMapMessageMediaEmptyAndNil(t *testing.T) {
	t.Parallel()

	for _, media := range []tg.MessageMediaClass{nil, &tg.MessageMediaEmpty{}} {
		ctx := scan.MapMessage(&tg.Message{ID: 33, Message: "plain", Media: media}, testChat(), filters.Sender{})

		assert.Nil(t, ctx.File)
		assert.Equal(t, "plain", ctx.Message.Text)
	}
}

func TestMapMessageDocumentSpoiler(t *testing.T) {
	t.Parallel()

	ctx := scan.MapMessage(&tg.Message{
		ID:    34,
		Media: &tg.MessageMediaDocument{Document: docWith(), Spoiler: true},
	}, testChat(), filters.Sender{})

	assert.True(t, ctx.Message.Spoiler)
}

func TestMapService(t *testing.T) {
	t.Parallel()

	service := &tg.MessageService{
		ID: 35, Date: 1700000300, Out: true, Silent: true, Mentioned: true,
		Action: &tg.MessageActionChatCreate{Title: "Group"},
	}

	ctx := scan.MapService(service, testChat(), filters.Sender{})

	assert.True(t, ctx.Message.Service)
	assert.Equal(t, int64(35), ctx.Message.ID)
	assert.Equal(t, int64(1700000300), ctx.Message.Date)
	assert.True(t, ctx.Message.Out)
	assert.True(t, ctx.Message.Silent)
	assert.True(t, ctx.Message.Mentioned)
	assert.Nil(t, ctx.File)
	assert.Empty(t, ctx.Message.Text)
}

func TestMapDialogChatDeletedUser(t *testing.T) {
	t.Parallel()

	entities := peer.NewEntities(map[int64]*tg.User{
		12: tlmock.User(12, "Ghost", tlmock.WithDeleted),
		10: tlmock.User(10, "Alice"),
	}, nil, nil)

	dialog := &tg.Dialog{Peer: &tg.PeerUser{UserID: 12}}
	chat, ok := scan.MapDialogChat(dialog, entities)
	require.True(t, ok)

	assert.Equal(t, "private", chat.Type)
	assert.True(t, chat.Deleted)

	liveDialog := &tg.Dialog{Peer: &tg.PeerUser{UserID: 10}}
	liveChat, ok := scan.MapDialogChat(liveDialog, entities)
	require.True(t, ok)

	assert.Equal(t, "private", liveChat.Type)
	assert.False(t, liveChat.Deleted)
}
