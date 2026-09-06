package download_test

import (
	"mime"
	"testing"

	"github.com/4q4r/teleparse/internal/download"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRewriteExtPath pins the canonical-extension rewrite: a mismatched
// extension rewrites, a matching (or equivalent) one never does, compound
// archive suffixes stay and unknown mime types are a no-op.
func TestRewriteExtPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		path string
		mime string
		want string
	}{
		{"mismatched archive rewrites", "backup.rar", "application/zip", "backup.zip"},
		{"vnd.rar rewrites", "backup.zip", "application/vnd.rar", "backup.rar"},
		{"x-rar-compressed rewrites", "backup.zip", "application/x-rar-compressed", "backup.rar"},
		{"matching extension untouched", "archive.zip", "application/zip", "archive.zip"},
		{"tar matching untouched", "data.tar", "application/x-tar", "data.tar"},
		{"empty extension appends canonical", "file_123", "application/zip", "file_123.zip"},
		{"compound tar.gz under gzip untouched", "bundle.tar.gz", "application/gzip", "bundle.tar.gz"},
		{"compound tar.gz under tar untouched", "bundle.tar.gz", "application/x-tar", "bundle.tar.gz"},
		{"compound tgz untouched", "bundle.tgz", "application/gzip", "bundle.tgz"},
		{"case-insensitive match untouched", "PHOTO.JPG", "image/jpeg", "PHOTO.JPG"},
		{"jpeg equivalent untouched", "photo.jpeg", "image/jpeg", "photo.jpeg"},
		{"unknown mime untouched", "blob.bin", "application/x-unknown-thing", "blob.bin"},
		{"no mime untouched", "blob.bin", "", "blob.bin"},
		{"stem keeps case on rewrite", "Backup.RAR", "application/zip", "Backup.zip"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, testCase.want, download.RewriteExtPath(testCase.path, testCase.mime))
		})
	}
}

// TestRewriteExtPathStdlibFallback pins the mime.ExtensionsByType fallback
// through a type seeded at runtime: system mime tables differ per machine,
// so the fallback path gets its own deterministic type.
func TestRewriteExtPathStdlibFallback(t *testing.T) {
	t.Parallel()

	const (
		seededMime = "application/x-teleparse-seeded"
		seededExt  = ".tpseed"
	)

	require.NoError(t, mime.AddExtensionType(seededExt, seededMime))

	t.Run("mismatch rewrites to canonical", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, "data.tpseed", download.RewriteExtPath("data", seededMime),
			"the empty extension appends the seeded canonical")
		assert.Equal(t, "data.tpseed", download.RewriteExtPath("data.old", seededMime),
			"a mismatched extension takes the seeded canonical")
	})

	t.Run("match untouched", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, "data.TPSEED", download.RewriteExtPath("data.TPSEED", seededMime))
	})
}

// TestCanonicalExt pins the pinned archive table and the empty answer for
// unknown types.
func TestCanonicalExt(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"application/zip":              ".zip",
		"application/x-rar-compressed": ".rar",
		"application/vnd.rar":          ".rar",
		"application/x-tar":            ".tar",
		"application/gzip":             ".gz",
		"application/x-unknown":        "",
	}

	for mimeType, want := range cases {
		assert.Equal(t, want, download.CanonicalExt(mimeType), "canonical ext of %s", mimeType)
	}
}
