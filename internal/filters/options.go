// Package filters defines the filter options model (single source of truth for
// CLI flags, config keys and TOML profiles) and the predicate compiler that
// turns options into server-side pushdown plans plus client-side predicates.
package filters

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Sentinel errors wrapped by dynamic validation messages.
var (
	ErrBadVocab       = errors.New("value not allowed")
	ErrBadRegex       = errors.New("invalid regex")
	ErrBadIDRange     = errors.New("min-id > max-id")
	ErrBadParse       = errors.New("cannot parse value")
	ErrBadBool        = errors.New("expected true|false")
	ErrNotUnmarshaler = errors.New("type does not implement UnmarshalText")
)

// TriBool is a tri-state option: unset = don't care, true = must be, false = must not be.
type TriBool struct {
	set   bool
	value bool
}

// Tri returns a set TriBool.
func Tri(v bool) TriBool { return TriBool{set: true, value: v} }

// Set marks the value as explicitly set.
func (t *TriBool) Set(v bool) { t.set, t.value = true, v }

// IsSet reports whether the option was explicitly provided.
func (t *TriBool) IsSet() bool { return t.set }

// Value returns the stored value (false when unset).
func (t *TriBool) Value() bool { return t.value }

// String renders the value; empty string when unset.
func (t *TriBool) String() string {
	if !t.set {
		return ""
	}

	return strconv.FormatBool(t.value)
}

// MarshalText implements encoding.TextMarshaler for TOML.
func (t *TriBool) MarshalText() ([]byte, error) {
	if !t.set {
		return nil, nil
	}

	return []byte(strconv.FormatBool(t.value)), nil
}

// UnmarshalText implements encoding.TextUnmarshaler for TOML and flags.
func (t *TriBool) UnmarshalText(text []byte) error {
	if len(text) == 0 {
		t.set = false

		return nil
	}

	v, err := strconv.ParseBool(string(text))
	if err != nil {
		return fmt.Errorf("%q: %w: %w", string(text), ErrBadBool, err)
	}

	t.set, t.value = true, v

	return nil
}

// Recursion controls scope expansion.
type Recursion struct {
	Topics   bool   `toml:"topics"   flag:"recurse-topics"  usage:"expand forum topics inside forums"`
	Albums   string `toml:"albums"   flag:"albums"          usage:"album handling: expand|first|skip"`
	Replies  int    `toml:"replies"  flag:"follow-replies"  usage:"follow reply chains up to N levels deep"`
	Forwards int    `toml:"forwards" flag:"follow-forwards" usage:"unpack forward origins up to N levels into scan scope"`
}

