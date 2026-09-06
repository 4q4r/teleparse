package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/4q4r/teleparse/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunNotifyWebhookAndRewriteExtConfig pins the file-config surface of
// both new knobs: [run] notify_webhook and [output] rewrite_ext.
func TestRunNotifyWebhookAndRewriteExtConfig(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	toml := "[run]\nnotify_webhook = \"https://cfg.example/hook\"\n\n[output]\nrewrite_ext = true\n"
	require.NoError(t, os.WriteFile(path, []byte(toml), 0o600))

	cfg, _, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, "https://cfg.example/hook", cfg.Run.NotifyWebhook)
	assert.True(t, cfg.Output.RewriteExt)
}

// TestRunNotifyWebhookDefaultsOff pins the off-by-default contract.
func TestRunNotifyWebhookDefaultsOff(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")

	cfg, _, err := config.Load(path)
	require.NoError(t, err)

	assert.Empty(t, cfg.Run.NotifyWebhook)
	assert.False(t, cfg.Output.RewriteExt)
}

// TestWebhookEnvOverridesConfig pins TELEPARSE_WEBHOOK outranking the config
// file value. Serial: t.Setenv cannot run in parallel tests.
func TestWebhookEnvOverridesConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	toml := "[run]\nnotify_webhook = \"https://cfg.example/hook\"\n"
	require.NoError(t, os.WriteFile(path, []byte(toml), 0o600))

	t.Setenv("TELEPARSE_WEBHOOK", "https://env.example/hook")

	cfg, _, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, "https://env.example/hook", cfg.Run.NotifyWebhook)
}
