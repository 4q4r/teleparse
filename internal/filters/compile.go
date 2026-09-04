package filters

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

const (
	pixelsPerMegapixel = 1_000_000
	executionNote      = "execution: limit/reverse/order/dedupe/skip-existing/" +
		"since-state/recursion affect scanning, not matching"
)

type regexSet struct {
	chat           *regexp.Regexp
	senderName     *regexp.Regexp
	senderUsername *regexp.Regexp
	senderPhone    *regexp.Regexp
	name           *regexp.Regexp
	text           *regexp.Regexp
	url            *regexp.Regexp
}

type fileBounds struct {
	minSize    int64
	maxSize    int64
	minSeconds int
	maxSeconds int
}

type explainRow struct {
	on   bool
	line string
}

// Compile validates options and compiles them into a server pushdown plan
// plus ordered client-side predicates. It fails loudly on values Validate
// cannot see, such as malformed glob patterns.
func Compile(opts *Options) (*Plan, error) {
	if err := opts.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCompile, err)
	}

	if err := validateCompileExtras(opts); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCompile, err)
	}

	push, err := derivePushdown(opts)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCompile, err)
	}

	set, err := compileRegexes(opts)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCompile, err)
	}

	filePreds, err := filePredicates(opts, set)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCompile, err)
	}

	datePreds, err := datePredicates(opts)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCompile, err)
	}

	fwdPreds, err := forwardPredicates(opts)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCompile, err)
	}

	plan := &Plan{Pushdown: push}
	plan.Predicates = append(plan.Predicates, chatPredicates(opts, set)...)
	plan.Predicates = append(plan.Predicates, senderPredicates(opts, set)...)
	plan.Predicates = append(plan.Predicates, mediaPredicates(opts)...)
	plan.Predicates = append(plan.Predicates, filePreds...)
	plan.Predicates = append(plan.Predicates, textPredicates(opts, set)...)
	plan.Predicates = append(plan.Predicates, datePreds...)
	plan.Predicates = append(plan.Predicates, fwdPreds...)
	plan.Predicates = append(plan.Predicates, engagementPredicates(opts)...)
	plan.Predicates = append(plan.Predicates, miscPredicates(opts)...)
	plan.ExplainLines = explainLines(opts, push)

	return plan, nil
}

func validateCompileExtras(opts *Options) error {
	if err := checkVocab("in-album", []string{opts.InAlbum}, inAlbumModes()); err != nil {
		return err
	}

	return validateGlobs(opts)
}

func inAlbumModes() []string {
	return []string{"", "any", "only", "first"}
}

func validateGlobs(opts *Options) error {
	patterns := append([]string{opts.ChatGlob, opts.NameGlob}, opts.Mime...)
	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}

		if _, err := path.Match(pattern, ""); err != nil {
			return fmt.Errorf("glob %q: %w", pattern, err)
		}
	}

	return nil
}

func derivePushdown(opts *Options) (Pushdown, error) {
	push := Pushdown{MessagesFilter: messagesFilterFor(opts.Media)}

	minDate, err := resolveDate("after/last", opts.After, opts.Last)
	if err != nil {
		return push, err
	}

	maxDate, err := resolveDate("before/older-than", opts.Before, opts.OlderThan)
	if err != nil {
		return push, err
	}

	push.MinDate = minDate
	push.MaxDate = maxDate
	push.FromUsers = cloneStrings(opts.FromUsers)
	push.Notes = pushdownNotes(opts, push.MessagesFilter)

	return push, nil
}

func messagesFilterFor(media []string) string {
	if len(media) == 2 && contains(media, "photo") && contains(media, "video") {
		return "photo_video"
	}

	if len(media) != 1 {
		return ""
	}

	switch media[0] {
	case "photo":
		return "photo"
	case "video":
		return "video"
	case "video-note":
		return "round_video"
	case "voice":
		return "voice"
	case "audio":
		return "music"
	case "document":
		return "document"
	case "gif":
		return "gif"
	case "geo":
		return "geo"
	case "contact":
		return "contact"
	case "webpage":
		return "url"
	default:
		return ""
	}
}

