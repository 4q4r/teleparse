package filters_test

import (
	"strings"
	"testing"

	"github.com/4q4r/teleparse/internal/filters"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompileExcludeOverridesInclude(t *testing.T) {
	t.Parallel()

	videoCtx := baseContext()
	videoCtx.File = videoFile(2_000)

	photoCtx := baseContext()
	photoCtx.File = photoFile(3_000)

	zipCtx := baseContext()
	zipCtx.File = documentFile("arch.zip", ".zip", "application/zip", 1_000)

	rarCtx := baseContext()
	rarCtx.File = documentFile("arch.rar", ".rar", "application/vnd.rar", 1_000)

	channelCtx := baseContext()
	channelCtx.Chat = filters.Chat{ID: 40, Type: "channel", Title: "News Hub"}

	runPredicateCases(t, []predCase{
		{
			name: "media include plus same exclude drops the file",
			set: func(o *filters.Options) {
				o.Media = []string{"video"}
				o.ExcludeMedia = []string{"video"}
			},
			ctx: videoCtx, want: false,
		},
		{
			name: "media include keeps sibling kind under exclude",
			set: func(o *filters.Options) {
				o.Media = []string{"photo", "video"}
				o.ExcludeMedia = []string{"video"}
			},
			ctx: photoCtx, want: true,
		},
		{
			name: "mime glob include plus specific mime exclude drops match",
			set: func(o *filters.Options) {
				o.Mime = []string{"video/*"}
				o.ExcludeMime = []string{"video/mp4"}
			},
			ctx: videoCtx, want: false,
		},
		{
			name: "ext include plus ext exclude drops listed extension",
			set: func(o *filters.Options) {
				o.Ext = []string{".zip", ".rar"}
				o.ExcludeExt = []string{".zip"}
			},
			ctx: zipCtx, want: false,
		},
		{
			name: "ext include keeps sibling extension under exclude",
			set: func(o *filters.Options) {
				o.Ext = []string{".zip", ".rar"}
				o.ExcludeExt = []string{".zip"}
			},
			ctx: rarCtx, want: true,
		},
		{
			name: "chat type include plus exclude narrows the list",
			set: func(o *filters.Options) {
				o.ChatType = []string{"private", "channel"}
				o.ExcludeChatType = []string{"channel"}
			},
			ctx: baseContext(), want: true,
		},
		{
			name: "chat type include plus exclude drops excluded kind",
			set: func(o *filters.Options) {
				o.ChatType = []string{"private", "channel"}
				o.ExcludeChatType = []string{"channel"}
			},
			ctx: channelCtx, want: false,
		},
	})
}

func TestCompileExcludePureNegation(t *testing.T) {
	t.Parallel()

	videoCtx := baseContext()
	videoCtx.File = videoFile(2_000)

	photoCtx := baseContext()
	photoCtx.File = photoFile(3_000)

	zipCtx := baseContext()
	zipCtx.File = documentFile("arch.zip", ".zip", "application/zip", 1_000)

	channelCtx := baseContext()
	channelCtx.Chat = filters.Chat{ID: 40, Type: "channel", Title: "News Hub"}

	runPredicateCases(t, []predCase{
		{
			name: "exclude media alone passes fileless message",
			set:  func(o *filters.Options) { o.ExcludeMedia = []string{"video"} },
			ctx:  baseContext(), want: true,
		},
		{
			name: "exclude media alone drops excluded kind",
			set:  func(o *filters.Options) { o.ExcludeMedia = []string{"video"} },
			ctx:  videoCtx, want: false,
		},
		{
			name: "exclude media alone keeps other kind",
			set:  func(o *filters.Options) { o.ExcludeMedia = []string{"video"} },
			ctx:  photoCtx, want: true,
		},
		{
			name: "exclude mime glob alone drops matching mime",
			set:  func(o *filters.Options) { o.ExcludeMime = []string{"image/*"} },
			ctx:  photoCtx, want: false,
		},
		{
			name: "exclude mime glob alone keeps other mime",
			set:  func(o *filters.Options) { o.ExcludeMime = []string{"image/*"} },
			ctx:  videoCtx, want: true,
		},
		{
			name: "exclude ext alone drops listed extension",
			set:  func(o *filters.Options) { o.ExcludeExt = []string{".mp4"} },
			ctx:  videoCtx, want: false,
		},
		{
			name: "exclude ext alone keeps other extension",
			set:  func(o *filters.Options) { o.ExcludeExt = []string{".mp4"} },
			ctx:  zipCtx, want: true,
		},
		{
			name: "exclude chat type alone drops excluded kind",
			set:  func(o *filters.Options) { o.ExcludeChatType = []string{"channel"} },
			ctx:  channelCtx, want: false,
		},
		{
			name: "exclude chat type alone keeps other kind",
			set:  func(o *filters.Options) { o.ExcludeChatType = []string{"channel"} },
			ctx:  baseContext(), want: true,
		},
	})
}

func TestCompileChatDeletedTri(t *testing.T) {
	t.Parallel()

	deletedCtx := baseContext()
	deletedCtx.Chat.Deleted = true

	groupCtx := baseContext()
	groupCtx.Chat = filters.Chat{ID: 11, Type: "supergroup", Title: "Team 42"}

	runPredicateCases(t, []predCase{
		{
			name: "chat deleted true matches deleted private chat",
			set:  func(o *filters.Options) { o.ChatDeleted = filters.Tri(true) },
			ctx:  deletedCtx, want: true,
		},
		{
			name: "chat deleted true rejects live private chat",
			set:  func(o *filters.Options) { o.ChatDeleted = filters.Tri(true) },
			ctx:  baseContext(), want: false,
		},
		{
			name: "chat deleted false matches live chat",
			set:  func(o *filters.Options) { o.ChatDeleted = filters.Tri(false) },
			ctx:  baseContext(), want: true,
		},
		{
			name: "chat deleted false rejects deleted chat",
			set:  func(o *filters.Options) { o.ChatDeleted = filters.Tri(false) },
			ctx:  deletedCtx, want: false,
		},
		{
			name: "chat deleted true never matches group peer",
			set:  func(o *filters.Options) { o.ChatDeleted = filters.Tri(true) },
			ctx:  groupCtx, want: false,
		},
	})

	t.Run("unset adds no predicate", func(t *testing.T) {
		t.Parallel()

		plan, err := filters.Compile(&filters.Options{})
		require.NoError(t, err)
		assert.NotContains(t, predicateNames(plan), "chat_deleted")
	})
}

func TestCompileSenderNonSelection(t *testing.T) {
	t.Parallel()

	contactCtx := baseContext()
	contactCtx.Sender.IsContact = true
	contactCtx.Sender.IsMutual = true

	absentCtx := baseContext()
	absentCtx.Sender = filters.Sender{}

	runPredicateCases(t, []predCase{
		{
			name: "non contacts matches stranger sender",
			set:  func(o *filters.Options) { o.SenderNonContacts = true },
			ctx:  baseContext(), want: true,
		},
		{
			name: "non contacts rejects contact sender",
			set:  func(o *filters.Options) { o.SenderNonContacts = true },
			ctx:  contactCtx, want: false,
		},
		{
			name: "non contacts never matches absent sender",
			set:  func(o *filters.Options) { o.SenderNonContacts = true },
			ctx:  absentCtx, want: false,
		},
		{
			name: "non mutual matches one-way sender",
			set:  func(o *filters.Options) { o.SenderNonMutual = true },
			ctx:  baseContext(), want: true,
		},
		{
			name: "non mutual rejects mutual contact",
			set:  func(o *filters.Options) { o.SenderNonMutual = true },
			ctx:  contactCtx, want: false,
		},
		{
			name: "non mutual never matches absent sender",
			set:  func(o *filters.Options) { o.SenderNonMutual = true },
			ctx:  absentCtx, want: false,
		},
	})
}

func TestCompileNewOptionsExplainLines(t *testing.T) {
	t.Parallel()

	opts := &filters.Options{
		ChatDeleted:       filters.Tri(true),
		ExcludeChatType:   []string{"channel"},
		ExcludeMedia:      []string{"video"},
		ExcludeMime:       []string{"image/*"},
		ExcludeExt:        []string{".zip"},
		SenderNonContacts: true,
		SenderNonMutual:   true,
	}

	plan, err := filters.Compile(opts)
	require.NoError(t, err)

	joined := strings.Join(plan.ExplainLines, "\n")

	for _, want := range []string{
		"chat_deleted=true",
		"exclude_chat_type=channel",
		"exclude_media=video",
		"exclude_mime=image/*",
		"exclude_ext=.zip",
		"sender_non_contacts=true",
		"sender_non_mutual=true",
	} {
		assert.Contains(t, joined, want)
	}
}

func TestValidateExcludeVocab(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*filters.Options)
	}{
		{
			name:   "exclude media bad kind",
			mutate: func(o *filters.Options) { o.ExcludeMedia = []string{"hologram"} },
		},
		{
			name:   "exclude chat type bad kind",
			mutate: func(o *filters.Options) { o.ExcludeChatType = []string{"megacorp"} },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opts := &filters.Options{}
			tc.mutate(opts)

			err := opts.Validate()
			require.ErrorIs(t, err, filters.ErrBadVocab)
		})
	}
}

func TestCompileExcludeMimeGlobValidated(t *testing.T) {
	t.Parallel()

	opts := &filters.Options{ExcludeMime: []string{"image/[a-"}}

	plan, err := filters.Compile(opts)
	require.Error(t, err)
	assert.Nil(t, plan)
	require.ErrorIs(t, err, filters.ErrCompile)
}
