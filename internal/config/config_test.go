package config_test

import (
	"os"
	"path/filepath"
	"strings"
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

// clearProxyEnv neutralizes every proxy environment variable so tests are
// hermetic against a developer shell that exports a system-wide proxy.
func clearProxyEnv(t *testing.T) {
	t.Helper()

	for _, name := range []string{"TELEPARSE_PROXY", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(name, "")
	}
}

func TestStandardEnvProxyPickup(t *testing.T) {
	// Serial: t.Setenv cannot run in parallel tests.

	tests := []struct {
		name       string
		envName    string
		envValue   string
		wantProxy  string
		wantSource string
	}{
		{
			name: "HTTPS_PROXY http", envName: "HTTPS_PROXY", envValue: "http://127.0.0.1:8080",
			wantProxy: "http://127.0.0.1:8080", wantSource: "env:HTTPS_PROXY",
		},
		{
			name: "lowercase https_proxy fallback", envName: "https_proxy", envValue: "socks5://127.0.0.2:1080",
			wantProxy: "socks5://127.0.0.2:1080", wantSource: "env:https_proxy",
		},
		{
			name: "ALL_PROXY socks5", envName: "ALL_PROXY", envValue: "socks5://127.0.0.3:1080",
			wantProxy: "socks5://127.0.0.3:1080", wantSource: "env:ALL_PROXY",
		},
		{
			name: "scheme-less value normalized to http", envName: "HTTPS_PROXY", envValue: "127.0.0.4:8080",
			wantProxy: "http://127.0.0.4:8080", wantSource: "env:HTTPS_PROXY",
		},
		{
			name: "HTTPS_PROXY beats ALL_PROXY", envName: "HTTPS_PROXY", envValue: "http://127.0.0.1:8080",
			wantProxy: "http://127.0.0.1:8080", wantSource: "env:HTTPS_PROXY",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearProxyEnv(t)

			if tt.name == "HTTPS_PROXY beats ALL_PROXY" {
				t.Setenv("ALL_PROXY", "socks5://127.0.0.5:1080")
			}

			t.Setenv(tt.envName, tt.envValue)

			cfg, _, err := config.Load(filepath.Join(t.TempDir(), "config.toml"))
			require.NoError(t, err)
			assert.Equal(t, tt.wantProxy, cfg.Net.Proxy)
			assert.Equal(t, tt.wantSource, cfg.Net.ProxySource)
		})
	}
}

func TestStandardEnvProxyOverridesFileProxy(t *testing.T) {
	// Serial: t.Setenv cannot run in parallel tests.

	clearProxyEnv(t)
	t.Setenv("HTTPS_PROXY", "http://127.0.0.9:8080")

	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("[net]\nproxy = \"socks5://1.2.3.4:1080\"\n"), 0o600))

	cfg, _, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.9:8080", cfg.Net.Proxy)
	assert.Equal(t, "env:HTTPS_PROXY", cfg.Net.ProxySource, "standard env outranks the config file, like curl")
}

func TestFileProxySourceWithoutEnv(t *testing.T) {
	// Serial: t.Setenv cannot run in parallel tests.

	clearProxyEnv(t)

	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("[net]\nproxy = \"socks5://1.2.3.4:1080\"\n"), 0o600))

	cfg, _, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "socks5://1.2.3.4:1080", cfg.Net.Proxy)
	assert.Equal(t, "config", cfg.Net.ProxySource)
}

func TestTeleparseProxyBeatsStandardEnv(t *testing.T) {
	// Serial: t.Setenv cannot run in parallel tests.

	clearProxyEnv(t)
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:8080")
	t.Setenv("TELEPARSE_PROXY", "socks5://127.0.0.6:1080")

	cfg, _, err := config.Load(filepath.Join(t.TempDir(), "config.toml"))
	require.NoError(t, err)
	assert.Equal(t, "socks5://127.0.0.6:1080", cfg.Net.Proxy)
	assert.Equal(t, "env:TELEPARSE_PROXY", cfg.Net.ProxySource)
}

func TestIgnoreEnvSkipsStandardEnvButNotTeleparse(t *testing.T) {
	// Serial: t.Setenv cannot run in parallel tests.

	clearProxyEnv(t)
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:8080")

	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("[net]\nignore_env = true\n"), 0o600))

	cfg, _, err := config.Load(path)
	require.NoError(t, err)
	assert.Empty(t, cfg.Net.Proxy)
	assert.Empty(t, cfg.Net.ProxySource)

	t.Setenv("TELEPARSE_PROXY", "socks5://127.0.0.7:1080")

	cfg, _, err = config.Load(filepath.Join(t.TempDir(), "config.toml"))
	require.NoError(t, err)
	assert.Equal(t, "socks5://127.0.0.7:1080", cfg.Net.Proxy)
	assert.Equal(t, "env:TELEPARSE_PROXY", cfg.Net.ProxySource, "explicit app override survives ignore_env")
}

func TestEnvProxyUnknownSchemeFails(t *testing.T) {
	// Serial: t.Setenv cannot run in parallel tests.

	clearProxyEnv(t)
	t.Setenv("HTTPS_PROXY", "bad-scheme://x")

	_, _, err := config.Load(filepath.Join(t.TempDir(), "config.toml"))
	require.Error(t, err)
	require.ErrorIs(t, err, config.ErrBadEnvProxyScheme)
	assert.Contains(t, err.Error(), "HTTPS_PROXY", "error must name the offending env var")
	assert.Contains(t, err.Error(), "socks5://", "error must list allowed schemes")
	assert.Contains(t, err.Error(), "ignore_env", "error must point at the escape hatch")
}

func TestLoadWritesCommentedTemplate(t *testing.T) {
	// Serial: t.Setenv cannot run in parallel tests.

	clearProxyEnv(t)

	path := filepath.Join(t.TempDir(), "config.toml")
	_, _, err := config.Load(path)
	require.NoError(t, err)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(raw)

	assert.Contains(t, text, "# net.ignore_env", "template documents the env escape hatch")
	assert.NotContains(t, text, "[filters]", "per-run filters do not belong in the template")
	assert.NotContains(t, text, "[profiles]", "profiles do not belong in the template")

	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)

		if trimmed == "" || strings.HasPrefix(trimmed, "#") ||
			(strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]")) {
			continue
		}

		t.Errorf("active (uncommented) key in generated template: %q", line)
	}

	cfg, _, err := config.Load(path)
	require.NoError(t, err)

	want := config.Default()
	assert.Equal(t, want.Auth, cfg.Auth)
	assert.Equal(t, want.Net, cfg.Net)
	assert.Equal(t, want.Pacing, cfg.Pacing)
	assert.Equal(t, want.Output, cfg.Output)
}

func TestSaveMarshalsLiveValuesUnlikeTemplate(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, config.Default().Save(path))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	assert.Contains(t, string(raw), "concurrency = 3", "Save writes active values, not the commented template")
}