func resolveDate(label, primary, fallback string) (int64, error) {
	raw := primary
	if raw == "" {
		raw = fallback
	}

	if raw == "" {
		return 0, nil
	}

	stamp, err := ParseTime(raw)
	if err != nil {
		return 0, fmt.Errorf("%s %q: %w", label, raw, err)
	}

	return stamp.Unix(), nil
}

func pushdownNotes(opts *Options, messagesFilter string) []string {
	var notes []string

	if len(opts.FromUsers) > 0 && chatTypeAllowsPrivate(opts.ChatType) {
		notes = append(notes, "server ignores from_id in private chats")
	}

	if messagesFilter != "" && opts.Recursion.Topics {
		notes = append(notes, "media filter inactive inside forum-topic iteration")
	}

	if opts.InAlbum == "first" {
		notes = append(notes, "album=first: per-message pass-all; scan coalesces")
	}

	return notes
}

func chatTypeAllowsPrivate(types []string) bool {
	return len(types) == 0 || contains(types, "private")
}

func cloneStrings(values []string) []string {
	clone := make([]string, len(values))
	copy(clone, values)

	return clone
}

func compileRegexes(opts *Options) (regexSet, error) {
	var set regexSet

	fields := []struct {
		name    string
		pattern string
		target  **regexp.Regexp
	}{
		{"chat_regex", opts.ChatRegex, &set.chat},
		{"sender_name_regex", opts.SenderNameRegex, &set.senderName},
		{"sender_username_regex", opts.SenderUsernameRegex, &set.senderUsername},
		{"sender_phone_regex", opts.SenderPhoneRegex, &set.senderPhone},
		{"name_regex", opts.NameRegex, &set.name},
		{"text_regex", opts.TextRegex, &set.text},
		{"url_regex", opts.URLRegex, &set.url},
	}

	for _, field := range fields {
		if err := assignRegex(field.target, field.name, field.pattern); err != nil {
			return set, err
		}
	}

	return set, nil
}

func assignRegex(target **regexp.Regexp, name, pattern string) error {
	if pattern == "" {
		return nil
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("%s %q: %w: %w", name, pattern, ErrBadRegex, err)
	}

	*target = re

	return nil
}

func chatPredicates(opts *Options, set regexSet) []NamedPredicate {
	var preds []NamedPredicate

	if len(opts.ChatType) > 0 {
		preds = append(preds, NamedPredicate{Name: "chat_type", Fn: func(ctx *Context) bool {
			return chatTypeMatches(opts.ChatType, ctx)
		}})
	}

	if opts.ChatGlob != "" {
		preds = append(preds, NamedPredicate{Name: "chat_glob", Fn: func(ctx *Context) bool {
			return matchGlob(opts.ChatGlob, ctx.Chat.Title)
		}})
	}

	if set.chat != nil {
		preds = append(preds, NamedPredicate{Name: "chat_regex", Fn: func(ctx *Context) bool {
			return set.chat.MatchString(ctx.Chat.Title)
		}})
	}

	if opts.Archived == "only" || opts.Archived == "exclude" {
		want := opts.Archived == "only"

		preds = append(preds, NamedPredicate{Name: "archived", Fn: func(ctx *Context) bool {
			return ctx.Chat.Archived == want
		}})
	}

	if opts.SavedOnly {
		preds = append(preds, NamedPredicate{Name: "saved_only", Fn: func(ctx *Context) bool {
			return ctx.Chat.Saved
		}})
	}

	if opts.ChatUsername != "" {
		target := normalizeHandle(opts.ChatUsername)

		preds = append(preds, NamedPredicate{Name: "chat_username", Fn: func(ctx *Context) bool {
			return normalizeHandle(ctx.Chat.Username) == target
		}})
	}

	if opts.SkipProtected {
		preds = append(preds, NamedPredicate{Name: "skip_protected", Fn: func(ctx *Context) bool {
			return !ctx.Chat.Protected
		}})
	}

	return preds
}

func chatTypeMatches(types []string, ctx *Context) bool {
	for _, want := range types {
		if want == "bot" && (ctx.Chat.Type == "bot" || ctx.Sender.IsBot) {
			return true
		}

		if ctx.Chat.Type == want {
			return true
		}
	}

	return false
}

