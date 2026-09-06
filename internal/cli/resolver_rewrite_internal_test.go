package cli

import (
	"path/filepath"
	"testing"

	"github.com/4q4r/teleparse/internal/filters"
	"github.com/4q4r/teleparse/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rewriteResolver builds a store-only resolver with the rewrite enabled.
func rewriteResolver() *runResolver {
	resolver := manifestResolver(fakeTitles{titles: map[int64]string{7: "News"}})
	resolver.rewriteExt = true

	return resolver
}

// TestResolveRewritesMismatchedExtension pins --rewrite-ext end to end on
// the manifest path: the on-disk name takes the canonical extension for the
// recorded mime type while the manifest's own filename stays untouched.
func TestResolveRewritesMismatchedExtension(t *testing.T) {
	t.Parallel()

	item := store.MediaItem{
		ChatID: 7, MessageID: 10, MediaIndex: 0,
		Filename: ptr("backup.rar"),
		Mime:     ptr("application/zip"),
		Date:     ptr("2024-03-05T10:00:00Z"),
	}

	resolved, err := rewriteResolver().Resolve(item)
	require.NoError(t, err)

	assert.Equal(t, filepath.Join("root", "News", "2024-03", "backup.zip"), resolved.Path)
	assert.Equal(t, "backup.rar", resolved.Meta.Filename, "the manifest filename field never changes")
}

// TestResolveRewriteDisabledByDefault pins the off default.
func TestResolveRewriteDisabledByDefault(t *testing.T) {
	t.Parallel()

	item := store.MediaItem{
		ChatID: 7, MessageID: 10,
		Filename: ptr("backup.rar"),
		Mime:     ptr("application/zip"),
		Date:     ptr("2024-03-05T10:00:00Z"),
	}

	resolved, err := manifestResolver(fakeTitles{titles: map[int64]string{7: "News"}}).Resolve(item)
	require.NoError(t, err)

	assert.Equal(t, filepath.Join("root", "News", "2024-03", "backup.rar"), resolved.Path)
}

// TestResolveRewriteFreshWalkPath pins the rewrite on the walked-message
// path too: the final path renames while sidecar metadata follows the final
// path and keeps Telegram's original name.
func TestResolveRewriteFreshWalkPath(t *testing.T) {
	t.Parallel()

	resolver := rewriteResolver()

	fctx := filters.Context{
		Chat:    filters.Chat{ID: 7, Title: "News"},
		Message: filters.Message{ID: 101, Date: 1709632800},
		File:    &filters.FileInfo{Present: true, Kind: "document", Name: "clip.avi", Ext: ".avi"},
	}

	resolver.cache.put(msgCacheKey{chatID: 7, msgID: 101},
		walkedMessage{fctx: &fctx, msg: docMessageWith(101, 778, "ref")})

	item := store.MediaItem{ChatID: 7, MessageID: 101, MediaIndex: 0, Mime: ptr("application/zip")}

	resolved, err := resolver.Resolve(item)
	require.NoError(t, err)

	assert.Equal(t, filepath.Join("root", "News", "2024-03", "clip.zip"), resolved.Path)
	assert.Equal(t, filepath.Join("root", "News", "2024-03", "clip.zip"), resolved.Meta.Path,
		"sidecar paths follow the rewritten final path")
	assert.Equal(t, "clip.avi", resolved.Meta.Filename)
}

// TestResolveRewriteNeverBreaksCompound pins that compound archive names
// survive the rewrite even when the mime type disagrees.
func TestResolveRewriteNeverBreaksCompound(t *testing.T) {
	t.Parallel()

	item := store.MediaItem{
		ChatID: 7, MessageID: 11,
		Filename: ptr("bundle.tar.gz"),
		Mime:     ptr("application/x-tar"),
		Date:     ptr("2024-03-05T10:00:00Z"),
	}

	resolved, err := rewriteResolver().Resolve(item)
	require.NoError(t, err)

	assert.Equal(t, filepath.Join("root", "News", "2024-03", "bundle.tar.gz"), resolved.Path)
}

// TestResolveRewriteUnknownMimeNoop pins the no-op for rows without a
// recorded mime type.
func TestResolveRewriteUnknownMimeNoop(t *testing.T) {
	t.Parallel()

	item := store.MediaItem{
		ChatID: 7, MessageID: 12,
		Filename: ptr("blob.bin"),
		Date:     ptr("2024-03-05T10:00:00Z"),
	}

	resolved, err := rewriteResolver().Resolve(item)
	require.NoError(t, err)

	assert.Equal(t, filepath.Join("root", "News", "2024-03", "blob.bin"), resolved.Path)
}
