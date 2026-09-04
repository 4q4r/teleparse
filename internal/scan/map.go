package scan

import (
	"path"
	"strconv"
	"strings"
	"teleparse/internal/filters"

	"github.com/gotd/td/telegram/message/peer"
	tg "github.com/gotd/td/tg"
)

// Chat type vocabulary, mirroring the filters context model.
const (
	chatPrivate    = "private"
	chatBot        = "bot"
	chatGroup      = "group"
	chatSupergroup = "supergroup"
	chatChannel    = "channel"
	chatForum      = "forum"
)

// MapMessage projects one gotd message onto the pure filters Context. Sender
// resolution is the caller's job; the passed Sender is copied verbatim, so an
// absent Sender (Present=false) only matches predicates left unset.
func MapMessage(msg *tg.Message, chat filters.Chat, sender filters.Sender) filters.Context {
	ctx := filters.Context{Chat: chat, Sender: sender}

	ctx.Message.ID = int64(msg.ID)
	ctx.Message.Out = msg.Out
	ctx.Message.Pinned = msg.Pinned
	ctx.Message.Silent = msg.Silent
	ctx.Message.Mentioned = msg.Mentioned
	ctx.Message.GroupedID = msg.GroupedID
	ctx.Message.Date = int64(msg.Date)
	ctx.Message.Spoiler = mediaSpoiler(msg.Media)

	if editDate, ok := msg.GetEditDate(); ok {
		ctx.Message.EditDate = int64(editDate)
		ctx.Message.HasEdit = true
	}

	if ttl, ok := msg.GetTTLPeriod(); ok {
		ctx.Message.TTLSeconds = ttl
	}

	if views, ok := msg.GetViews(); ok {
		ctx.Message.Views = int64(views)
	}

	if forwards, ok := msg.GetForwards(); ok {
		ctx.Message.Forwards = int64(forwards)
	}

	// Telegram stores media captions inside the message text field.
	if hasMedia(msg.Media) {
		ctx.Message.Caption = msg.Message
	} else {
		ctx.Message.Text = msg.Message
	}

	ctx.Message.Entities = mapEntities(msg.Message, msg.Entities)
	ctx.Message.Forward = mapForward(msg)
	ctx.Message.Reply = mapReply(msg)
	ctx.Message.Reactions, ctx.Message.TotalReactions = mapReactions(msg)
	ctx.File = mapMedia(msg.Media)

	return ctx
}

// MapService projects one service message onto a minimal filters Context with
// Service set; service messages carry no downloadable media.
func MapService(msg *tg.MessageService, chat filters.Chat, sender filters.Sender) filters.Context {
	ctx := filters.Context{Chat: chat, Sender: sender}

	ctx.Message.ID = int64(msg.ID)
	ctx.Message.Out = msg.Out
	ctx.Message.Silent = msg.Silent
	ctx.Message.Mentioned = msg.Mentioned
	ctx.Message.Date = int64(msg.Date)
	ctx.Message.Service = true
	ctx.Message.Reply = mapReply(msg)

	if ttl, ok := msg.GetTTLPeriod(); ok {
		ctx.Message.TTLSeconds = ttl
	}

	return ctx
}

// MapDialogChat maps one dialog plus its entity set onto a filters.Chat,
// mapping peer types honestly: users split into private/bot, chats into group,
// channels into forum (megagroup+forum), supergroup (megagroup, gigagroup) or
// channel (broadcast). The second return is false when entity data is missing.
func MapDialogChat(dialog *tg.Dialog, entities peer.Entities) (filters.Chat, bool) {
	var chat filters.Chat

	chat.Archived = dialog.FolderID == archivedFolderID

	switch peerType := dialog.Peer.(type) {
	case *tg.PeerUser:
		user, ok := entities.User(peerType.UserID)
		if !ok {
			return chat, false
		}

		chat.ID = user.ID
		chat.Title = strings.TrimSpace(user.FirstName + " " + user.LastName)
		chat.Username = user.Username
		chat.Deleted = user.Deleted

		if user.Bot {
			chat.Type = chatBot
		} else {
			chat.Type = chatPrivate
		}
	case *tg.PeerChat:
		group, ok := entities.Chat(peerType.ChatID)
		if !ok {
			return chat, false
		}

		return dialogChat(group.ID, group.Title, "", group.Noforwards, chatGroup, chat.Archived), true
	case *tg.PeerChannel:
		channel, ok := entities.Channel(peerType.ChannelID)
		if !ok {
			return filters.Chat{Archived: chat.Archived}, false
		}

		return dialogChat(
			channel.ID, channel.Title, channel.Username, channel.Noforwards,
			classifyDialogChannel(channel), chat.Archived,
		), true
	default:
		return chat, false
	}

	return chat, true
}

func dialogChat(id int64, title, username string, protected bool, chatType string, archived bool) filters.Chat {
	return filters.Chat{
		ID:        id,
		Type:      chatType,
		Title:     title,
		Username:  username,
		Archived:  archived,
		Protected: protected,
	}
}

