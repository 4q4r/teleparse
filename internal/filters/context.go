package filters

// Context is the pure-Go projection of one Telegram message, consumed by the
// client-side predicates produced by Compile. It deliberately carries no
// transport types so predicate logic stays decoupled and testable.
type Context struct {
	Chat    Chat
	Sender  Sender
	Message Message
	File    *FileInfo
}

// Chat describes the chat a message belongs to. Type is one of private, bot,
// group, supergroup, channel or forum. Deleted marks user peers whose account
// was deleted; it stays false for group and channel peers, where the concept
// does not apply.
type Chat struct {
	ID                         int64
	Type                       string
	Title, Username            string
	Archived, Protected, Saved bool
	Deleted                    bool
}

// Sender describes the message author. Present is false when the sender is
// unknown, e.g. for anonymous channel posts.
type Sender struct {
	Present                                           bool
	ID                                                int64
	Name, Username, Phone                             string
	IsBot, IsContact, IsMutual, IsPremium, IsVerified bool
	IsScam, IsDeleted                                 bool
}

// Message carries per-message fields shared by every predicate family. Date
// and EditDate are unix seconds; HasEdit distinguishes a zero edit date.
type Message struct {
	ID                      int64
	Out                     bool
	Date, EditDate          int64
	HasEdit                 bool
	Text, Caption           string
	Entities                []Entity
	Pinned, Silent, Service bool
	Views, Forwards         int64
	Reactions               []Reaction
	TotalReactions          int
	Forward                 *ForwardInfo
	Reply                   *ReplyInfo
	GroupedID               int64
	Mentioned, Spoiler      bool
	TTLSeconds              int
}

// Entity is a message entity. Kind is one of url, email, phone, mention,
// text_mention, hashtag, bot_command, spoiler, code, pre, quote,
// custom_emoji or text_link; Data carries href or user-id payloads.
type Entity struct {
	Kind       string
	Text, Data string
}

// Reaction is one reaction emoji with its total count.
type Reaction struct {
	Emoji string
	Count int
}

// ForwardInfo describes a forward origin. Present is false for non-forwarded
// messages; Hidden marks protected origins without usable sender data.
type ForwardInfo struct {
	Present                bool
	FromID                 int64
	FromName, FromUsername string
	Hidden                 bool
	Date                   int64
}

// ReplyInfo points at the message this one replies to.
type ReplyInfo struct {
	Present bool
	ToMsgID int64
}

// FileInfo describes attached media checkable before download. Kind is one of
// photo, video, video-note, voice, audio, document, sticker, gif, webpage,
// poll, geo, contact, dice, game, invoice, story, paid or unsupported.
// StickerKind is one of static, animated or video.
type FileInfo struct {
	Present                       bool
	Kind                          string
	Mime, Name, Ext               string
	Size                          int64
	Duration                      int
	Width, Height                 int
	Streamable, NoSound, Waveform bool
	StickerKind                   string
}

// TextOrCaption returns the message text, falling back to the media caption.
func (c *Context) TextOrCaption() string {
	if c.Message.Text != "" {
		return c.Message.Text
	}

	return c.Message.Caption
}

// Pixels returns the file's pixel count, or 0 when absent or unknown.
func (f *FileInfo) Pixels() int {
	if f == nil || f.Width <= 0 || f.Height <= 0 {
		return 0
	}

	return f.Width * f.Height
}
