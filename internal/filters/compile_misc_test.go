package filters_test

import (
	"teleparse/internal/filters"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompileEngagementPredicates(t *testing.T) {
	t.Parallel()

	replyCtx := flaggedContext(func(msg *filters.Message) { msg.Reply = &filters.ReplyInfo{Present: true, ToMsgID: 99} })
	viewCtx := flaggedContext(func(msg *filters.Message) { msg.Views = 100 })
	forwardCtx := flaggedContext(func(msg *filters.Message) { msg.Forwards = 7 })
	reactionCtx := flaggedContext(func(msg *filters.Message) {
		msg.Reactions = []filters.Reaction{{Emoji: "👍", Count: 2}}
		msg.TotalReactions = 5
	})
	pinnedCtx := flaggedContext(func(msg *filters.Message) { msg.Pinned = true })

	runPredicateCases(t, []predCase{
		{
			name: "is reply true matches reply",
			set:  func(o *filters.Options) { o.IsReply = filters.Tri(true) },
			ctx:  replyCtx, want: true,
		},
		{
			name: "is reply true rejects plain message",
			set:  func(o *filters.Options) { o.IsReply = filters.Tri(true) },
			ctx:  baseContext(), want: false,
		},
		{
			name: "is reply false matches plain message",
			set:  func(o *filters.Options) { o.IsReply = filters.Tri(false) },
			ctx:  baseContext(), want: true,
		},
		{
			name: "min views inclusive at boundary",
			set:  func(o *filters.Options) { o.MinViews = 100 },
			ctx:  viewCtx, want: true,
		},
		{
			name: "min views rejects below boundary",
			set:  func(o *filters.Options) { o.MinViews = 101 },
			ctx:  viewCtx, want: false,
		},
		{
			name: "min forwards inclusive at boundary",
			set:  func(o *filters.Options) { o.MinForwards = 7 },
			ctx:  forwardCtx, want: true,
		},
		{
			name: "min forwards rejects below boundary",
			set:  func(o *filters.Options) { o.MinForwards = 8 },
			ctx:  forwardCtx, want: false,
		},
		{
			name: "min reactions inclusive at boundary",
			set:  func(o *filters.Options) { o.MinReactions = 5 },
			ctx:  reactionCtx, want: true,
		},
		{
			name: "min reactions rejects below boundary",
			set:  func(o *filters.Options) { o.MinReactions = 6 },
			ctx:  reactionCtx, want: false,
		},
		{
			name: "reaction any of matches present emoji",
			set:  func(o *filters.Options) { o.Reaction = []string{"🔥", "👍"} },
			ctx:  reactionCtx, want: true,
		},
		{
			name: "reaction rejects absent emoji",
			set:  func(o *filters.Options) { o.Reaction = []string{"🔥"} },
			ctx:  reactionCtx, want: false,
		},
		{
			name: "pinned true matches pinned message",
			set:  func(o *filters.Options) { o.Pinned = filters.Tri(true) },
			ctx:  pinnedCtx, want: true,
		},
		{
			name: "pinned false rejects pinned message",
			set:  func(o *filters.Options) { o.Pinned = filters.Tri(false) },
			ctx:  pinnedCtx, want: false,
		},
		{
			name: "pinned false matches unpinned message",
			set:  func(o *filters.Options) { o.Pinned = filters.Tri(false) },
			ctx:  baseContext(), want: true,
		},
	})
}

func TestCompileMiscPredicates(t *testing.T) {
	t.Parallel()

	serviceCtx := flaggedContext(func(msg *filters.Message) { msg.Service = true })
	silentCtx := flaggedContext(func(msg *filters.Message) { msg.Silent = true })
	spoilerCtx := flaggedContext(func(msg *filters.Message) { msg.Spoiler = true })

	runPredicateCases(t, []predCase{
		{
			name: "min id inclusive at boundary",
			set:  func(o *filters.Options) { o.MinID = 100 },
			ctx:  baseContext(), want: true,
		},
		{
			name: "min id rejects below boundary",
			set:  func(o *filters.Options) { o.MinID = 101 },
			ctx:  baseContext(), want: false,
		},
		{
			name: "max id inclusive at boundary",
			set:  func(o *filters.Options) { o.MaxID = 100 },
			ctx:  baseContext(), want: true,
		},
		{
			name: "max id rejects above boundary",
			set:  func(o *filters.Options) { o.MaxID = 99 },
			ctx:  baseContext(), want: false,
		},
		{
			name: "min and max id window matches inside",
			set:  func(o *filters.Options) { o.MinID = 50; o.MaxID = 200 },
			ctx:  baseContext(), want: true,
		},
		{
			name: "service only matches service message",
			set:  func(o *filters.Options) { o.Service = "only" },
			ctx:  serviceCtx, want: true,
		},
		{
			name: "service only rejects regular message",
			set:  func(o *filters.Options) { o.Service = "only" },
			ctx:  baseContext(), want: false,
		},
		{
			name: "service exclude matches regular message",
			set:  func(o *filters.Options) { o.Service = "exclude" },
			ctx:  baseContext(), want: true,
		},
		{
			name: "service exclude rejects service message",
			set:  func(o *filters.Options) { o.Service = "exclude" },
			ctx:  serviceCtx, want: false,
		},
		{
			name: "silent true matches silent message",
			set:  func(o *filters.Options) { o.Silent = filters.Tri(true) },
			ctx:  silentCtx, want: true,
		},
		{
			name: "silent true rejects audible message",
			set:  func(o *filters.Options) { o.Silent = filters.Tri(true) },
			ctx:  baseContext(), want: false,
		},
		{
			name: "silent false matches audible message",
			set:  func(o *filters.Options) { o.Silent = filters.Tri(false) },
			ctx:  baseContext(), want: true,
		},
		{
			name: "spoiler true matches covered media",
			set:  func(o *filters.Options) { o.HasSpoiler = filters.Tri(true) },
			ctx:  spoilerCtx, want: true,
		},
		{
			name: "spoiler true rejects uncovered media",
			set:  func(o *filters.Options) { o.HasSpoiler = filters.Tri(true) },
			ctx:  baseContext(), want: false,
		},
		{
			name: "spoiler false matches uncovered media",
			set:  func(o *filters.Options) { o.HasSpoiler = filters.Tri(false) },
			ctx:  baseContext(), want: true,
		},
	})
}

func TestCompileServiceAnyAddsNoPredicate(t *testing.T) {
	t.Parallel()

	plan, err := filters.Compile(&filters.Options{Service: "any"})
	require.NoError(t, err)
	assert.NotContains(t, predicateNames(plan), "service")
}

func TestCompileMiscUnsetAddNoPredicates(t *testing.T) {
	t.Parallel()

	plan, err := filters.Compile(&filters.Options{})
	require.NoError(t, err)

	names := predicateNames(plan)
	assert.NotContains(t, names, "is_reply")
	assert.NotContains(t, names, "min_views")
	assert.NotContains(t, names, "min_forwards")
	assert.NotContains(t, names, "min_reactions")
	assert.NotContains(t, names, "reaction")
	assert.NotContains(t, names, "pinned")
	assert.NotContains(t, names, "min_id")
	assert.NotContains(t, names, "max_id")
	assert.NotContains(t, names, "silent")
	assert.NotContains(t, names, "has_spoiler")
}

func TestCompileMiscExplainLines(t *testing.T) {
	t.Parallel()

	opts := &filters.Options{
		IsReply:      filters.Tri(true),
		MinViews:     1000,
		MinForwards:  10,
		MinReactions: 3,
		Reaction:     []string{"🔥"},
		Pinned:       filters.Tri(true),
		MinID:        500,
		MaxID:        900,
		Service:      "exclude",
		Silent:       filters.Tri(false),
		HasSpoiler:   filters.Tri(true),
	}
	plan, err := filters.Compile(opts)
	require.NoError(t, err)

	assert.Contains(t, plan.ExplainLines, "client: is_reply=true")
	assert.Contains(t, plan.ExplainLines, "client: min_views=1000")
	assert.Contains(t, plan.ExplainLines, "client: min_forwards=10")
	assert.Contains(t, plan.ExplainLines, "client: min_reactions=3")
	assert.Contains(t, plan.ExplainLines, "client: reaction=🔥")
	assert.Contains(t, plan.ExplainLines, "client: pinned=true")
	assert.Contains(t, plan.ExplainLines, "client: min_id=500")
	assert.Contains(t, plan.ExplainLines, "client: max_id=900")
	assert.Contains(t, plan.ExplainLines, "client: service=exclude")
	assert.Contains(t, plan.ExplainLines, "client: silent=false")
	assert.Contains(t, plan.ExplainLines, "client: has_spoiler=true")
}
