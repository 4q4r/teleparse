package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/4q4r/teleparse/internal/download"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDedupeStatsAndGcCommands exercises the blob-store maintenance surface
// end to end against a temp downloads root.
func TestDedupeStatsAndGcCommands(t *testing.T) {
	// Serial: t.Setenv cannot run in parallel tests.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	root := t.TempDir()

	orphan := download.BlobPath(root, "photo", 800, ".jpg")
	require.NoError(t, os.MkdirAll(filepath.Dir(orphan), 0o700))
	require.NoError(t, os.WriteFile(orphan, []byte("gone!"), 0o600))

	statsOut, err := execute(t, "--root", root, "dedupe", "stats")
	require.NoError(t, err)
	assert.Contains(t, statsOut, "blobs: 1")
	assert.Contains(t, statsOut, "orphaned: 1")

	gcOut, err := execute(t, "--root", root, "dedupe", "gc")
	require.NoError(t, err)
	assert.True(t, strings.Contains(gcOut, "removed: 1") || strings.Contains(gcOut, "removed 1"),
		"gc output should report one removal, got %q", gcOut)
	assert.NoFileExists(t, orphan)

	finalOut, err := execute(t, "--root", root, "dedupe", "stats")
	require.NoError(t, err)
	assert.Contains(t, finalOut, "blobs: 0")
}

func TestDedupeCommandRegistered(t *testing.T) {
	t.Parallel()

	out, err := execute(t, "--help")
	require.NoError(t, err)
	assert.Contains(t, out, "dedupe", "root help should list the dedupe command")
}