// Options is the full filter surface. Empty/nil means "no constraint".
// Field groups map 1:1 to CLI flag families and TOML keys.
type Options struct {
	// --- chat scope ---
	ChatType        []string `toml:"chat_type"        flag:"chat-type"        usage:"chat types: private,group,supergroup,channel,forum (comma list)"`
	ExcludeChatType []string `toml:"exclude_chat_type" flag:"exclude-chat-type" usage:"chat types to skip (comma list); overrides --chat-type matches"`
	ChatGlob        string   `toml:"chat_glob"        flag:"chat-glob"        usage:"glob over chat titles, e.g. 'News*'"`
	ChatRegex       string   `toml:"chat_regex"       flag:"chat-regex"       usage:"regex over chat titles"`
	Archived        string   `toml:"archived"         flag:"archived"         usage:"archived chats: only|exclude|any"`
	SavedOnly       bool     `toml:"saved_only"       flag:"saved"            usage:"scan Saved Messages only"`
	ChatUsername    string   `toml:"chat_username"    flag:"chat-username"    usage:"exact chat @username"`
	ChatDeleted     TriBool  `toml:"chat_deleted"     flag:"chat-deleted"     usage:"chats whose peer is a deleted account (true|false)"`

	// --- sender ---
	ContactsOnly        bool     `toml:"contacts_only"        flag:"sender-contacts"       usage:"only messages from users in my contacts"`
	MutualOnly          bool     `toml:"mutual_only"          flag:"sender-mutual"         usage:"only mutual contacts"`
	FromMe              TriBool  `toml:"from_me"              flag:"from-me"               usage:"messages sent by me (true|false)"`
	FromUsers           []string `toml:"from_users"           flag:"from"                  usage:"sender ids/@usernames/phone numbers (comma list)"`
	ExcludeUsers        []string `toml:"exclude_users"        flag:"exclude"               usage:"sender ids/@usernames/phone numbers to skip"`
	SenderBot           TriBool  `toml:"sender_bot"           flag:"sender-bot"            usage:"bot senders (true|false)"`
	SenderPremium       TriBool  `toml:"sender_premium"       flag:"sender-premium"        usage:"premium senders (true|false)"`
	SenderVerified      TriBool  `toml:"sender_verified"      flag:"sender-verified"       usage:"verified senders (true|false)"`
	SenderScam          TriBool  `toml:"sender_scam"          flag:"sender-scam"           usage:"flagged-scam senders (true|false)"`
	SenderDeleted       TriBool  `toml:"sender_deleted"       flag:"sender-deleted"        usage:"deleted accounts (true|false)"`
	SenderNonContacts   bool     `toml:"sender_non_contacts"   flag:"sender-non-contacts"   usage:"only senders NOT in my contacts"`
	SenderNonMutual     bool     `toml:"sender_non_mutual"     flag:"sender-non-mutual"     usage:"only senders who are NOT mutual contacts"`
	SenderNameRegex     string   `toml:"sender_name_regex"     flag:"sender-name-regex"     usage:"regex over sender display name"`
	SenderUsernameRegex string   `toml:"sender_username_regex" flag:"sender-username-regex" usage:"regex over sender @username"`
	SenderPhoneRegex    string   `toml:"sender_phone_regex"   flag:"sender-phone-regex"    usage:"regex over sender phone (contacts only)"`

	// --- media type ---
	Media        []string `toml:"media"         flag:"media"         usage:"media types: photo,video,video-note,voice,audio,document,sticker,gif (comma list)"`
	ExcludeMedia []string `toml:"exclude_media" flag:"exclude-media" usage:"media kinds to skip (comma list); overrides --media matches"`
	StickerKind  []string `toml:"sticker_kind"  flag:"sticker-kind"  usage:"sticker kinds: static,animated,video"`
	HasMedia     TriBool  `toml:"has_media"     flag:"has-media"     usage:"any media attached (true|false)"`
	InAlbum      string   `toml:"in_album"      flag:"in-album"      usage:"album membership: any|only|first"`

	// --- file metadata (all checkable before download) ---
	Mime        []string `toml:"mime"         flag:"mime"         usage:"MIME globs, e.g. application/zip,video/* (comma list)"`
	ExcludeMime []string `toml:"exclude_mime" flag:"exclude-mime" usage:"MIME globs to skip, e.g. image/* (comma list); overrides --mime matches"`
	Ext         []string `toml:"ext"          flag:"ext"          usage:"extensions with dot, e.g. .zip,.rar (comma list)"`
	ExcludeExt  []string `toml:"exclude_ext"  flag:"exclude-ext"  usage:"extensions to skip with dot (comma list); overrides --ext matches"`
	NameGlob    string   `toml:"name_glob"    flag:"name"         usage:"filename glob, e.g. '*.zip'"`
	NameRegex   string   `toml:"name_regex"   flag:"name-regex"   usage:"filename regex"`
	MinSize     string   `toml:"min_size"     flag:"min-size"     usage:"min file size, e.g. 10MB"`
	MaxSize     string   `toml:"max_size"     flag:"max-size"     usage:"max file size, e.g. 20MB"`
	MinDuration string   `toml:"min_duration" flag:"min-duration" usage:"min A/V duration, e.g. 30s"`
	MaxDuration string   `toml:"max_duration" flag:"max-duration" usage:"max A/V duration, e.g. 10m"`
	MinWidth    int      `toml:"min_width"    flag:"min-width"    usage:"min width px"`
	MaxWidth    int      `toml:"max_width"    flag:"max-width"    usage:"max width px"`
	MinHeight   int      `toml:"min_height"   flag:"min-height"   usage:"min height px"`
	MaxHeight   int      `toml:"max_height"   flag:"max-height"   usage:"max height px"`
	MinMP       float64  `toml:"min_mp"       flag:"min-mp"       usage:"min resolution in megapixels"`
	Streamable  TriBool  `toml:"streamable"   flag:"streamable"   usage:"streamable video (true|false)"`
	NoSound     TriBool  `toml:"no_sound"     flag:"video-nosound" usage:"muted videos (true|false)"`

	// --- text & entities ---
	TextRegex    string   `toml:"text_regex"   flag:"text-regex"    usage:"regex over text or caption"`
	HasText      string   `toml:"has_text"     flag:"has-text"      usage:"text presence: any|only|none"`
	Hashtag      []string `toml:"hashtag"      flag:"hashtag"       usage:"exact hashtags without # (comma list)"`
	AnyHashtag   bool     `toml:"any_hashtag"  flag:"any-hashtag"   usage:"any hashtag present"`
	Mention      []string `toml:"mention"      flag:"text-mention"  usage:"@username mentions in text (comma list); any with '*'"`
	WasMentioned TriBool  `toml:"was_mentioned" flag:"was-mentioned" usage:"message notified me via mention (true|false)"`
	HasURL       TriBool  `toml:"has_url"      flag:"has-url"       usage:"URL entity or link preview present (true|false)"`
	URLRegex     string   `toml:"url_regex"    flag:"url-regex"     usage:"regex over URLs and link-preview URLs"`
	HasEmail     bool     `toml:"has_email"    flag:"has-email"     usage:"email entity present"`
	HasPhone     bool     `toml:"has_phone"    flag:"has-phone"     usage:"phone entity present"`
	Command      string   `toml:"command"      flag:"command"       usage:"bot command entity, e.g. /start"`
	EmojiOnly    bool     `toml:"emoji_only"   flag:"emoji-only"    usage:"messages containing only emoji"`

	// --- dates (ISO 8601 or relative like 7d, 12h, 30m) ---
	After     string  `toml:"after"       flag:"from-date"  usage:"messages on/after date (2026-01-01 or 7d)"`
	Before    string  `toml:"before"      flag:"until-date" usage:"messages on/before date"`
	Last      string  `toml:"last"        flag:"last"       usage:"shorthand for after=now-N (e.g. --last 7d)"`
	OlderThan string  `toml:"older_than"  flag:"older-than" usage:"shorthand for before=now-N"`
	Edited    TriBool `toml:"edited"      flag:"edited"     usage:"edited messages (true|false)"`

	// --- forward origin ---
	Forwarded     TriBool  `toml:"forwarded"       flag:"forwarded"     usage:"forwarded messages (true|false)"`
	FwdFrom       []string `toml:"fwd_from"        flag:"fwd-from"      usage:"forward origin ids/@usernames (comma list)"`
	FwdHidden     TriBool  `toml:"fwd_hidden"      flag:"fwd-hidden"    usage:"hidden forward origin (true|false)"`
	FwdDateAfter  string   `toml:"fwd_date_after"  flag:"fwd-date-from" usage:"forward original date >="`
	FwdDateBefore string   `toml:"fwd_date_before" flag:"fwd-date-to"   usage:"forward original date <="`

	// --- replies & engagement ---
	IsReply      TriBool  `toml:"is_reply"      flag:"is-reply"      usage:"is a reply (true|false)"`
	MinViews     int64    `toml:"min_views"     flag:"min-views"     usage:"min channel post views"`
	MinForwards  int64    `toml:"min_forwards"  flag:"min-forwards"  usage:"min forward count"`
	MinReactions int      `toml:"min_reactions" flag:"min-reactions" usage:"min total reaction count"`
	Reaction     []string `toml:"reaction"      flag:"reaction"      usage:"specific reaction emoji (comma list)"`
	Pinned       TriBool  `toml:"pinned"        flag:"pinned"        usage:"pinned messages (true|false)"`

	// --- id ranges & service ---
	MinID   int64  `toml:"min_id"  flag:"min-id"  usage:"message id >="`
	MaxID   int64  `toml:"max_id"  flag:"max-id"  usage:"message id <="`
	Service string `toml:"service" flag:"service" usage:"service messages: any|only|exclude"`

	// --- misc message flags ---
	Silent        TriBool `toml:"silent"         flag:"silent"         usage:"sent silently (true|false)"`
	HasSpoiler    TriBool `toml:"has_spoiler"    flag:"spoiler"        usage:"spoiler-covered media (true|false)"`
	SkipProtected bool    `toml:"skip_protected" flag:"skip-protected" usage:"skip chats with protected content"`

	// --- execution ---
	Limit        int       `toml:"limit"         flag:"limit"         usage:"max messages to SCAN per chat"`
	Reverse      bool      `toml:"reverse"       flag:"reverse"       usage:"scan oldest-first instead of newest-first"`
	Order        string    `toml:"order"         flag:"order"         usage:"output order: date|id (listing only)"`
	Dedupe       string    `toml:"dedupe"        flag:"dedupe"        usage:"cross-chat dedup: unique-id|hash|off"`
	SkipExisting bool      `toml:"skip_existing" flag:"skip-existing" usage:"skip files already on disk"`
	SinceState   string    `toml:"since_state"   flag:"since-state"   usage:"incremental: read per-chat watermarks from state db"`
	Recursion    Recursion `toml:"recursion"     flag:""              usage:"recursion options (recurse-* flags)"`
}