func classifyDialogChannel(channel *tg.Channel) string {
	switch {
	case channel.Forum:
		return chatForum
	case channel.Megagroup, channel.Gigagroup:
		return chatSupergroup
	default:
		return chatChannel
	}
}

// hasMedia reports whether the media attachment carries a projection.
func hasMedia(media tg.MessageMediaClass) bool {
	if media == nil {
		return false
	}

	_, empty := media.(*tg.MessageMediaEmpty)

	return !empty
}

// mediaSpoiler reads the spoiler flag off photo and document media.
func mediaSpoiler(media tg.MessageMediaClass) bool {
	switch typed := media.(type) {
	case *tg.MessageMediaPhoto:
		return typed.Spoiler
	case *tg.MessageMediaDocument:
		return typed.Spoiler
	default:
		return false
	}
}

func mapForward(msg *tg.Message) *filters.ForwardInfo {
	header, ok := msg.GetFwdFrom()
	if !ok {
		return nil
	}

	fromName, _ := header.GetFromName()

	info := &filters.ForwardInfo{Present: true, FromName: fromName, Date: int64(header.GetDate())}

	fromID, hasFrom := header.GetFromID()
	if hasFrom {
		info.FromID = peerID(fromID)
	}

	info.Hidden = fromName != "" && !hasFrom

	return info
}

func mapReply(msg tg.NotEmptyMessage) *filters.ReplyInfo {
	header, ok := msg.GetReplyTo()
	if !ok {
		return nil
	}

	concrete, ok := header.(*tg.MessageReplyHeader)
	if !ok || concrete.ReplyToMsgID <= 0 {
		return nil
	}

	return &filters.ReplyInfo{Present: true, ToMsgID: int64(concrete.ReplyToMsgID)}
}

func mapReactions(msg *tg.Message) ([]filters.Reaction, int) {
	reactions, ok := msg.GetReactions()
	if !ok || len(reactions.Results) == 0 {
		return nil, 0
	}

	mapped := make([]filters.Reaction, 0, len(reactions.Results))
	total := 0

	for _, result := range reactions.Results {
		mapped = append(mapped, filters.Reaction{Emoji: reactionEmoji(result.Reaction), Count: result.Count})
		total += result.Count
	}

	return mapped, total
}

func reactionEmoji(reaction tg.ReactionClass) string {
	if emoji, ok := reaction.(*tg.ReactionEmoji); ok {
		return emoji.Emoticon
	}

	return ""
}

// peerID extracts the numeric id from user, chat and channel peers.
func peerID(peer tg.PeerClass) int64 {
	switch typed := peer.(type) {
	case *tg.PeerUser:
		return typed.UserID
	case *tg.PeerChat:
		return typed.ChatID
	case *tg.PeerChannel:
		return typed.ChannelID
	default:
		return 0
	}
}

func mapEntities(text string, entities []tg.MessageEntityClass) []filters.Entity {
	if len(entities) == 0 {
		return nil
	}

	mapped := make([]filters.Entity, 0, len(entities))

	for _, entity := range entities {
		if kind, data, ok := entityKind(entity); ok {
			mapped = append(mapped, filters.Entity{
				Kind: kind,
				Text: entityText(text, entity),
				Data: data,
			})
		}
	}

	return mapped
}

func entityKind(entity tg.MessageEntityClass) (string, string, bool) {
	switch typed := entity.(type) {
	case *tg.MessageEntityURL:
		return "url", "", true
	case *tg.MessageEntityEmail:
		return "email", "", true
	case *tg.MessageEntityPhone:
		return "phone", "", true
	case *tg.MessageEntityMention:
		return "mention", "", true
	case *tg.MessageEntityHashtag:
		return "hashtag", "", true
	case *tg.MessageEntityBotCommand:
		return "bot_command", "", true
	case *tg.MessageEntitySpoiler:
		return "spoiler", "", true
	case *tg.MessageEntityCode:
		return "code", "", true
	case *tg.MessageEntityPre:
		return "pre", "", true
	case *tg.MessageEntityBlockquote:
		return "quote", "", true
	case *tg.MessageEntityTextURL:
		return "text_link", typed.URL, true
	case *tg.MessageEntityMentionName:
		return "text_mention", strconv.FormatInt(typed.UserID, 10), true
	case *tg.MessageEntityCustomEmoji:
		return "custom_emoji", strconv.FormatInt(typed.DocumentID, 10), true
	default:
		return "", "", false
	}
}

func entityText(text string, entity tg.MessageEntityClass) string {
	offset := entity.GetOffset()
	length := entity.GetLength()

	if offset < 0 || offset >= len(text) {
		return ""
	}

	end := offset + length
	if end > len(text) {
		end = len(text)
	}

	return text[offset:end]
}

