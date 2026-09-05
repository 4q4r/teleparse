package filters_test

import (
	"testing"

	"github.com/4q4r/teleparse/internal/filters"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOptionsValidateVocabularies(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		mutate  func(*filters.Options)
		wantErr error
	}{
		{
			name:    "bad media kind",
			mutate:  func(o *filters.Options) { o.Media = []string{"photo", "hologram"} },
			wantErr: filters.ErrBadVocab,
		},
		{
			name:    "bad chat type",
			mutate:  func(o *filters.Options) { o.ChatType = []string{"private", "matrix"} },
			wantErr: filters.ErrBadVocab,
		},
		{
			name:    "bad sticker kind",
			mutate:  func(o *filters.Options) { o.StickerKind = []string{"liquid"} },
			wantErr: filters.ErrBadVocab,
		},
		{
			name:    "bad album mode",
			mutate:  func(o *filters.Options) { o.Recursion.Albums = "explode" },
			wantErr: filters.ErrBadVocab,
		},
		{
			name:    "bad archived mode",
			mutate:  func(o *filters.Options) { o.Archived = "sometimes" },
			wantErr: filters.ErrBadVocab,
		},
		{
			name:    "bad has-text mode",
			mutate:  func(o *filters.Options) { o.HasText = "maybe" },
			wantErr: filters.ErrBadVocab,
		},
		{
			name:    "bad dedupe mode",
			mutate:  func(o *filters.Options) { o.Dedupe = "md5" },
			wantErr: filters.ErrBadVocab,
		},
		{
			name:    "bad size",
			mutate:  func(o *filters.Options) { o.MaxSize = "20KG" },
			wantErr: filters.ErrBadParse,
		},
		{
			name:    "bad duration",
			mutate:  func(o *filters.Options) { o.MinDuration = "fast" },
			wantErr: filters.ErrBadParse,
		},
		{
			name:    "bad date",
			mutate:  func(o *filters.Options) { o.After = "yesterday-ish" },
			wantErr: filters.ErrBadParse,
		},
		{
			name:    "bad regex",
			mutate:  func(o *filters.Options) { o.TextRegex = "(" },
			wantErr: filters.ErrBadRegex,
		},
		{
			name:    "inverted id range",
			mutate:  func(o *filters.Options) { o.MinID, o.MaxID = 200, 100 },
			wantErr: filters.ErrBadIDRange,
		},
		{
			name:   "all good",
			mutate: func(o *filters.Options) { o.Media = []string{"photo", "video-note", "gif"} },
		},
		{
			name:   "hardlink dedupe mode accepted",
			mutate: func(o *filters.Options) { o.Dedupe = "hardlink" },
		},
		{
			name:   "legacy dedupe modes accepted",
			mutate: func(o *filters.Options) { o.Dedupe = "unique-id" },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opts := &filters.Options{}
			tc.mutate(opts)
			err := opts.Validate()
			if tc.wantErr == nil {
				require.NoError(t, err)

				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestTriBoolRoundTrip(t *testing.T) {
	t.Parallel()

	var tb filters.TriBool
	assert.False(t, tb.IsSet())
	assert.Empty(t, tb.String())

	require.NoError(t, tb.UnmarshalText([]byte("true")))
	assert.True(t, tb.IsSet())
	assert.True(t, tb.Value())
	assert.Equal(t, "true", tb.String())

	require.NoError(t, tb.UnmarshalText([]byte("false")))
	assert.True(t, tb.IsSet())
	assert.False(t, tb.Value())

	require.NoError(t, tb.UnmarshalText(nil))
	assert.False(t, tb.IsSet())

	err := tb.UnmarshalText([]byte("maybe"))
	require.Error(t, err)
	require.ErrorIs(t, err, filters.ErrBadBool)

	set := filters.Tri(true)
	text, err := set.MarshalText()
	require.NoError(t, err)
	assert.Equal(t, "true", string(text))
}
