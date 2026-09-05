package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/4q4r/teleparse/internal/cli"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatFlagRejectsUnknownValue(t *testing.T) {
	t.Parallel()

	_, err := execute(t, "--config", tempConfigPath(t), "--format", "xml", "proxy", "show")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "table")
	assert.Contains(t, err.Error(), "json")
	assert.Contains(t, err.Error(), "plain")
}

func TestFormatPlainOnProxyShow(t *testing.T) {
	// Serial: clearProxyEnv uses t.Setenv.

	clearProxyEnv(t)

	out, err := execute(t, "--config", tempConfigPath(t), "--format", "plain", "proxy", "show")
	require.NoError(t, err)
	assert.Equal(t, "proxy: (direct)\nsource: direct\n", out)
}

func TestFormatJSONOnProxyShow(t *testing.T) {
	// Serial: clearProxyEnv uses t.Setenv.

	clearProxyEnv(t)

	out, err := execute(t, "--config", tempConfigPath(t), "--format", "json", "proxy", "show")
	require.NoError(t, err)
	assert.JSONEq(t, `{"proxy": "", "source": "direct"}`, out)
}

func TestNoASCIIAndFormatFlagsRegistered(t *testing.T) {
	t.Parallel()

	root := cli.New()

	require.NotNil(t, root.PersistentFlags().Lookup("no-ascii"))
	require.NotNil(t, root.PersistentFlags().Lookup("format"))
}

func TestPingHelpRenders(t *testing.T) {
	t.Parallel()

	out, err := execute(t, "ping", "--help")
	require.NoError(t, err)
	assert.Contains(t, out, "--dc")
}

func TestPingRejectsUnknownDC(t *testing.T) {
	t.Parallel()

	_, err := execute(t, "--config", tempConfigPath(t), "ping", "--dc", "99")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dc")
}

// TestAuthListJSONFormatsAccounts pins --format json on auth list against a
// seeded accounts dir; serial because XDG_CONFIG_HOME is process-wide.
func TestAuthListJSONFormatsAccounts(t *testing.T) {
	configHome := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("XDG_CONFIG_HOME", configHome)

	accounts := filepath.Join(configHome, "teleparse", "accounts", "work")
	require.NoError(t, os.MkdirAll(accounts, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(accounts, "session.json"), []byte("{}"), 0o600))

	out, err := execute(t, "--config", tempConfigPath(t), "--format", "json", "auth", "list")
	require.NoError(t, err)
	assert.JSONEq(t, `["work"]`, out)
}

func TestAuthListPlainFormatsAccounts(t *testing.T) {
	configHome := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("XDG_CONFIG_HOME", configHome)

	accounts := filepath.Join(configHome, "teleparse", "accounts", "work")
	require.NoError(t, os.MkdirAll(accounts, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(accounts, "session.json"), []byte("{}"), 0o600))

	out, err := execute(t, "--config", tempConfigPath(t), "--format", "plain", "auth", "list")
	require.NoError(t, err)
	assert.Equal(t, "account: work\n", out)
}

// TestChatsListJSONFailsFastWithoutCreds ensures the machine-readable path
// errors on missing credentials instead of hanging on a dial.
func TestChatsListJSONFailsFastWithoutCreds(t *testing.T) {
	t.Setenv("TELEPARSE_API_ID", "")
	t.Setenv("TELEPARSE_API_HASH", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	_, err := execute(t, "--config", tempConfigPath(t), "--format", "json", "chats", "list")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TELEPARSE_API_ID")
}

// TestDoctorNoASCIIAndPremiumLine pins the doctor surface: the premium
// status line renders and --no-ascii keeps output ASCII.
func TestDoctorNoASCIIAndPremiumLine(t *testing.T) {
	configHome := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("XDG_CONFIG_HOME", configHome)

	accounts := filepath.Join(configHome, "teleparse", "accounts", "default")
	require.NoError(t, os.MkdirAll(accounts, 0o700))

	premium := `{"premium": true, "checked_at": "2026-09-04T00:00:00Z"}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(accounts, "premium.json"), []byte(premium), 0o600))

	out, err := execute(t, "--config", tempConfigPath(t), "--no-ascii", "doctor")
	require.Error(t, err, "creds are missing, doctor must fail")
	assert.Contains(t, out, "premium: yes (source: cache)")

	for idx, runeValue := range out {
		if runeValue >= 128 {
			t.Fatalf("non-ASCII rune %q at offset %d in doctor output", runeValue, idx)
		}
	}
}

// TestRunsListPlainFormatsEmpty pins plain on the empty-run surface.
func TestRunsListPlainFormatsEmpty(t *testing.T) {
	dataHome := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_DATA_HOME", dataHome)

	out, err := execute(t, "--config", tempConfigPath(t), "--format", "plain", "runs", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "no runs")
}

// TestStatsJSONEmptyState pins the json stats envelope against an empty db.
func TestStatsJSONEmptyState(t *testing.T) {
	dataHome := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_DATA_HOME", dataHome)

	out, err := execute(t, "--config", tempConfigPath(t), "--format", "json", "stats")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(strings.TrimSpace(out), "{"), "stats json must render an object, got %q", out)
	assert.Contains(t, out, `"statuses"`)
	assert.Contains(t, out, `"per_chat"`)
	assert.Contains(t, out, `"total"`)
}

var _ = cobra.Command{} // keep cobra imported for future flag assertions
