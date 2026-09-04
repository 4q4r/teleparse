package filters_test

import (
	"teleparse/internal/filters"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompileMediaPredicates(t *testing.T) {
	t.Parallel()

	photoCtx := withFile(baseContext(), photoFile(500_000))
	noFileCtx := baseContext()
	webpageCtx := withFile(baseContext(), &filters.FileInfo{Present: true, Kind: "webpage"})
	docCtx := withFile(baseContext(), documentFile("report.pdf", ".pdf", "application/pdf", 1_000))
	staticStickerCtx := withFile(baseContext(), stickerFile("static"))
	animatedStickerCtx := withFile(baseContext(), stickerFile("animated"))
	albumCtx := baseContext()
	albumCtx.Message.GroupedID = 77
	soloCtx := baseContext()

	runPredicateCases(t, []predCase{
		{
			name: "media photo matches photo file",
			set:  func(o *filters.Options) { o.Media = []string{"photo"} },
			ctx:  photoCtx, want: true,
		},
		{
			name: "media photo rejects document file",
			set:  func(o *filters.Options) { o.Media = []string{"photo"} },
			ctx:  docCtx, want: false,
		},
		{
			name: "media requires file present",
			set:  func(o *filters.Options) { o.Media = []string{"photo"} },
			ctx:  noFileCtx, want: false,
		},
		{
			name: "sticker media matches any sticker kind",
			set:  func(o *filters.Options) { o.Media = []string{"sticker"} },
			ctx:  staticStickerCtx, want: true,
		},
		{
			name: "sticker kind filters out static",
			set:  func(o *filters.Options) { o.StickerKind = []string{"animated"} },
			ctx:  staticStickerCtx, want: false,
		},
		{
			name: "sticker kind matches animated",
			set:  func(o *filters.Options) { o.StickerKind = []string{"animated"} },
			ctx:  animatedStickerCtx, want: true,
		},
		{
			name: "sticker kind rejects non sticker file",
			set:  func(o *filters.Options) { o.StickerKind = []string{"animated"} },
			ctx:  photoCtx, want: false,
		},
		{
			name: "has media true with attached file",
			set:  func(o *filters.Options) { o.HasMedia = filters.Tri(true) },
			ctx:  photoCtx, want: true,
		},
		{
			name: "has media true counts webpage preview",
			set:  func(o *filters.Options) { o.HasMedia = filters.Tri(true) },
			ctx:  webpageCtx, want: true,
		},
		{
			name: "has media true rejects bare text",
			set:  func(o *filters.Options) { o.HasMedia = filters.Tri(true) },
			ctx:  noFileCtx, want: false,
		},
		{
			name: "has media false matches bare text",
			set:  func(o *filters.Options) { o.HasMedia = filters.Tri(false) },
			ctx:  noFileCtx, want: true,
		},
		{
			name: "has media false rejects attached file",
			set:  func(o *filters.Options) { o.HasMedia = filters.Tri(false) },
			ctx:  photoCtx, want: false,
		},
		{
			name: "in album only matches grouped message",
			set:  func(o *filters.Options) { o.InAlbum = "only" },
			ctx:  albumCtx, want: true,
		},
		{
			name: "in album only rejects solo message",
			set:  func(o *filters.Options) { o.InAlbum = "only" },
			ctx:  soloCtx, want: false,
		},
	})
}

func TestCompileHasMediaAndAlbumAnyAddNoPredicates(t *testing.T) {
	t.Parallel()

	opts := &filters.Options{InAlbum: "any"}
	plan, err := filters.Compile(opts)
	require.NoError(t, err)

	names := predicateNames(plan)
	assert.NotContains(t, names, "has_media")
	assert.NotContains(t, names, "in_album")
}

func TestCompileInAlbumFirstPassesAllAndNotes(t *testing.T) {
	t.Parallel()

	opts := &filters.Options{InAlbum: "first"}
	plan, err := filters.Compile(opts)
	require.NoError(t, err)

	assert.NotContains(t, predicateNames(plan), "in_album")
	assert.Contains(t, plan.Pushdown.Notes, "album=first: per-message pass-all; scan coalesces")

	albumCtx := baseContext()
	albumCtx.Message.GroupedID = 77
	assert.True(t, planMatches(plan, albumContext()))
	assert.True(t, planMatches(plan, baseContext()))
}

func albumContext() *filters.Context {
	ctx := baseContext()
	ctx.Message.GroupedID = 77

	return ctx
}

func TestCompilePushdownMessagesFilterMatrix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		media      []string
		wantFilter string
		wantNotes  bool
	}{
		{name: "single photo pushes photo", media: []string{"photo"}, wantFilter: "photo"},
		{name: "photo video pair pushes photo_video", media: []string{"video", "photo"}, wantFilter: "photo_video"},
		{name: "three kinds have no server filter", media: []string{"photo", "video", "document"}, wantFilter: ""},
		{name: "sticker has no server filter", media: []string{"sticker"}, wantFilter: ""},
		{name: "audio maps to music", media: []string{"audio"}, wantFilter: "music"},
		{name: "video note maps to round video", media: []string{"video-note"}, wantFilter: "round_video"},
		{name: "webpage maps to url", media: []string{"webpage"}, wantFilter: "url"},
		{name: "no media means no filter", media: nil, wantFilter: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opts := &filters.Options{Media: tc.media}
			plan, err := filters.Compile(opts)
			require.NoError(t, err)
			assert.Equal(t, tc.wantFilter, plan.Pushdown.MessagesFilter)
			assert.Empty(t, plan.Pushdown.Notes)
		})
	}
}