func senderPredicates(opts *Options, set regexSet) []NamedPredicate {
	var preds []NamedPredicate

	if opts.ContactsOnly {
		preds = append(preds, NamedPredicate{Name: "contacts_only", Fn: func(ctx *Context) bool {
			return ctx.Sender.IsContact
		}})
	}

	if opts.MutualOnly {
		preds = append(preds, NamedPredicate{Name: "mutual_only", Fn: func(ctx *Context) bool {
			return ctx.Sender.IsMutual
		}})
	}

	if opts.FromMe.IsSet() {
		preds = append(preds, triPredicate("from_me", opts.FromMe.Value(), func(ctx *Context) bool {
			return ctx.Message.Out
		}))
	}

	if len(opts.FromUsers) > 0 {
		preds = append(preds, NamedPredicate{Name: "from_users", Fn: func(ctx *Context) bool {
			return senderMatches(opts.FromUsers, ctx)
		}})
	}

	if len(opts.ExcludeUsers) > 0 {
		preds = append(preds, NamedPredicate{Name: "exclude_users", Fn: func(ctx *Context) bool {
			return !senderMatches(opts.ExcludeUsers, ctx)
		}})
	}

	if opts.SenderBot.IsSet() {
		preds = append(preds, triPredicate("sender_bot", opts.SenderBot.Value(), func(ctx *Context) bool {
			return ctx.Sender.IsBot
		}))
	}

	if opts.SenderPremium.IsSet() {
		preds = append(preds, triPredicate("sender_premium", opts.SenderPremium.Value(), func(ctx *Context) bool {
			return ctx.Sender.IsPremium
		}))
	}

	if opts.SenderVerified.IsSet() {
		preds = append(preds, triPredicate("sender_verified", opts.SenderVerified.Value(), func(ctx *Context) bool {
			return ctx.Sender.IsVerified
		}))
	}

	if opts.SenderScam.IsSet() {
		preds = append(preds, triPredicate("sender_scam", opts.SenderScam.Value(), func(ctx *Context) bool {
			return ctx.Sender.IsScam
		}))
	}

	if opts.SenderDeleted.IsSet() {
		preds = append(preds, triPredicate("sender_deleted", opts.SenderDeleted.Value(), func(ctx *Context) bool {
			return ctx.Sender.IsDeleted
		}))
	}

	if set.senderName != nil {
		preds = append(preds, NamedPredicate{Name: "sender_name_regex", Fn: func(ctx *Context) bool {
			return set.senderName.MatchString(ctx.Sender.Name)
		}})
	}

	if set.senderUsername != nil {
		preds = append(preds, NamedPredicate{Name: "sender_username_regex", Fn: func(ctx *Context) bool {
			return set.senderUsername.MatchString(normalizeHandle(ctx.Sender.Username))
		}})
	}

	if set.senderPhone != nil {
		preds = append(preds, NamedPredicate{Name: "sender_phone_regex", Fn: func(ctx *Context) bool {
			return set.senderPhone.MatchString(ctx.Sender.Phone)
		}})
	}

	return preds
}

func triPredicate(name string, want bool, get func(*Context) bool) NamedPredicate {
	return NamedPredicate{Name: name, Fn: func(ctx *Context) bool {
		return get(ctx) == want
	}}
}

func senderMatches(entries []string, ctx *Context) bool {
	for _, entry := range entries {
		if senderMatchesEntry(entry, ctx) {
			return true
		}
	}

	return false
}

func senderMatchesEntry(entry string, ctx *Context) bool {
	return matchIDHandleOrPhone(entry, ctx.Sender.ID, ctx.Sender.Username, ctx.Sender.Phone)
}

// matchIDHandleOrPhone reports whether entry equals the decimal id, matches
// the username case-insensitively (ignoring a leading "@" on either side),
// or repeats the phone number's exact digit sequence. Phone entries must
// carry full international digits: "+", spaces and dashes are tolerated on
// either side, but no country-code guessing happens, so "8999…" never
// matches "+7999…". Phones resolve only when the account sees them
// (contacts or permissive privacy), so an empty stored phone never matches.
func matchIDHandleOrPhone(entry string, id int64, username, phone string) bool {
	needle := strings.TrimPrefix(entry, "@")
	if strconv.FormatInt(id, 10) == needle {
		return true
	}

	if strings.EqualFold(strings.TrimPrefix(username, "@"), needle) {
		return true
	}

	entryDigits := digitsOnly(entry)
	phoneDigits := digitsOnly(phone)

	return entryDigits != "" && phoneDigits != "" && entryDigits == phoneDigits
}

