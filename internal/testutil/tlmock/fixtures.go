package tlmock

import (
	"fmt"

	tg "github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// Fixture defaults chosen to stay stable and distinguishable in assertions.
const (
	// defaultDate is a fixed unix timestamp (2023-11-14T22:13:20Z).
	defaultDate = 1700000000
	// defaultDC mirrors gotd's primary production DC id.
	defaultDC = 2
	// defaultPeerID marks the chat every Msg fixture belongs to by default.
	defaultPeerID = 30
	// forumTopicPts is the pts stamped onto channel-message history pages.
	forumTopicPts = 7
	// selfUserID authors default service-message actions.
	selfUserID = 1
)

// Documented RPC error codes: 420 ratelimit, 400 bad request.
const (
	codeFloodWait   = 420
	codeBadRequest  = 400
	floodWaitFormat = "FLOOD_WAIT_%d"
	channelPrivate  = "CHANNEL_PRIVATE"
	fileRefExpired  = "FILE_REFERENCE_EXPIRED"
)

// DialogPage builds a messages.dialogs#15ba6c40 result: the final dialog
// page carrying every dialog with its entity set and top messages.
func DialogPage(
	dialogs []tg.DialogClass, messages []tg.MessageClass, users []tg.UserClass, chats []tg.ChatClass,
) *tg.MessagesDialogs {
	return &tg.MessagesDialogs{Dialogs: dialogs, Messages: messages, Users: users, Chats: chats}
}

// DialogSlicePage builds a messages.dialogsSlice#71e094f3 result: an
// incomplete dialog page; the gotd iterator keeps paginating until a page
// carries no dialogs at all or a plain messages.dialogs closes the stream.
func DialogSlicePage(
	count int, dialogs []tg.DialogClass, messages []tg.MessageClass,
	users []tg.UserClass, chats []tg.ChatClass,
) *tg.MessagesDialogsSlice {
	return &tg.MessagesDialogsSlice{
		Count: count, Dialogs: dialogs, Messages: messages, Users: users, Chats: chats,
	}
}

// Dialog builds dialog#fc89f7f3: folderID 1 marks the archived folder,
// 0 the inbox; topMessage must also appear in the page's messages for the
// gotd iterator to advance its offsets.
func Dialog(peer tg.PeerClass, topMessage, folderID int) *tg.Dialog {
	return &tg.Dialog{Peer: peer, TopMessage: topMessage, FolderID: folderID}
}

// MsgOption customizes a Msg or ServiceMsg fixture.
type MsgOption func(*tg.Message)

// WithPeer overrides the chat the message belongs to.
func WithPeer(peer tg.PeerClass) MsgOption {
	return func(msg *tg.Message) {
		msg.PeerID = peer
	}
}

// WithFrom sets the message author (message.from_id).
func WithFrom(userID int64) MsgOption {
	return func(msg *tg.Message) {
		msg.SetFromID(&tg.PeerUser{UserID: userID})
	}
}

// WithFromChannel marks the message as posted by the channel itself.
func WithFromChannel(channelID int64) MsgOption {
	return func(msg *tg.Message) {
		msg.SetFromID(&tg.PeerChannel{ChannelID: channelID})
	}
}

// WithDate overrides the fixed default message date.
func WithDate(date int) MsgOption {
	return func(msg *tg.Message) {
		msg.Date = date
	}
}

// WithGroupedID ties the message into an album (message.grouped_id).
func WithGroupedID(id int64) MsgOption {
	return func(msg *tg.Message) {
		msg.SetGroupedID(id)
	}
}

// WithDocument attaches messageMediaDocument#52d8ccd9 wrapping doc.
func WithDocument(doc *tg.Document) MsgOption {
	return func(msg *tg.Message) {
		msg.SetMedia(&tg.MessageMediaDocument{Document: doc})
	}
}

// WithPhoto attaches messageMediaPhoto#e216eb63 wrapping photo.
func WithPhoto(photo *tg.Photo) MsgOption {
	return func(msg *tg.Message) {
		msg.SetMedia(&tg.MessageMediaPhoto{Photo: photo})
	}
}

// Msg builds message#7600b9d3 with text in the message field, a stable date
// and a channel peer; Telegram stores media captions in the same field.
func Msg(id int, text string, opts ...MsgOption) *tg.Message {
	msg := &tg.Message{
		ID: id, Date: defaultDate, Message: text,
		PeerID: &tg.PeerChannel{ChannelID: defaultPeerID},
	}

	for _, opt := range opts {
		opt(msg)
	}

	return msg
}

// ServiceMsg builds messageService#7a800e0a carrying the given service
// action, authored by the default self user.
func ServiceMsg(id int, action tg.MessageActionClass) *tg.MessageService {
	msg := &tg.MessageService{
		ID: id, Date: defaultDate, Action: action,
		PeerID: &tg.PeerChannel{ChannelID: defaultPeerID},
	}
	msg.SetFromID(&tg.PeerUser{UserID: selfUserID})

	return msg
}

// Document builds document#8fd4c4d8 with a stable date and DC, the given
// mime type, size and attributes.
func Document(id int64, mime string, size int64, attrs ...tg.DocumentAttributeClass) *tg.Document {
	return &tg.Document{
		ID: id, Date: defaultDate, MimeType: mime, Size: size,
		DCID: defaultDC, Attributes: attrs,
	}
}

// VideoOption customizes a VideoAttr fixture.
type VideoOption func(*tg.DocumentAttributeVideo)

// RoundMessage marks a round video message (round_message flag).
func RoundMessage(video *tg.DocumentAttributeVideo) {
	video.SetRoundMessage(true)
}

// Nosound marks a muted video (nosound flag).
func Nosound(video *tg.DocumentAttributeVideo) {
	video.SetNosound(true)
}

// Streaming marks a streamable video (supports_streaming flag).
func Streaming(video *tg.DocumentAttributeVideo) {
	video.SetSupportsStreaming(true)
}

// VideoAttr builds documentAttributeVideo#43c57c48: flags round_message,
// nosound and supports_streaming are set through the gotd flag helpers.
func VideoAttr(width, height int, seconds float64, opts ...VideoOption) *tg.DocumentAttributeVideo {
	video := &tg.DocumentAttributeVideo{W: width, H: height, Duration: seconds}

	for _, opt := range opts {
		opt(video)
	}

	return video
}

// AudioOption customizes an AudioAttr fixture.
type AudioOption func(*tg.DocumentAttributeAudio)

// Voice marks a voice message (voice flag).
func Voice(audio *tg.DocumentAttributeAudio) {
	audio.SetVoice(true)
}

// AudioTitle sets the audio title (title flag field).
func AudioTitle(title string) AudioOption {
	return func(audio *tg.DocumentAttributeAudio) {
		audio.SetTitle(title)
	}
}

// AudioPerformer sets the audio performer (performer flag field).
func AudioPerformer(performer string) AudioOption {
	return func(audio *tg.DocumentAttributeAudio) {
		audio.SetPerformer(performer)
	}
}

// AudioAttr builds documentAttributeAudio#9852f9c6 with duration seconds;
// the voice flag turns the audio file into a voice message.
func AudioAttr(duration int, opts ...AudioOption) *tg.DocumentAttributeAudio {
	audio := &tg.DocumentAttributeAudio{Duration: duration}

	for _, opt := range opts {
		opt(audio)
	}

	return audio
}

// StickerAttr builds documentAttributeSticker#6319d612 with the alternative
// emoji and an empty stickerset.
func StickerAttr(alt string) *tg.DocumentAttributeSticker {
	return &tg.DocumentAttributeSticker{Alt: alt, Stickerset: &tg.InputStickerSetEmpty{}}
}

// AnimatedAttr builds documentAttributeAnimated#11b58939, marking an
// animated (GIF/MPEG4) document.
func AnimatedAttr() *tg.DocumentAttributeAnimated {
	return &tg.DocumentAttributeAnimated{}
}

// NamedFileAttr builds documentAttributeFilename#15590068 carrying the
// original filename.
func NamedFileAttr(name string) *tg.DocumentAttributeFilename {
	return &tg.DocumentAttributeFilename{FileName: name}
}

// Photo builds photo#fb197a65 with a stable date and DC plus the given size
// variants.
func Photo(id int64, sizes ...tg.PhotoSizeClass) *tg.Photo {
	return &tg.Photo{ID: id, Date: defaultDate, DCID: defaultDC, Sizes: sizes}
}

// PhotoSize builds photoSize#75c78e60; kind is the documented thumbnail
// letter (m for preview, x for full size).
func PhotoSize(kind string, width, height, size int) *tg.PhotoSize {
	return &tg.PhotoSize{Type: kind, W: width, H: height, Size: size}
}

// ProgressiveSize builds photoSizeProgressive#fa3efb95 carrying the
// progressive JPEG prefix sizes.
func ProgressiveSize(kind string, width, height int, sizes ...int) *tg.PhotoSizeProgressive {
	return &tg.PhotoSizeProgressive{Type: kind, W: width, H: height, Sizes: sizes}
}

// UserOption customizes a User fixture.
type UserOption func(*tg.User)

// WithUsername sets the @username.
func WithUsername(name string) UserOption {
	return func(user *tg.User) {
		user.SetUsername(name)
	}
}

// WithAccessHash sets the access hash peers need to address the user.
func WithAccessHash(hash int64) UserOption {
	return func(user *tg.User) {
		user.AccessHash = hash
	}
}

// WithPhone sets the phone number.
func WithPhone(phone string) UserOption {
	return func(user *tg.User) {
		user.SetPhone(phone)
	}
}

// WithBot flags the account as a bot.
func WithBot(user *tg.User) {
	user.SetBot(true)
}

// WithContact flags a contacts-list membership.
func WithContact(user *tg.User) {
	user.SetContact(true)
}

// WithMutual flags a mutual contact.
func WithMutual(user *tg.User) {
	user.SetMutualContact(true)
}

// WithPremium flags a premium subscriber.
func WithPremium(user *tg.User) {
	user.SetPremium(true)
}

// WithVerified flags a verified account.
func WithVerified(user *tg.User) {
	user.SetVerified(true)
}

// WithScam flags a scam-flagged account.
func WithScam(user *tg.User) {
	user.SetScam(true)
}

// WithDeleted flags a deleted account.
func WithDeleted(user *tg.User) {
	user.SetDeleted(true)
}

// User builds user#b1b8cc83 with the id and first name; flag fields map
// onto the documented user flags (bot, contact, premium, verified, scam,
// deleted, mutual_contact).
func User(id int64, name string, opts ...UserOption) *tg.User {
	user := &tg.User{ID: id, FirstName: name}

	for _, opt := range opts {
		opt(user)
	}

	return user
}

// ChannelOption customizes a Channel fixture.
type ChannelOption func(*tg.Channel)

// WithBroadcast flags a broadcast channel.
func WithBroadcast(channel *tg.Channel) {
	channel.SetBroadcast(true)
}

// WithMegagroup flags a megagroup (supergroup chat).
func WithMegagroup(channel *tg.Channel) {
	channel.SetMegagroup(true)
}

// WithForum flags a forum-enabled supergroup.
func WithForum(channel *tg.Channel) {
	channel.SetForum(true)
}

// WithGigagroup flags a gigagroup (broadcast supergroup).
func WithGigagroup(channel *tg.Channel) {
	channel.SetGigagroup(true)
}

// WithChannelVerified flags a verified channel.
func WithChannelVerified(channel *tg.Channel) {
	channel.SetVerified(true)
}

// WithNoforwards flags content protection (forwards disabled).
func WithNoforwards(channel *tg.Channel) {
	channel.SetNoforwards(true)
}

// WithChannelUsername sets the public @username.
func WithChannelUsername(name string) ChannelOption {
	return func(channel *tg.Channel) {
		channel.SetUsername(name)
	}
}

// WithChannelAccessHash sets the access hash peers need to address the
// channel.
func WithChannelAccessHash(hash int64) ChannelOption {
	return func(channel *tg.Channel) {
		channel.AccessHash = hash
	}
}

// Channel builds channel#d49f34c6 with id and title; flag fields map onto
// the documented channel flags (megagroup, broadcast, forum, gigagroup,
// verified, noforwards). The photo is chatPhotoEmpty, as carried by chats
// without a locally known photo.
func Channel(id int64, title string, opts ...ChannelOption) *tg.Channel {
	channel := &tg.Channel{ID: id, Title: title, Photo: &tg.ChatPhotoEmpty{}}

	for _, opt := range opts {
		opt(channel)
	}

	return channel
}

// ChatOption customizes a ChatSmall fixture.
type ChatOption func(*tg.Chat)

// WithChatNoforwards flags content protection on a basic group.
func WithChatNoforwards(chat *tg.Chat) {
	chat.SetNoforwards(true)
}

// ChatSmall builds chat#41cbf256: a basic legacy group with an empty photo.
func ChatSmall(id int64, title string, opts ...ChatOption) *tg.Chat {
	chat := &tg.Chat{ID: id, Title: title, Photo: &tg.ChatPhotoEmpty{}}

	for _, opt := range opts {
		opt(chat)
	}

	return chat
}

// HistoryPage builds messages.messages#1d73e7ea: the final page of a
// private-chat or basic-group history query — the gotd message iterator
// stops after receiving it.
func HistoryPage(
	messages []tg.MessageClass, users []tg.UserClass, chats []tg.ChatClass,
) *tg.MessagesMessages {
	return &tg.MessagesMessages{Messages: messages, Users: users, Chats: chats}
}

// ChannelHistoryPage builds messages.channelMessages#c776ba4e with the
// total count; the gotd message iterator keeps paginating while pages stay
// full (len(messages) >= batch limit) and stops on a short page.
func ChannelHistoryPage(
	count int, messages []tg.MessageClass, users []tg.UserClass, chats []tg.ChatClass,
) *tg.MessagesChannelMessages {
	return &tg.MessagesChannelMessages{
		Pts: forumTopicPts, Count: count, Messages: messages, Users: users, Chats: chats,
	}
}

// Topics builds messages.forumTopics#367617d3 with the total topic count.
func Topics(count int, topics ...tg.ForumTopicClass) *tg.MessagesForumTopics {
	return &tg.MessagesForumTopics{Count: count, Topics: topics}
}

// Topic builds forumTopic#fcdad815 with id, title and the default chat
// peer; topMessage carries the thread's newest message id and from_id the
// topic creator.
func Topic(id int, title string) *tg.ForumTopic {
	return &tg.ForumTopic{
		ID: id, Date: defaultDate, Title: title, IconColor: id,
		TopMessage: id, ReadInboxMaxID: id, ReadOutboxMaxID: id,
		Peer:   &tg.PeerChannel{ChannelID: defaultPeerID},
		FromID: &tg.PeerUser{UserID: selfUserID},
	}
}

// ResolvedPeer builds contacts.resolvedPeer#7f077ad9: the resolved peer
// plus the entity set needed to address it.
func ResolvedPeer(
	peer tg.PeerClass, users []tg.UserClass, chats []tg.ChatClass,
) *tg.ContactsResolvedPeer {
	return &tg.ContactsResolvedPeer{Peer: peer, Users: users, Chats: chats}
}

// ContactsList builds contacts.contacts#eae87e42: the full contact list.
func ContactsList(users ...tg.UserClass) *tg.ContactsContacts {
	return &tg.ContactsContacts{Users: users}
}

// FileChunk builds upload.file#96a18d5 wrapping a
// storage.filePartial#40bc6f52 payload.
func FileChunk(mtime int, payload []byte) *tg.UploadFile {
	return &tg.UploadFile{Type: &tg.StorageFilePartial{}, Mtime: mtime, Bytes: payload}
}

// FloodWait returns the documented ratelimit error: code 420 with a
// FLOOD_WAIT_%d type whose argument carries the required wait seconds.
func FloodWait(seconds int) error {
	return tgerr.New(codeFloodWait, fmt.Sprintf(floodWaitFormat, seconds))
}

// ChannelPrivate returns the documented 400 CHANNEL_PRIVATE error raised
// when a chat is inaccessible to the account.
func ChannelPrivate() error {
	return tgerr.New(codeBadRequest, channelPrivate)
}

// FileRefExpired returns the documented 400 FILE_REFERENCE_EXPIRED error
// raised when a media reference must be refetched.
func FileRefExpired() error {
	return tgerr.New(codeBadRequest, fileRefExpired)
}