func TestCompilePushdownDateBounds(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, time.January, 2, 15, 4, 5, 0, time.UTC)
	before := time.Date(2026, time.March, 4, 0, 0, 0, 0, time.UTC)

	opts := &filters.Options{After: "2026-01-02T15:04:05Z", Before: "2026-03-04"}
	plan, err := filters.Compile(opts)
	require.NoError(t, err)
	assert.Equal(t, after.Unix(), plan.Pushdown.MinDate)
	assert.Equal(t, before.Unix(), plan.Pushdown.MaxDate)

	relOpts := &filters.Options{Last: "1h", OlderThan: "2h"}
	relPlan, err := filters.Compile(relOpts)
	require.NoError(t, err)
	assert.InDelta(t, time.Now().Add(-time.Hour).Unix(), relPlan.Pushdown.MinDate, 5)
	assert.InDelta(t, time.Now().Add(-2*time.Hour).Unix(), relPlan.Pushdown.MaxDate, 5)
}

func TestCompilePushdownFromUsersNotes(t *testing.T) {
	t.Parallel()

	defaultOpts := &filters.Options{FromUsers: []string{"20"}}
	defaultPlan, err := filters.Compile(defaultOpts)
	require.NoError(t, err)
	assert.Equal(t, []string{"20"}, defaultPlan.Pushdown.FromUsers)
	assert.Contains(t, defaultPlan.Pushdown.Notes, "server ignores from_id in private chats")

	privatePlan, err := filters.Compile(&filters.Options{
		FromUsers: []string{"20"}, ChatType: []string{"private"},
	})
	require.NoError(t, err)
	assert.Contains(t, privatePlan.Pushdown.Notes, "server ignores from_id in private chats")

	groupPlan, err := filters.Compile(&filters.Options{
		FromUsers: []string{"20"}, ChatType: []string{"group"},
	})
	require.NoError(t, err)
	assert.NotContains(t, groupPlan.Pushdown.Notes, "server ignores from_id in private chats")

	barePlan, err := filters.Compile(&filters.Options{})
	require.NoError(t, err)
	assert.NotContains(t, barePlan.Pushdown.Notes, "server ignores from_id in private chats")
}

func TestCompilePushdownTopicsNote(t *testing.T) {
	t.Parallel()

	opts := &filters.Options{Media: []string{"photo"}}
	opts.Recursion.Topics = true
	plan, err := filters.Compile(opts)
	require.NoError(t, err)
	assert.Equal(t, "photo", plan.Pushdown.MessagesFilter)
	assert.Contains(t, plan.Pushdown.Notes, "media filter inactive inside forum-topic iteration")

	noMediaOpts := &filters.Options{}
	noMediaOpts.Recursion.Topics = true
	noMediaPlan, err := filters.Compile(noMediaOpts)
	require.NoError(t, err)
	assert.NotContains(t, noMediaPlan.Pushdown.Notes, "media filter inactive inside forum-topic iteration")

	noTopicsPlan, err := filters.Compile(&filters.Options{Media: []string{"photo"}})
	require.NoError(t, err)
	assert.NotContains(t, noTopicsPlan.Pushdown.Notes, "media filter inactive inside forum-topic iteration")
}

func TestCompileExplainLines(t *testing.T) {
	t.Parallel()

	opts := &filters.Options{
		Media:     []string{"photo"},
		ChatType:  []string{"private"},
		MinSize:   "1MB",
		FromUsers: []string{"20"},
		HasMedia:  filters.Tri(true),
		Limit:     50,
	}
	plan, err := filters.Compile(opts)
	require.NoError(t, err)

	assert.Contains(t, plan.ExplainLines, "server: messages_filter=photo")
	assert.Contains(t, plan.ExplainLines, "server: from_users=20")
	assert.Contains(t, plan.ExplainLines, "client: chat_type=private")
	assert.Contains(t, plan.ExplainLines, "client: min_size=1MB")
	assert.Contains(t, plan.ExplainLines, "client: has_media=true")
	assert.Contains(t, plan.ExplainLines, "server ignores from_id in private chats")
	assert.Contains(t, plan.ExplainLines,
		"execution: limit/reverse/order/dedupe/skip-existing/since-state/recursion affect scanning, not matching")
}