// digitsOnly strips everything but ASCII digits, so phone spellings that
// differ only in "+", spaces or dashes compare by their bare digit sequence.
func digitsOnly(value string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune("0123456789", r) {
			return r
		}

		return -1
	}, value)
}

func mediaPredicates(opts *Options) []NamedPredicate {
	var preds []NamedPredicate

	if len(opts.Media) > 0 {
		preds = append(preds, NamedPredicate{Name: "media", Fn: func(ctx *Context) bool {
			file, ok := fileOf(ctx)
			if !ok {
				return false
			}

			return contains(opts.Media, file.Kind)
		}})
	}

	if len(opts.StickerKind) > 0 {
		preds = append(preds, NamedPredicate{Name: "sticker_kind", Fn: func(ctx *Context) bool {
			file, ok := fileOf(ctx)
			if !ok {
				return false
			}

			return file.Kind == "sticker" && contains(opts.StickerKind, file.StickerKind)
		}})
	}

	if opts.HasMedia.IsSet() {
		want := opts.HasMedia.Value()

		preds = append(preds, NamedPredicate{Name: "has_media", Fn: func(ctx *Context) bool {
			_, hasFile := fileOf(ctx)

			return hasFile == want
		}})
	}

	if opts.InAlbum == "only" {
		preds = append(preds, NamedPredicate{Name: "in_album", Fn: func(ctx *Context) bool {
			return ctx.Message.GroupedID > 0
		}})
	}

	return preds
}

func filePredicates(opts *Options, set regexSet) ([]NamedPredicate, error) {
	bounds, err := parseFileBounds(opts)
	if err != nil {
		return nil, err
	}

	preds := fileMatchPredicates(opts, set)
	preds = append(preds, fileBoundPredicates(opts, bounds)...)

	return preds, nil
}

func parseFileBounds(opts *Options) (fileBounds, error) {
	var (
		bounds fileBounds
		err    error
	)

	bounds.minSize, err = optionalSize("min-size", opts.MinSize)
	if err != nil {
		return bounds, err
	}

	bounds.maxSize, err = optionalSize("max-size", opts.MaxSize)
	if err != nil {
		return bounds, err
	}

	bounds.minSeconds, err = optionalDuration("min-duration", opts.MinDuration)
	if err != nil {
		return bounds, err
	}

	bounds.maxSeconds, err = optionalDuration("max-duration", opts.MaxDuration)
	if err != nil {
		return bounds, err
	}

	return bounds, nil
}

func optionalSize(label, raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}

	size, err := ParseSize(raw)
	if err != nil {
		return 0, fmt.Errorf("%s %q: %w", label, raw, err)
	}

	return size, nil
}

func optionalDuration(label, raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}

	parsed, err := ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s %q: %w", label, raw, err)
	}

	return int(parsed.Seconds()), nil
}

func fileMatchPredicates(opts *Options, set regexSet) []NamedPredicate {
	var preds []NamedPredicate

	if len(opts.Mime) > 0 {
		patterns := lowerAll(opts.Mime)

		preds = append(preds, requireFilePredicate("mime", func(file *FileInfo) bool {
			return anyGlobMatches(patterns, strings.ToLower(file.Mime))
		}))
	}

	if len(opts.Ext) > 0 {
		wanted := normalizeExts(opts.Ext)

		preds = append(preds, requireFilePredicate("ext", func(file *FileInfo) bool {
			return contains(wanted, normalizeExt(file.Ext))
		}))
	}

	if opts.NameGlob != "" {
		preds = append(preds, requireFilePredicate("name_glob", func(file *FileInfo) bool {
			return matchGlob(opts.NameGlob, file.Name)
		}))
	}

	if set.name != nil {
		preds = append(preds, requireFilePredicate("name_regex", func(file *FileInfo) bool {
			return set.name.MatchString(file.Name)
		}))
	}

	return preds
}

