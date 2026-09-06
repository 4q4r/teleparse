package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/4q4r/teleparse/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAccountsDecode(t *testing.T) {
	t.Setenv("TELEPARSE_API_ID", "12345")
	t.Setenv("TELEPARSE_API_HASH", "0123456789abcdef0123456789abcdef")

	path := filepath.Join(t.TempDir(), "config.toml")

	text := `
[accounts]
routing = { "@chat" = "account2", "123456" = "spare" }
premium_preferred = true
`

	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))

	cfg, _, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, map[string]string{"@chat": "account2", "123456": "spare"}, cfg.Accounts.Routing)
	assert.True(t, cfg.Accounts.PremiumPreferred)
}

func TestAccountsDefaultOff(t *testing.T) {
	t.Parallel()

	cfg := config.Default()

	assert.Empty(t, cfg.Accounts.Routing, "routing must default to an empty table")
	assert.False(t, cfg.Accounts.PremiumPreferred, "premium_preferred must default to false")

	require.NoError(t, cfg.Validate())
}

func TestAccountsValidateRoutingTarget(t *testing.T) {
	t.Setenv("TELEPARSE_API_ID", "12345")
	t.Setenv("TELEPARSE_API_HASH", "0123456789abcdef0123456789abcdef")

	path := filepath.Join(t.TempDir(), "config.toml")

	text := `
[accounts]
routing = { "@chat" = "" }
`

	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))

	_, _, err := config.Load(path)
	require.Error(t, err)
	require.ErrorIs(t, err, config.ErrBadRoutingTarget)
	assert.Contains(t, err.Error(), `accounts.routing["@chat"]`)
}

func TestAccountsTemplateKeysPresent(t *testing.T) {
	t.Setenv("TELEPARSE_API_ID", "12345")
	t.Setenv("TELEPARSE_API_HASH", "0123456789abcdef0123456789abcdef")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Loading the default (missing) path writes the commented template;
	// every accounts knob must appear there so first-run users discover it.
	_, _, err := config.Load("")
	require.NoError(t, err)

	templatePath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "teleparse", "config.toml")

	raw, err := os.ReadFile(templatePath)
	require.NoError(t, err)

	template := string(raw)
	assert.Contains(t, template, "[accounts]")
	assert.Contains(t, template, "premium_preferred")
	assert.Contains(t, template, `routing = { "@chat" = "account2", "123456" = "spare" }`)
}
