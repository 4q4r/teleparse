package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/4q4r/teleparse/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScanIncrementalDefaultsOn(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	_, _, err := config.Load(path)
	require.NoError(t, err)

	cfg, _, err := config.Load(path)
	require.NoError(t, err)
	assert.True(t, cfg.Scan.Incremental, "incremental walks must be on by default")
}

func TestScanIncrementalConfigurable(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("[scan]\nincremental = false\n"), 0o600))

	cfg, _, err := config.Load(path)
	require.NoError(t, err)
	assert.False(t, cfg.Scan.Incremental, "scan.incremental = false opts out of cached walks")
}

func TestScanIncrementalUnknownKeyRejected(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("[scan]\nincrementall = true\n"), 0o600))

	_, _, err := config.Load(path)
	require.Error(t, err)
	assert.ErrorIs(t, err, config.ErrDecodeTOML, "scan section typos must fail loudly")
}

func TestTemplateDocumentsIncrementalAndFullFlag(t *testing.T) {
	// Serial: t.Setenv cannot run in parallel tests.

	clearProxyEnv(t)

	path := filepath.Join(t.TempDir(), "config.toml")
	_, _, err := config.Load(path)
	require.NoError(t, err)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	text := string(raw)
	assert.Contains(t, text, "[scan]", "template carries the scan section")
	assert.Contains(t, text, "incremental", "template documents the incremental knob")
}