// Vocabularies returned as fresh slices (callers must not mutate).
func mediaKinds() []string {
	return []string{
		"photo", "video", "video-note", "voice", "audio", "document", "sticker", "gif",
		"webpage", "poll", "geo", "contact", "dice", "game", "invoice", "story", "paid", "unsupported",
	}
}

func chatTypes() []string {
	return []string{"private", "bot", "group", "supergroup", "channel", "forum"}
}

func stickerKinds() []string { return []string{"static", "animated", "video"} }

func modeList(name string) []string {
	switch name {
	case "albums":
		return []string{"", "expand", "first", "skip"}
	case "archived":
		return []string{"", "only", "exclude", "any"}
	case "has-text":
		return []string{"", "any", "only", "none"}
	case "service":
		return []string{"", "any", "only", "exclude"}
	case "dedupe":
		return []string{"", "unique-id", "hash", "off"}
	default:
		return nil
	}
}

// Validate checks vocabularies and parseable values. It does not compile the
// predicate pipeline; Compile does that once at scan time.
func (o *Options) Validate() error {
	checks := []struct {
		name    string
		values  []string
		allowed []string
	}{
		{"media", o.Media, mediaKinds()},
		{"exclude-media", o.ExcludeMedia, mediaKinds()},
		{"chat-type", o.ChatType, chatTypes()},
		{"exclude-chat-type", o.ExcludeChatType, chatTypes()},
		{"sticker-kind", o.StickerKind, stickerKinds()},
		{"albums", []string{o.Recursion.Albums}, modeList("albums")},
		{"archived", []string{o.Archived}, modeList("archived")},
		{"has-text", []string{o.HasText}, modeList("has-text")},
		{"service", []string{o.Service}, modeList("service")},
		{"dedupe", []string{o.Dedupe}, modeList("dedupe")},
	}

	for _, c := range checks {
		if err := checkVocab(c.name, c.values, c.allowed); err != nil {
			return err
		}
	}

	if err := o.validateParsed(); err != nil {
		return err
	}

	return o.validateRegexes()
}

