package cli_test

import (
	"testing"

	"github.com/4q4r/teleparse/internal/cli"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthLoginQRFlagRegistration(t *testing.T) {
	t.Parallel()

	root := cli.New()

	login := commandByName(t, root, "auth")
	login = commandByName(t, login, "login")
	require.NotNil(t, login)

	for _, name := range []string{"qr", "timeout", "import-tdesktop", "import-telethon", "phone"} {
		assert.NotNil(t, login.Flags().Lookup(name), "auth login must register --%s", name)
	}
}

func TestAuthLoginHelpMentionsQR(t *testing.T) {
	t.Parallel()

	out, err := execute(t, "auth", "login", "--help")
	require.NoError(t, err)
	assert.Contains(t, out, "--qr")
	assert.Contains(t, out, "--timeout")
	assert.Contains(t, out, "--import-tdesktop")
}

func TestAuthLoginQRExcludesOtherMethods(t *testing.T) {
	t.Parallel()

	cfg := tempConfigPath(t)

	for _, args := range [][]string{
		{"--config", cfg, "auth", "login", "--qr", "--phone", "+15551234567"},
		{"--config", cfg, "auth", "login", "--qr", "--import-telethon", "x.session"},
		{"--config", cfg, "auth", "login", "--qr", "--import-tdesktop", "tdata"},
	} {
		_, err := execute(t, args...)
		require.Error(t, err, "args %v", args)
		assert.Contains(t, err.Error(), "--qr cannot be combined", "args %v", args)
	}
}

func TestAuthLoginQRTimeoutValidation(t *testing.T) {
	t.Parallel()

	cfg := tempConfigPath(t)

	_, err := execute(t, "--config", cfg, "auth", "login", "--qr", "--timeout", "10ms")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least 30s")

	_, err = execute(t, "--config", cfg, "auth", "login", "--timeout", "5m")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--timeout requires --qr")
}
