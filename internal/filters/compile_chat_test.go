package filters_test

import (
	"testing"

	"github.com/4q4r/teleparse/internal/filters"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompileChatPredicates(t *testing.T) {
	t.Parallel()

	groupCtx := baseContext()
	groupCtx.Chat = filters.Chat{ID: 11, Type: "supergroup", Title: "Team 42", Username: "team42"}

	botChatCtx := baseContext()
	botChatCtx.Chat.Type = "bot"

	botSenderCtx := baseContext()
	botSenderCtx.Sender.IsBot = true

	newsCtx := baseContext()
	newsCtx.Chat.Title = "News Hub"

	archivedCtx := baseContext()
	archivedCtx.Chat.Archived = true

	protectedCtx := baseContext()
	protectedCtx.Chat.Protected = true

	savedCtx := baseContext()
	savedCtx.Chat.Saved = true

	renamedCtx := baseContext()
	renamedCtx.Chat.Username = "team42"

	runPredicateCases(t, []predCase{
		{
			name: "chat type private matches",
			set:  func(o *filters.Options) { o.ChatType = []string{"private"} },
			ctx:  baseContext(), want: true,
		},
		{
			name: "chat type list rejects other kind",
			set:  func(o *filters.Options) { o.ChatType = []string{"private", "channel"} },
			ctx:  groupCtx, want: false,
		},
		{
			name: "chat type bot via chat kind",
			set:  func(o *filters.Options) { o.ChatType = []string{"bot"} },
			ctx:  botChatCtx, want: true,
		},
		{
			name: "chat type bot via sender flag in private chat",
			set:  func(o *filters.Options) { o.ChatType = []string{"bot"} },
			ctx:  botSenderCtx, want: true,
		},
		{
			name: "chat glob prefix matches title",
			set:  func(o *filters.Options) { o.ChatGlob = "News*" },
			ctx:  newsCtx, want: true,
		},
		{
			name: "chat glob rejects other title",
			set:  func(o *filters.Options) { o.ChatGlob = "Spam*" },
			ctx:  newsCtx, want: false,
		},
		{
			name: "chat regex matches title",
			set:  func(o *filters.Options) { o.ChatRegex = `^Team \d+$` },
			ctx:  groupCtx, want: true,
		},
		{
			name: "chat regex rejects title",
			set:  func(o *filters.Options) { o.ChatRegex = `^Team \d+$` },
			ctx:  newsCtx, want: false,
		},
		{
			name: "archived only matches archived chat",
			set:  func(o *filters.Options) { o.Archived = "only" },
			ctx:  archivedCtx, want: true,
		},
		{
			name: "archived only rejects live chat",
			set:  func(o *filters.Options) { o.Archived = "only" },
			ctx:  baseContext(), want: false,
		},
		{
			name: "archived exclude matches live chat",
			set:  func(o *filters.Options) { o.Archived = "exclude" },
			ctx:  baseContext(), want: true,
		},
		{
			name: "archived exclude rejects archived chat",
			set:  func(o *filters.Options) { o.Archived = "exclude" },
			ctx:  archivedCtx, want: false,
		},
		{
			name: "saved only matches saved messages chat",
			set:  func(o *filters.Options) { o.SavedOnly = true },
			ctx:  savedCtx, want: true,
		},
		{
			name: "saved only rejects regular chat",
			set:  func(o *filters.Options) { o.SavedOnly = true },
			ctx:  baseContext(), want: false,
		},
		{
			name: "chat username matches case insensitive with at sign",
			set:  func(o *filters.Options) { o.ChatUsername = "@ALICE" },
			ctx:  baseContext(), want: true,
		},
		{
			name: "chat username rejects other handle",
			set:  func(o *filters.Options) { o.ChatUsername = "bob" },
			ctx:  renamedCtx, want: false,
		},
		{
			name: "skip protected passes clean chat",
			set:  func(o *filters.Options) { o.SkipProtected = true },
			ctx:  baseContext(), want: true,
		},
		{
			name: "skip protected rejects protected chat",
			set:  func(o *filters.Options) { o.SkipProtected = true },
			ctx:  protectedCtx, want: false,
		},
	})
}

func TestCompileArchivedAnyAddsNoPredicate(t *testing.T) {
	t.Parallel()

	opts := &filters.Options{Archived: "any"}
	plan, err := filters.Compile(opts)
	require.NoError(t, err)
	assert.NotContains(t, predicateNames(plan), "archived")
}

func TestCompileEmptyChatTypeAddsNoPredicate(t *testing.T) {
	t.Parallel()

	plan, err := filters.Compile(&filters.Options{})
	require.NoError(t, err)
	assert.NotContains(t, predicateNames(plan), "chat_type")
	assert.True(t, planMatches(plan, baseContext()))
}