func fileBoundPredicates(opts *Options, bounds fileBounds) []NamedPredicate {
	var preds []NamedPredicate

	if opts.MinSize != "" {
		preds = append(preds, requireFilePredicate("min_size", func(file *FileInfo) bool {
			return file.Size >= bounds.minSize
		}))
	}

	if opts.MaxSize != "" {
		preds = append(preds, requireFilePredicate("max_size", func(file *FileInfo) bool {
			return file.Size <= bounds.maxSize
		}))
	}

	if opts.MinDuration != "" {
		preds = append(preds, requireFilePredicate("min_duration", func(file *FileInfo) bool {
			return file.Duration >= bounds.minSeconds
		}))
	}

	if opts.MaxDuration != "" {
		preds = append(preds, requireFilePredicate("max_duration", func(file *FileInfo) bool {
			return file.Duration <= bounds.maxSeconds
		}))
	}

	if opts.MinWidth > 0 {
		preds = append(preds, requireFilePredicate("min_width", func(file *FileInfo) bool {
			return file.Width >= opts.MinWidth
		}))
	}

	if opts.MaxWidth > 0 {
		preds = append(preds, requireFilePredicate("max_width", func(file *FileInfo) bool {
			return file.Width <= opts.MaxWidth
		}))
	}

	if opts.MinHeight > 0 {
		preds = append(preds, requireFilePredicate("min_height", func(file *FileInfo) bool {
			return file.Height >= opts.MinHeight
		}))
	}

	if opts.MaxHeight > 0 {
		preds = append(preds, requireFilePredicate("max_height", func(file *FileInfo) bool {
			return file.Height <= opts.MaxHeight
		}))
	}

	if opts.MinMP > 0 {
		preds = append(preds, requireFilePredicate("min_mp", func(file *FileInfo) bool {
			return float64(file.Pixels())/pixelsPerMegapixel >= opts.MinMP
		}))
	}

	if opts.Streamable.IsSet() {
		want := opts.Streamable.Value()

		preds = append(preds, requireFilePredicate("streamable", func(file *FileInfo) bool {
			return file.Streamable == want
		}))
	}

	if opts.NoSound.IsSet() {
		want := opts.NoSound.Value()

		preds = append(preds, requireFilePredicate("no_sound", func(file *FileInfo) bool {
			return file.NoSound == want
		}))
	}

	return preds
}

func requireFilePredicate(name string, pass func(*FileInfo) bool) NamedPredicate {
	return NamedPredicate{Name: name, Fn: func(ctx *Context) bool {
		file, ok := fileOf(ctx)
		if !ok {
			return false
		}

		return pass(file)
	}}
}

func fileOf(ctx *Context) (*FileInfo, bool) {
	if ctx.File == nil || !ctx.File.Present {
		return nil, false
	}

	return ctx.File, true
}