func mapMedia(media tg.MessageMediaClass) *filters.FileInfo {
	switch typed := media.(type) {
	case nil, *tg.MessageMediaEmpty:
		return nil
	case *tg.MessageMediaPhoto:
		file := &filters.FileInfo{Present: true, Kind: "photo", Mime: photoMime}
		file.Width, file.Height, file.Size = photoDimensions(typed.Photo)

		return file
	case *tg.MessageMediaDocument:
		return mapDocument(typed.Document)
	case *tg.MessageMediaGeo, *tg.MessageMediaGeoLive, *tg.MessageMediaVenue:
		return &filters.FileInfo{Present: true, Kind: "geo"}
	case *tg.MessageMediaContact:
		return &filters.FileInfo{Present: true, Kind: "contact"}
	case *tg.MessageMediaPoll:
		return &filters.FileInfo{Present: true, Kind: "poll"}
	case *tg.MessageMediaDice:
		return &filters.FileInfo{Present: true, Kind: "dice"}
	case *tg.MessageMediaGame:
		return &filters.FileInfo{Present: true, Kind: "game"}
	case *tg.MessageMediaInvoice:
		return &filters.FileInfo{Present: true, Kind: "invoice"}
	case *tg.MessageMediaStory:
		return &filters.FileInfo{Present: true, Kind: "story"}
	case *tg.MessageMediaPaidMedia:
		return &filters.FileInfo{Present: true, Kind: "paid"}
	case *tg.MessageMediaWebPage:
		return &filters.FileInfo{Present: true, Kind: "webpage"}
	default:
		return &filters.FileInfo{Present: true, Kind: "unsupported"}
	}
}

func photoDimensions(photo tg.PhotoClass) (int, int, int64) {
	concrete, ok := photo.(*tg.Photo)
	if !ok {
		return 0, 0, 0
	}

	var (
		width  int
		height int
		size   int64
	)

	for _, variant := range concrete.Sizes {
		switch sized := variant.(type) {
		case *tg.PhotoSize:
			if int64(sized.Size) > size {
				width, height, size = sized.W, sized.H, int64(sized.Size)
			}
		case *tg.PhotoSizeProgressive:
			if largest := largestOf(sized.Sizes); int64(largest) > size {
				width, height, size = sized.W, sized.H, int64(largest)
			}
		}
	}

	return width, height, size
}

func largestOf(sizes []int) int {
	largest := 0

	for _, candidate := range sizes {
		if candidate > largest {
			largest = candidate
		}
	}

	return largest
}

func mapDocument(document tg.DocumentClass) *filters.FileInfo {
	concrete, ok := document.(*tg.Document)
	if !ok {
		return nil
	}

	file := &filters.FileInfo{Present: true, Mime: concrete.MimeType, Size: concrete.Size}

	var (
		name     string
		sticker  *tg.DocumentAttributeSticker
		audio    *tg.DocumentAttributeAudio
		video    *tg.DocumentAttributeVideo
		animated bool
	)

	for _, attribute := range concrete.Attributes {
		switch typed := attribute.(type) {
		case *tg.DocumentAttributeFilename:
			name = typed.FileName
		case *tg.DocumentAttributeImageSize:
			file.Width, file.Height = typed.W, typed.H
		case *tg.DocumentAttributeSticker:
			sticker = typed
		case *tg.DocumentAttributeAnimated:
			animated = true
		case *tg.DocumentAttributeAudio:
			audio = typed
		case *tg.DocumentAttributeVideo:
			video = typed
		default:
		}
	}

	switch {
	case sticker != nil:
		file.Kind = "sticker"
		file.StickerKind = stickerKind(animated, video != nil)
		file.Name = sticker.Alt
	case audio != nil:
		file.Kind = "audio"

		if audio.Voice {
			file.Kind = "voice"
		}

		file.Duration = audio.Duration
		file.Waveform = len(audio.Waveform) > 0
		file.Name = audioTitle(audio, name)
	case animated:
		file.Kind = "gif"
	case video != nil:
		file.Kind = "video"

		if video.RoundMessage {
			file.Kind = "video-note"
		}

		file.Duration = int(video.Duration)
		file.Width, file.Height = video.W, video.H
		file.Streamable = video.SupportsStreaming
		file.NoSound = video.Nosound
	default:
		file.Kind = "document"
	}

	if file.Name == "" {
		file.Name = name
	}

	file.Ext = strings.ToLower(path.Ext(file.Name))

	return file
}

func stickerKind(animated, video bool) string {
	switch {
	case animated:
		return "animated"
	case video:
		return "video"
	default:
		return "static"
	}
}

func audioTitle(audio *tg.DocumentAttributeAudio, filename string) string {
	if filename != "" {
		return filename
	}

	if audio.Title != "" {
		return audio.Title
	}

	return audio.Performer
}

// Photo files always arrive as JPEG, and folder id 1 marks archived dialogs.
const (
	photoMime        = "image/jpeg"
	archivedFolderID = 1
)