func (o *Options) validateParsed() error {
	numeric := []struct {
		name string
		raw  string
		kind string
	}{
		{"min-size", o.MinSize, "size"},
		{"max-size", o.MaxSize, "size"},
		{"min-duration", o.MinDuration, "duration"},
		{"max-duration", o.MaxDuration, "duration"},
		{"after", o.After, "time"},
		{"before", o.Before, "time"},
		{"last", o.Last, "time"},
		{"older-than", o.OlderThan, "time"},
	}
	for _, n := range numeric {
		if n.raw == "" {
			continue
		}

		if err := parseByKind(n.raw, n.kind); err != nil {
			return fmt.Errorf("%s: %w", n.name, err)
		}
	}

	if o.MinID > 0 && o.MaxID > 0 && o.MinID > o.MaxID {
		return fmt.Errorf("min-id %d, max-id %d: %w", o.MinID, o.MaxID, ErrBadIDRange)
	}

	return nil
}

func (o *Options) validateRegexes() error {
	regexes := map[string]string{
		"chat-regex":            o.ChatRegex,
		"name-regex":            o.NameRegex,
		"text-regex":            o.TextRegex,
		"url-regex":             o.URLRegex,
		"sender-name-regex":     o.SenderNameRegex,
		"sender-username-regex": o.SenderUsernameRegex,
		"sender-phone-regex":    o.SenderPhoneRegex,
	}
	for name, pattern := range regexes {
		if pattern == "" {
			continue
		}

		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("%s %q: %w: %w", name, pattern, ErrBadRegex, err)
		}
	}

	return nil
}

func parseByKind(raw, kind string) error {
	var err error

	switch kind {
	case "size":
		_, err = ParseSize(raw)
	case "duration":
		_, err = ParseDuration(raw)
	default:
		_, err = ParseTime(raw)
	}

	if err != nil {
		return fmt.Errorf("%q: %w: %w", raw, ErrBadParse, err)
	}

	return nil
}

func checkVocab(name string, values, allowed []string) error {
	for _, v := range values {
		if !contains(allowed, v) {
			return fmt.Errorf("%s %q: %w (one of %s)", name, v, ErrBadVocab, strings.Join(allowed, ","))
		}
	}

	return nil
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}

	return false
}

// ParseDuration parses "30s", "10m", "1h30m" into time.Duration.
func ParseDuration(input string) (time.Duration, error) {
	d, err := time.ParseDuration(input)
	if err != nil {
		return 0, fmt.Errorf("duration %q: %w: %w", input, ErrBadParse, err)
	}

	return d, nil
}