func explainLines(opts *Options, push Pushdown) []string {
	rows := []explainRow{
		{on: push.MessagesFilter != "", line: "server: messages_filter=" + push.MessagesFilter},
		{on: push.MinDate != 0, line: "server: min_date=" + strconv.FormatInt(push.MinDate, 10)},
		{on: push.MaxDate != 0, line: "server: max_date=" + strconv.FormatInt(push.MaxDate, 10)},
		{on: len(push.FromUsers) > 0, line: "server: from_users=" + strings.Join(push.FromUsers, ",")},
		{on: len(opts.ChatType) > 0, line: "client: chat_type=" + strings.Join(opts.ChatType, ",")},
		{on: opts.ChatGlob != "", line: "client: chat_glob=" + opts.ChatGlob},
		{on: opts.ChatRegex != "", line: "client: chat_regex=" + opts.ChatRegex},
		{on: opts.Archived == "only" || opts.Archived == "exclude", line: "client: archived=" + opts.Archived},
		{on: opts.SavedOnly, line: "client: saved_only=true"},
		{on: opts.ChatUsername != "", line: "client: chat_username=" + opts.ChatUsername},
		{on: opts.SkipProtected, line: "client: skip_protected=true"},
		{on: opts.ContactsOnly, line: "client: contacts_only=true"},
		{on: opts.MutualOnly, line: "client: mutual_only=true"},
		{on: opts.FromMe.IsSet(), line: "client: from_me=" + opts.FromMe.String()},
		{on: len(opts.FromUsers) > 0, line: "client: from_users=" + strings.Join(opts.FromUsers, ",")},
		{on: len(opts.ExcludeUsers) > 0, line: "client: exclude_users=" + strings.Join(opts.ExcludeUsers, ",")},
		{on: opts.SenderBot.IsSet(), line: "client: sender_bot=" + opts.SenderBot.String()},
		{on: opts.SenderPremium.IsSet(), line: "client: sender_premium=" + opts.SenderPremium.String()},
		{on: opts.SenderVerified.IsSet(), line: "client: sender_verified=" + opts.SenderVerified.String()},
		{on: opts.SenderScam.IsSet(), line: "client: sender_scam=" + opts.SenderScam.String()},
		{on: opts.SenderDeleted.IsSet(), line: "client: sender_deleted=" + opts.SenderDeleted.String()},
		{on: opts.SenderNameRegex != "", line: "client: sender_name_regex=" + opts.SenderNameRegex},
		{on: opts.SenderUsernameRegex != "", line: "client: sender_username_regex=" + opts.SenderUsernameRegex},
		{on: opts.SenderPhoneRegex != "", line: "client: sender_phone_regex=" + opts.SenderPhoneRegex},
		{on: len(opts.Media) > 0, line: "client: media=" + strings.Join(opts.Media, ",")},
		{on: len(opts.StickerKind) > 0, line: "client: sticker_kind=" + strings.Join(opts.StickerKind, ",")},
		{on: opts.HasMedia.IsSet(), line: "client: has_media=" + opts.HasMedia.String()},
		{on: opts.InAlbum == "only" || opts.InAlbum == "first", line: "client: in_album=" + opts.InAlbum},
		{on: len(opts.Mime) > 0, line: "client: mime=" + strings.Join(opts.Mime, ",")},
		{on: len(opts.Ext) > 0, line: "client: ext=" + strings.Join(opts.Ext, ",")},
		{on: opts.NameGlob != "", line: "client: name_glob=" + opts.NameGlob},
		{on: opts.NameRegex != "", line: "client: name_regex=" + opts.NameRegex},
		{on: opts.MinSize != "", line: "client: min_size=" + opts.MinSize},
		{on: opts.MaxSize != "", line: "client: max_size=" + opts.MaxSize},
		{on: opts.MinDuration != "", line: "client: min_duration=" + opts.MinDuration},
		{on: opts.MaxDuration != "", line: "client: max_duration=" + opts.MaxDuration},
		{on: opts.MinWidth > 0, line: "client: min_width=" + strconv.Itoa(opts.MinWidth)},
		{on: opts.MaxWidth > 0, line: "client: max_width=" + strconv.Itoa(opts.MaxWidth)},
		{on: opts.MinHeight > 0, line: "client: min_height=" + strconv.Itoa(opts.MinHeight)},
		{on: opts.MaxHeight > 0, line: "client: max_height=" + strconv.Itoa(opts.MaxHeight)},
		{on: opts.MinMP > 0, line: "client: min_mp=" + strconv.FormatFloat(opts.MinMP, 'f', -1, 64)},
		{on: opts.Streamable.IsSet(), line: "client: streamable=" + opts.Streamable.String()},
		{on: opts.NoSound.IsSet(), line: "client: no_sound=" + opts.NoSound.String()},
		{on: opts.TextRegex != "", line: "client: text_regex=" + opts.TextRegex},
		{on: opts.HasText != "", line: "client: has_text=" + opts.HasText},
		{on: len(opts.Hashtag) > 0, line: "client: hashtag=" + strings.Join(opts.Hashtag, ",")},
		{on: opts.AnyHashtag, line: "client: any_hashtag=true"},
		{on: len(opts.Mention) > 0, line: "client: mention=" + strings.Join(opts.Mention, ",")},
		{on: opts.WasMentioned.IsSet(), line: "client: was_mentioned=" + opts.WasMentioned.String()},
		{on: opts.HasURL.IsSet(), line: "client: has_url=" + opts.HasURL.String()},
		{on: opts.URLRegex != "", line: "client: url_regex=" + opts.URLRegex},
		{on: opts.HasEmail, line: "client: has_email=true"},
		{on: opts.HasPhone, line: "client: has_phone=true"},
		{on: opts.Command != "", line: "client: command=" + opts.Command},
		{on: opts.EmojiOnly, line: "client: emoji_only=true"},
		{on: opts.After != "", line: "client: after=" + opts.After},
		{on: opts.Before != "", line: "client: before=" + opts.Before},
		{on: opts.Last != "", line: "client: last=" + opts.Last},
		{on: opts.OlderThan != "", line: "client: older_than=" + opts.OlderThan},
		{on: opts.Edited.IsSet(), line: "client: edited=" + opts.Edited.String()},
		{on: opts.Forwarded.IsSet(), line: "client: forwarded=" + opts.Forwarded.String()},
		{on: len(opts.FwdFrom) > 0, line: "client: fwd_from=" + strings.Join(opts.FwdFrom, ",")},
		{on: opts.FwdHidden.IsSet(), line: "client: fwd_hidden=" + opts.FwdHidden.String()},
		{on: opts.FwdDateAfter != "", line: "client: fwd_date_after=" + opts.FwdDateAfter},
		{on: opts.FwdDateBefore != "", line: "client: fwd_date_before=" + opts.FwdDateBefore},
		{on: opts.IsReply.IsSet(), line: "client: is_reply=" + opts.IsReply.String()},
		{on: opts.MinViews > 0, line: "client: min_views=" + strconv.FormatInt(opts.MinViews, 10)},
		{on: opts.MinForwards > 0, line: "client: min_forwards=" + strconv.FormatInt(opts.MinForwards, 10)},
		{on: opts.MinReactions > 0, line: "client: min_reactions=" + strconv.Itoa(opts.MinReactions)},
		{on: len(opts.Reaction) > 0, line: "client: reaction=" + strings.Join(opts.Reaction, ",")},
		{on: opts.Pinned.IsSet(), line: "client: pinned=" + opts.Pinned.String()},
		{on: opts.MinID > 0, line: "client: min_id=" + strconv.FormatInt(opts.MinID, 10)},
		{on: opts.MaxID > 0, line: "client: max_id=" + strconv.FormatInt(opts.MaxID, 10)},
		{on: opts.Service == "only" || opts.Service == "exclude", line: "client: service=" + opts.Service},
		{on: opts.Silent.IsSet(), line: "client: silent=" + opts.Silent.String()},
		{on: opts.HasSpoiler.IsSet(), line: "client: has_spoiler=" + opts.HasSpoiler.String()},
	}

	lines := make([]string, 0, len(rows)+len(push.Notes)+1)

	for _, row := range rows {
		if row.on {
			lines = append(lines, row.line)
		}
	}

	lines = append(lines, push.Notes...)
	lines = append(lines, clientNotes(opts)...)

	if executionConstrained(opts) {
		lines = append(lines, executionNote)
	}

	return lines
}

func executionConstrained(opts *Options) bool {
	return opts.Limit > 0 || opts.Reverse || opts.Order != "" || opts.Dedupe != "" || opts.SkipExisting ||
		opts.SinceState != "" || opts.Recursion != (Recursion{})
}

func normalizeHandle(handle string) string {
	return strings.TrimPrefix(strings.ToLower(handle), "@")
}

func lowerAll(values []string) []string {
	lowered := make([]string, len(values))
	for idx, value := range values {
		lowered[idx] = strings.ToLower(value)
	}

	return lowered
}

func normalizeExts(raw []string) []string {
	normalized := make([]string, len(raw))
	for idx, value := range raw {
		normalized[idx] = normalizeExt(value)
	}

	return normalized
}

func normalizeExt(raw string) string {
	return strings.ToLower(strings.TrimPrefix(raw, "."))
}

func anyGlobMatches(patterns []string, value string) bool {
	for _, pattern := range patterns {
		if matchGlob(pattern, value) {
			return true
		}
	}

	return false
}

func matchGlob(pattern, value string) bool {
	matched, err := path.Match(pattern, value)

	return err == nil && matched
}
