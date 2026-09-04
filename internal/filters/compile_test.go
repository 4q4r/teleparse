package filters_test

import (
	"teleparse/internal/filters"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type predCase struct {
	name string
	set  func(*filters.Options)
	ctx  *filters.Context
	want bool
}

func runPredicateCases(t *testing.T, cases []predCase) {
	t.Helper()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opts := &filters.Options{}
			tc.set(opts)

			plan, err := filters.Compile(opts)
			require.NoError(t, err)
			assert.Equal(t, tc.want, planMatches(plan, tc.ctx))
		})
	}
}

func planMatches(plan *filters.Plan, ctx *filters.Context) bool {
	for _, pred := range plan.Predicates {
		if !pred.Fn(ctx) {
			return false
		}
	}

	return true
}

func predicateNames(plan *filters.Plan) []string {
	names := make([]string, 0, len(plan.Predicates))
	for _, pred := range plan.Predicates {
		names = append(names, pred.Name)
	}

	return names
}

func baseContext() *filters.Context {
	return &filters.Context{
		Chat: filters.Chat{
			ID: 10, Type: "private", Title: "Alice", Username: "alice",
		},
		Sender: filters.Sender{
			Present: true, ID: 20, Name: "Alice", Username: "alice",
		},
		Message: filters.Message{
			ID: 100, Date: 1_750_000_000, Text: "hello",
		},
	}
}

func withFile(ctx *filters.Context, file *filters.FileInfo) *filters.Context {
	ctx.File = file

	return ctx
}

func photoFile(size int64) *filters.FileInfo {
	return &filters.FileInfo{
		Present: true, Kind: "photo", Mime: "image/jpeg", Size: size, Width: 1280, Height: 960,
	}
}

func videoFile(size int64) *filters.FileInfo {
	return &filters.FileInfo{
		Present: true, Kind: "video", Mime: "video/mp4", Name: "clip.mp4", Ext: ".mp4",
		Size: size, Duration: 120, Width: 1920, Height: 1080, Streamable: true,
	}
}

func documentFile(name, ext, mime string, size int64) *filters.FileInfo {
	return &filters.FileInfo{
		Present: true, Kind: "document", Mime: mime, Name: name, Ext: ext, Size: size,
	}
}

func stickerFile(kind string) *filters.FileInfo {
	return &filters.FileInfo{
		Present: true, Kind: "sticker", Mime: "image/webp", Name: "cat.webp", Ext: ".webp",
		Size: 4096, Width: 512, Height: 512, StickerKind: kind,
	}
}

func TestCompileEmptyOptionsYieldsNoConstraints(t *testing.T) {
	t.Parallel()

	plan, err := filters.Compile(&filters.Options{})
	require.NoError(t, err)
	assert.Empty(t, plan.Predicates)
	assert.Empty(t, plan.ExplainLines)
	assert.Empty(t, plan.Pushdown.MessagesFilter)
	assert.Zero(t, plan.Pushdown.MinDate)
	assert.Zero(t, plan.Pushdown.MaxDate)
}

func TestCompileRejectsInvalidOptions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		mutate  func(*filters.Options)
		wantErr error
	}{
		{
			name:    "validate vocab failure wraps ErrCompile",
			mutate:  func(o *filters.Options) { o.Media = []string{"hologram"} },
			wantErr: filters.ErrBadVocab,
		},
		{
			name:    "bad chat glob pattern",
			mutate:  func(o *filters.Options) { o.ChatGlob = "[" },
			wantErr: filters.ErrCompile,
		},
		{
			name:    "bad mime glob pattern",
			mutate:  func(o *filters.Options) { o.Mime = []string{"video/[a-"} },
			wantErr: filters.ErrCompile,
		},
		{
			name:    "bad in-album mode",
			mutate:  func(o *filters.Options) { o.InAlbum = "sometimes" },
			wantErr: filters.ErrBadVocab,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opts := &filters.Options{}
			tc.mutate(opts)

			plan, err := filters.Compile(opts)
			require.Error(t, err)
			assert.Nil(t, plan)
			require.ErrorIs(t, err, filters.ErrCompile)
			assert.ErrorIs(t, err, tc.wantErr)
		})
	}
}
