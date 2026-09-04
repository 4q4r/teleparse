package filters_test

import (
	"teleparse/internal/filters"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func entity(kind, text string) filters.Entity {
	return filters.Entity{Kind: kind, Text: text}
}

func linkEntity(kind, text, data string) filters.Entity {
	return filters.Entity{Kind: kind, Text: text, Data: data}
}

func textContext(text string, entities ...filters.Entity) *filters.Context {
	ctx := baseContext()
	ctx.Message.Text = text
	ctx.Message.Entities = entities

	return ctx
}

func captionContext(caption string, entities ...filters.Entity) *filters.Context {
	ctx := baseContext()
	ctx.Message.Text = ""
	ctx.Message.Caption = caption
	ctx.Message.Entities = entities

	return ctx
}

func TestCompileTextPredicates(t *testing.T) {
	t.Parallel()

	emptyCtx := baseContext()
	emptyCtx.Message.Text = ""
	emptyCtx.Message.Caption = ""

	runPredicateCases(t, []predCase{
		{
			name: "text regex matches text",
			set:  func(o *filters.Options) { o.TextRegex = `^hel` },
			ctx:  baseContext(), want: true,
		},
		{
			name: "text regex rejects text",
			set:  func(o *filters.Options) { o.TextRegex = `^zzz` },
			ctx:  baseContext(), want: false,
		},
		{
			name: "text regex falls back to caption",
			set:  func(o *filters.Options) { o.TextRegex = `ready$` },
			ctx:  captionContext("report ready"), want: true,
		},
		{
			name: "has text any matches non-empty text",
			set:  func(o *filters.Options) { o.HasText = "any" },
			ctx:  baseContext(), want: true,
		},
		{
			name: "has text any matches caption only",
			set:  func(o *filters.Options) { o.HasText = "any" },
			ctx:  captionContext("see this"), want: true,
		},
		{
			name: "has text none matches empty message",
			set:  func(o *filters.Options) { o.HasText = "none" },
			ctx:  emptyCtx, want: true,
		},
		{
			name: "has text none rejects text",
			set:  func(o *filters.Options) { o.HasText = "none" },
			ctx:  baseContext(), want: false,
		},
		{
			name: "has text only matches bare text",
			set:  func(o *filters.Options) { o.HasText = "only" },
			ctx:  baseContext(), want: true,
		},
		{
			name: "has text only rejects text with file",
			set:  func(o *filters.Options) { o.HasText = "only" },
			ctx:  withFile(baseContext(), photoFile(1000)), want: false,
		},
		{
			name: "hashtag matches exact tag",
			set:  func(o *filters.Options) { o.Hashtag = []string{"news"} },
			ctx:  textContext("big #news", entity("hashtag", "#news")), want: true,
		},
		{
			name: "hashtag matches case insensitive",
			set:  func(o *filters.Options) { o.Hashtag = []string{"News"} },
			ctx:  textContext("big #news", entity("hashtag", "#news")), want: true,
		},
		{
			name: "hashtag strips leading hash from entry",
			set:  func(o *filters.Options) { o.Hashtag = []string{"#news"} },
			ctx:  textContext("big #news", entity("hashtag", "#news")), want: true,
		},
		{
			name: "hashtag star matches any hashtag",
			set:  func(o *filters.Options) { o.Hashtag = []string{"*"} },
			ctx:  textContext("wow #tech", entity("hashtag", "#tech")), want: true,
		},
		{
			name: "hashtag star rejects message without hashtags",
			set:  func(o *filters.Options) { o.Hashtag = []string{"*"} },
			ctx:  baseContext(), want: false,
		},
		{
			name: "hashtag requires every entry present",
			set:  func(o *filters.Options) { o.Hashtag = []string{"news", "tech"} },
			ctx:  textContext("big #news", entity("hashtag", "#news")), want: false,
		},
		{
			name: "hashtag rejects absent tag",
			set:  func(o *filters.Options) { o.Hashtag = []string{"tech"} },
			ctx:  textContext("big #news", entity("hashtag", "#news")), want: false,
		},
		{
			name: "any hashtag matches presence",
			set:  func(o *filters.Options) { o.AnyHashtag = true },
			ctx:  textContext("wow #tech", entity("hashtag", "#tech")), want: true,
		},
		{
			name: "any hashtag rejects absence",
			set:  func(o *filters.Options) { o.AnyHashtag = true },
			ctx:  baseContext(), want: false,
		},
		{
			name: "mention matches handle case insensitive",
			set:  func(o *filters.Options) { o.Mention = []string{"@Alice"} },
			ctx:  textContext("hi @alice", entity("mention", "@alice")), want: true,
		},
		{
			name: "mention matches text mention entity",
			set:  func(o *filters.Options) { o.Mention = []string{"bob"} },
			ctx:  textContext("hi bob", entity("text_mention", "bob")), want: true,
		},
		{
			name: "mention star matches any mention",
			set:  func(o *filters.Options) { o.Mention = []string{"*"} },
			ctx:  textContext("hi @carol", entity("mention", "@carol")), want: true,
		},
		{
			name: "mention rejects absent handle",
			set:  func(o *filters.Options) { o.Mention = []string{"bob"} },
			ctx:  textContext("hi @alice", entity("mention", "@alice")), want: false,
		},
		{
			name: "was mentioned true uses flag not entities",
			set:  func(o *filters.Options) { o.WasMentioned = filters.Tri(true) },
			ctx:  flaggedContext(func(msg *filters.Message) { msg.Mentioned = true }), want: true,
		},
		{
			name: "was mentioned true rejects clear flag",
			set:  func(o *filters.Options) { o.WasMentioned = filters.Tri(true) },
			ctx:  baseContext(), want: false,
		},
		{
			name: "was mentioned false ignores mention entities",
			set:  func(o *filters.Options) { o.WasMentioned = filters.Tri(false) },
			ctx:  textContext("hi @alice", entity("mention", "@alice")), want: true,
		},
		{
			name: "has url true via url entity",
			set:  func(o *filters.Options) { o.HasURL = filters.Tri(true) },
			ctx:  textContext("go https://example.com", entity("url", "https://example.com")), want: true,
		},
		{
			name: "has url true via text link entity",
			set:  func(o *filters.Options) { o.HasURL = filters.Tri(true) },
			ctx:  textContext("go", linkEntity("text_link", "go", "https://example.com")), want: true,
		},
		{
			name: "has url true via webpage file",
			set:  func(o *filters.Options) { o.HasURL = filters.Tri(true) },
			ctx:  withFile(baseContext(), &filters.FileInfo{Present: true, Kind: "webpage"}), want: true,
		},
		{
			name: "has url false matches bare message",
			set:  func(o *filters.Options) { o.HasURL = filters.Tri(false) },
			ctx:  baseContext(), want: true,
		},
		{
			name: "has url false rejects url entity",
			set:  func(o *filters.Options) { o.HasURL = filters.Tri(false) },
			ctx:  textContext("go", entity("url", "https://example.com")), want: false,
		},
		{
			name: "url regex matches text link data",
			set:  func(o *filters.Options) { o.URLRegex = `example\.com/page$` },
			ctx:  textContext("go", linkEntity("text_link", "go", "https://example.com/page")), want: true,
		},
		{
			name: "url regex rejects non-matching data",
			set:  func(o *filters.Options) { o.URLRegex = `example\.org` },
			ctx:  textContext("go", linkEntity("text_link", "go", "https://example.com/page")), want: false,
		},
		{
			name: "has email matches email entity",
			set:  func(o *filters.Options) { o.HasEmail = true },
			ctx:  textContext("mail a@b.io", entity("email", "a@b.io")), want: true,
		},
		{
			name: "has email rejects absence",
			set:  func(o *filters.Options) { o.HasEmail = true },
			ctx:  baseContext(), want: false,
		},
		{
			name: "has phone matches phone entity",
			set:  func(o *filters.Options) { o.HasPhone = true },
			ctx:  textContext("call +15550100", entity("phone", "+15550100")), want: true,
		},
		{
			name: "has phone rejects absence",
			set:  func(o *filters.Options) { o.HasPhone = true },
			ctx:  baseContext(), want: false,
		},
		{
			name: "command matches exact bot command",
			set:  func(o *filters.Options) { o.Command = "/start" },
			ctx:  textContext("/start", entity("bot_command", "/start")), want: true,
		},
		{
			name: "command rejects different command",
			set:  func(o *filters.Options) { o.Command = "/start" },
			ctx:  textContext("/help", entity("bot_command", "/help")), want: false,
		},
		{
			name: "command rejects message without command entity",
			set:  func(o *filters.Options) { o.Command = "/start" },
			ctx:  baseContext(), want: false,
		},
	})
}

func flaggedContext(mutate func(*filters.Message)) *filters.Context {
	ctx := baseContext()
	mutate(&ctx.Message)

	return ctx
}

func TestCompileEmojiOnly(t *testing.T) {
	t.Parallel()

	runPredicateCases(t, []predCase{
		{
			name: "emoji only matches pure emoji",
			set:  func(o *filters.Options) { o.EmojiOnly = true },
			ctx:  textContext("🔥🔥"), want: true,
		},
		{
			name: "emoji only allows whitespace",
			set:  func(o *filters.Options) { o.EmojiOnly = true },
			ctx:  textContext("🔥 👍 😀"), want: true,
		},
		{
			name: "emoji only rejects letters",
			set:  func(o *filters.Options) { o.EmojiOnly = true },
			ctx:  textContext("🔥a🔥"), want: false,
		},
		{
			name: "emoji only rejects digits",
			set:  func(o *filters.Options) { o.EmojiOnly = true },
			ctx:  textContext("123"), want: false,
		},
		{
			name: "emoji only rejects empty text",
			set:  func(o *filters.Options) { o.EmojiOnly = true },
			ctx:  textContext(""), want: false,
		},
		{
			name: "emoji only ignores caption-only messages",
			set:  func(o *filters.Options) { o.EmojiOnly = true },
			ctx:  captionContext("🔥"), want: false,
		},
	})
}

func TestCompileEmojiOnlyExplainNote(t *testing.T) {
	t.Parallel()

	plan, err := filters.Compile(&filters.Options{EmojiOnly: true})
	require.NoError(t, err)
	assert.Contains(t, plan.ExplainLines, "client: emoji_only=true")
	assert.Contains(t, plan.ExplainLines, "emoji-only: code/pre entity ranges are not excluded from the letter scan")

	plain, err := filters.Compile(&filters.Options{})
	require.NoError(t, err)
	assert.NotContains(t, plain.ExplainLines, "emoji-only: code/pre entity ranges are not excluded from the letter scan")
}

func TestCompileTextExplainLines(t *testing.T) {
	t.Parallel()

	opts := &filters.Options{
		TextRegex:    `^hel`,
		HasText:      "only",
		Hashtag:      []string{"news"},
		Mention:      []string{"alice"},
		WasMentioned: filters.Tri(true),
		URLRegex:     `example\.com`,
		Command:      "/start",
	}
	plan, err := filters.Compile(opts)
	require.NoError(t, err)

	assert.Contains(t, plan.ExplainLines, "client: text_regex=^hel")
	assert.Contains(t, plan.ExplainLines, "client: has_text=only")
	assert.Contains(t, plan.ExplainLines, "client: hashtag=news")
	assert.Contains(t, plan.ExplainLines, "client: mention=alice")
	assert.Contains(t, plan.ExplainLines, "client: was_mentioned=true")
	assert.Contains(t, plan.ExplainLines, "client: url_regex=example\\.com")
	assert.Contains(t, plan.ExplainLines, "client: command=/start")
}

func TestCompileTextRelativeNow(t *testing.T) {
	t.Parallel()

	freshCtx := flaggedContext(func(msg *filters.Message) { msg.Date = time.Now().Add(-30 * time.Minute).Unix() })

	runPredicateCases(t, []predCase{
		{
			name: "url regex over url entity data",
			set:  func(o *filters.Options) { o.URLRegex = `^https://` },
			ctx:  textContext("go", linkEntity("url", "https://example.com", "https://example.com")), want: true,
		},
		{
			name: "text regex matches freshly stamped message",
			set:  func(o *filters.Options) { o.TextRegex = `hell` },
			ctx:  freshCtx, want: true,
		},
	})
}
