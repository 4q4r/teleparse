package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clearProxyEnv neutralizes every proxy environment variable so tests are
// hermetic against a developer shell that exports a system-wide proxy.
func clearProxyEnv(t *testing.T) {
	t.Helper()

	for _, name := range []string{"TELEPARSE_PROXY", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(name, "")
	}
}

func TestProxyShowSourceDirect(t *testing.T) {
	// Serial: t.Setenv cannot run in parallel tests.

	clearProxyEnv(t)

	out, err := execute(t, "--config", tempConfigPath(t), "proxy", "show")
	require.NoError(t, err)
	assert.Contains(t, out, "proxy: (direct)")
	assert.Contains(t, out, "source: direct")
}

func TestProxyShowSourceFlag(t *testing.T) {
	// Serial: t.Setenv cannot run in parallel tests.

	clearProxyEnv(t)

	out, err := execute(t, "--config", tempConfigPath(t), "--proxy", "socks5://127.0.0.1:1080", "proxy", "show")
	require.NoError(t, err)
	assert.Contains(t, out, "proxy: socks5://127.0.0.1:1080")
	assert.Contains(t, out, "source: flag")
}

func TestProxyShowSourceStandardEnv(t *testing.T) {
	// Serial: t.Setenv cannot run in parallel tests.

	clearProxyEnv(t)
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:8080")

	out, err := execute(t, "--config", tempConfigPath(t), "proxy", "show")
	require.NoError(t, err)
	assert.Contains(t, out, "proxy: http://127.0.0.1:8080")
	assert.Contains(t, out, "source: env:HTTPS_PROXY")
}

func TestProxyShowSourceConfig(t *testing.T) {
	// Serial: t.Setenv cannot run in parallel tests.

	clearProxyEnv(t)

	cfg := tempConfigPath(t)
	require.NoError(t, os.WriteFile(cfg, []byte("[net]\nproxy = \"socks5://1.2.3.4:1080\"\n"), 0o600))

	out, err := execute(t, "--config", cfg, "proxy", "show")
	require.NoError(t, err)
	assert.Contains(t, out, "proxy: socks5://1.2.3.4:1080")
	assert.Contains(t, out, "source: config")
}

func TestProxyShowBadEnvSchemeFails(t *testing.T) {
	// Serial: t.Setenv cannot run in parallel tests.

	clearProxyEnv(t)
	t.Setenv("HTTPS_PROXY", "bad-scheme://x")

	_, err := execute(t, "--config", tempConfigPath(t), "proxy", "show")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTPS_PROXY")
}

func TestDoctorProxySourceAndIgnoreEnvNote(t *testing.T) {
	// Serial: t.Setenv cannot run in parallel tests.

	clearProxyEnv(t)
	t.Setenv("TELEPARSE_API_ID", "123456")
	t.Setenv("TELEPARSE_API_HASH", "0123456789abcdef0123456789abcdef")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))

	cfg := tempConfigPath(t)
	require.NoError(t, os.WriteFile(cfg, []byte("[net]\nignore_env = true\n"), 0o600))

	out, err := execute(t, "--config", cfg, "doctor")
	require.NoError(t, err)
	assert.Contains(t, out, "proxy (none configured) [direct]")
	assert.NotContains(t, out, "FAIL", "no proxy is dialed and all other checks pass")

	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:8080")

	out, err = execute(t, "--config", cfg, "doctor")
	require.NoError(t, err)
	assert.Contains(t, out, "env proxy ignored via net.ignore_env",
		"doctor must explain why a set env proxy is not in effect")
}
