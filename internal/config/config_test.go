package config_test

import (
	"os"
	"path/filepath"
	"teleparse/internal/config"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadCreatesDefaultAndRoundTrips(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sub", "config.toml")
	cfg, paths, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "default", cfg.Auth.Account)
	assert.Equal(t, 3, cfg.Pacing.Concurrency)
	assert.Equal(t, "unique-id", cfg.Filters.Dedupe)
	assert.True(t, cfg.Filters.Recursion.Topics)
	assert.Contains(t, paths.StateDB, "state.db")
	assert.Contains(t, paths.AccountsDir, "accounts")
	assert.FileExists(t, path)

	cfg2, _, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, cfg.Auth, cfg2.Auth)
	assert.Equal(t, cfg.Net, cfg2.Net)
	assert.Equal(t, cfg.Pacing, cfg2.Pacing)
	assert.Equal(t, cfg.Output, cfg2.Output)
	assert.Equal(t, cfg.Filters.Dedupe, cfg2.Filters.Dedupe)
	assert.Equal(t, cfg.Filters.Recursion, cfg2.Filters.Recursion)
	assert.NotNil(t, cfg2.Profiles)
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("[net]\nproxee = \"socks5://1.2.3.4:1080\"\n"), 0o600))

	_, _, err := config.Load(path)
	require.Error(t, err)
	assert.ErrorIs(t, err, config.ErrDecodeTOML)
}

func TestLoadRejectsBadValues(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("[pacing]\nconcurrency = 0\n"), 0o600))

	_, _, err := config.Load(path)
	require.Error(t, err)
	assert.ErrorIs(t, err, config.ErrBadConcurrency)
}

func TestEnvOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("TELEPARSE_ACCOUNT", "spare")
	t.Setenv("TELEPARSE_PROXY", "socks5://127.0.0.1:1080")
	t.Setenv("TELEPARSE_CONCURRENCY", "7")
	t.Setenv("TELEPARSE_ROOT", "/tmp/teleparse-x")

	cfg, paths, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "spare", cfg.Auth.Account)
	assert.Equal(t, "socks5://127.0.0.1:1080", cfg.Net.Proxy)
	assert.Equal(t, 7, cfg.Pacing.Concurrency)
	assert.Equal(t, "/tmp/teleparse-x", paths.Downloads)
}

func TestProfileOverlay(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	tomlText := "[filters]\ndedupe = \"unique-id\"\nmedia = [\"photo\"]\n" +
		"[profiles.videos]\nmedia = [\"video\"]\ndedupe = \"hash\"\n"
	require.NoError(t, os.WriteFile(path, []byte(tomlText), 0o600))

	cfg, _, err := config.Load(path)
	require.NoError(t, err)

	prof, err := cfg.Profile("videos")
	require.NoError(t, err)
	assert.Equal(t, []string{"video"}, prof.Media)
	assert.Equal(t, "hash", prof.Dedupe)
	assert.Equal(t, cfg.Filters.Recursion, prof.Recursion, "unset fields inherit base")

	_, err = cfg.Profile("absent")
	require.Error(t, err)
	assert.ErrorIs(t, err, config.ErrProfileAbsent)
}
