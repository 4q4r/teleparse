package filters

import (
	"regexp"
	"strings"
	"unicode"
)

// textPredicates covers text presence, message entities and text-shape
// filters. Entity lookups are nil-safe by construction: empty entity slices
// simply match nothing.
func textPredicates(opts *Options, set regexSet) []NamedPredicate {
	var preds []NamedPredicate

	if set.text != nil {
		preds = append(preds, NamedPredicate{Name: "text_regex", Fn: func(ctx *Context) bool {
			return set.text.MatchString(ctx.TextOrCaption())
		}})
	}

	if opts.HasText != "" {
		preds = append(preds, NamedPredicate{Name: "has_text", Fn: func(ctx *Context) bool {
			return hasTextMatches(opts.HasText, ctx)
		}})
	}

	if len(opts.Hashtag) > 0 {
		preds = append(preds, NamedPredicate{Name: "hashtag", Fn: func(ctx *Context) bool {
			return hashtagsMatch(opts.Hashtag, ctx.Message.Entities)
		}})
	}

	if opts.AnyHashtag {
		preds = append(preds, NamedPredicate{Name: "any_hashtag", Fn: func(ctx *Context) bool {
			return hasEntityKind(ctx.Message.Entities, "hashtag")
		}})
	}

	if len(opts.Mention) > 0 {
		preds = append(preds, NamedPredicate{Name: "mention", Fn: func(ctx *Context) bool {
			return mentionsMatch(opts.Mention, ctx.Message.Entities)
		}})
	}

	if opts.WasMentioned.IsSet() {
		preds = append(preds, triPredicate("was_mentioned", opts.WasMentioned.Value(), func(ctx *Context) bool {
			return ctx.Message.Mentioned
		}))
	}

	if opts.HasURL.IsSet() {
		want := opts.HasURL.Value()

		preds = append(preds, NamedPredicate{Name: "has_url", Fn: func(ctx *Context) bool {
			return hasURLValue(ctx) == want
		}})
	}

	if set.url != nil {
		preds = append(preds, NamedPredicate{Name: "url_regex", Fn: func(ctx *Context) bool {
			return urlEntitiesMatch(set.url, ctx.Message.Entities)
		}})
	}

	if opts.HasEmail {
		preds = append(preds, NamedPredicate{Name: "has_email", Fn: func(ctx *Context) bool {
			return hasEntityKind(ctx.Message.Entities, "email")
		}})
	}

	if opts.HasPhone {
		preds = append(preds, NamedPredicate{Name: "has_phone", Fn: func(ctx *Context) bool {
			return hasEntityKind(ctx.Message.Entities, "phone")
		}})
	}

	if opts.Command != "" {
		preds = append(preds, NamedPredicate{Name: "command", Fn: func(ctx *Context) bool {
			return commandMatches(opts.Command, ctx.Message.Entities)
		}})
	}

	if opts.EmojiOnly {
		preds = append(preds, NamedPredicate{Name: "emoji_only", Fn: func(ctx *Context) bool {
			return emojiOnly(ctx.Message.Text)
		}})
	}

	return preds
}

// clientNotes documents client-side matcher caveats for --explain.
func clientNotes(opts *Options) []string {
	if !opts.EmojiOnly {
		return nil
	}

	return []string{"emoji-only: code/pre entity ranges are not excluded from the letter scan"}
}

func hasTextMatches(mode string, ctx *Context) bool {
	switch mode {
	case "any":
		return ctx.TextOrCaption() != ""
	case "none":
		return ctx.TextOrCaption() == ""
	case "only":
		_, hasFile := fileOf(ctx)

		return ctx.TextOrCaption() != "" && !hasFile
	default:
		return true
	}
}

// hashtagsMatch requires every entry to be present; "*" demands any hashtag.
func hashtagsMatch(entries []string, entities []Entity) bool {
	for _, entry := range entries {
		if entry == "*" {
			if !hasEntityKind(entities, "hashtag") {
				return false
			}

			continue
		}

		if !entitiesContainTag(entities, entry) {
			return false
		}
	}

	return true
}

func entitiesContainTag(entities []Entity, want string) bool {
	for _, entity := range entities {
		if entity.Kind != "hashtag" {
			continue
		}

		if strings.EqualFold(stripHash(entity.Text), stripHash(want)) {
			return true
		}
	}

	return false
}

// mentionsMatch is any-of: one listed handle (or "*" for any mention) suffices.
func mentionsMatch(entries []string, entities []Entity) bool {
	for _, entry := range entries {
		if entitiesContainMention(entities, entry) {
			return true
		}
	}

	return false
}

func entitiesContainMention(entities []Entity, want string) bool {
	for _, entity := range entities {
		if entity.Kind != "mention" && entity.Kind != "text_mention" {
			continue
		}

		if want == "*" ||
			strings.EqualFold(strings.TrimPrefix(entity.Text, "@"), strings.TrimPrefix(want, "@")) {
			return true
		}
	}

	return false
}

func hasURLValue(ctx *Context) bool {
	if hasEntityKind(ctx.Message.Entities, "url") || hasEntityKind(ctx.Message.Entities, "text_link") {
		return true
	}

	return ctx.File != nil && ctx.File.Kind == "webpage"
}

func urlEntitiesMatch(url *regexp.Regexp, entities []Entity) bool {
	for _, entity := range entities {
		if entity.Kind != "url" && entity.Kind != "text_link" {
			continue
		}

		if url.MatchString(entity.Data) {
			return true
		}
	}

	return false
}

func commandMatches(want string, entities []Entity) bool {
	for _, entity := range entities {
		if entity.Kind == "bot_command" && entity.Text == want {
			return true
		}
	}

	return false
}

// emojiOnly reports non-empty text made solely of non-letter, non-digit
// runes; whitespace is allowed. Link-preview URLs are not part of Context
// and therefore never contribute runes here.
func emojiOnly(text string) bool {
	if text == "" {
		return false
	}

	for _, glyph := range text {
		if unicode.IsLetter(glyph) || unicode.IsDigit(glyph) {
			return false
		}
	}

	return true
}

func hasEntityKind(entities []Entity, kind string) bool {
	for _, entity := range entities {
		if entity.Kind == kind {
			return true
		}
	}

	return false
}

func stripHash(value string) string {
	return strings.TrimPrefix(value, "#")
}
