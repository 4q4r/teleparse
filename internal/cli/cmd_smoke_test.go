package cli_test

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/4q4r/teleparse/internal/cli"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// execute runs the root command with args, returning stdout and the
// execution error.
func execute(t *testing.T, args ...string) (string, error) {
	t.Helper()

	root := cli.New()

	var out bytes.Buffer

	root.SetOut(&out)
	root.SetArgs(args)

	err := root.Execute()

	return out.String(), err
}

func TestRootHelpRenders(t *testing.T) {
	t.Parallel()

	out, err := execute(t, "--help")

	require.NoError(t, err)

	for _, name := range []string{"scan", "dl", "sync", "resume", "runs", "export", "stats", "profile"} {
		assert.Contains(t, out, name, "root help should list %s", name)
	}
}

func TestDlFlagRegistration(t *testing.T) {
	t.Parallel()

	root := cli.New()

	dl := commandByName(t, root, "dl")
	require.NotNil(t, dl.Flags().Lookup("dry-run"))
	require.NotNil(t, dl.Flags().Lookup("explain"))
	require.NotNil(t, dl.Flags().Lookup("count-only"))
	require.NotNil(t, dl.Flags().Lookup("takeout"))
	require.NotNil(t, dl.Flags().Lookup("profile"))

	// The reflective filter surface registers ~100 flags.
	count := 0

	dl.Flags().VisitAll(func(*pflag.Flag) { count++ })

	assert.Greater(t, count, 80, "expected the full filter flag surface, got %d", count)
}

func TestScanIsDryRunAlias(t *testing.T) {
	t.Parallel()

	root := cli.New()

	scan := commandByName(t, root, "scan")
	require.NotNil(t, scan)

	// scan must not offer a takeout flag: it never downloads.
	assert.Nil(t, scan.Flags().Lookup("takeout"))
}

func TestRunsExportStatsHelp(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"runs", "--help"},
		{"runs", "list", "--help"},
		{"runs", "show", "--help"},
		{"runs", "clean", "--help"},
		{"resume", "--help"},
		{"export", "jsonl", "--help"},
		{"export", "csv", "--help"},
		{"stats", "--help"},
		{"sync", "--help"},
	} {
		_, err := execute(t, args...)
		require.NoError(t, err, "command %v", args)
	}
}

func TestStatsRunsExportAgainstEmptyState(t *testing.T) {
	// Serial: t.Setenv cannot run in parallel tests.

	dataHome := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_DATA_HOME", dataHome)

	configPath := filepath.Join(t.TempDir(), "config.toml")

	out, err := execute(t, "--config", configPath, "stats")
	require.NoError(t, err)
	assert.Contains(t, out, "total: 0 file(s)")

	out, err = execute(t, "--config", configPath, "runs", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "no runs")

	out, err = execute(t, "--config", configPath, "export", "jsonl")
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(out), "empty manifest exports nothing")

	_, err = execute(t, "--config", configPath, "export", "jsonl", "--run", "run-x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "run linkage")
}

func TestAuthLoginNonTTYWithoutCredsPrintsActionableError(t *testing.T) {
	// Serial: t.Setenv cannot run in parallel tests.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg"))
	t.Setenv("TELEPARSE_API_ID", "")
	t.Setenv("TELEPARSE_API_HASH", "")

	configPath := filepath.Join(t.TempDir(), "config.toml")

	// go test pipes stdin, so auth login takes the non-interactive branch.
	_, err := execute(t, "--config", configPath, "auth", "login")
	require.Error(t, err)

	message := err.Error()
	assert.Contains(t, message, "TELEPARSE_API_ID=")
	assert.Contains(t, message, "TELEPARSE_API_HASH=")
	assert.Contains(t, message, "my.telegram.org")
	assert.Contains(t, message, "auth login")

	// The dl-family surfaces the same single error.
	_, err = execute(t, "--config", configPath, "chats", "list")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TELEPARSE_API_ID=")
	assert.Contains(t, err.Error(), "my.telegram.org")
}

func TestRootRegistersColorControlFlags(t *testing.T) {
	t.Parallel()

	root := cli.New()

	require.NotNil(t, root.PersistentFlags().Lookup("no-color"))
	require.NotNil(t, root.PersistentFlags().Lookup("no-ascii"))

	out, err := execute(t, "auth", "login", "--help")
	require.NoError(t, err)
	assert.Contains(t, out, "no-color")
}

func commandByName(t *testing.T, root *cobra.Command, name string) *cobra.Command {
	t.Helper()

	for _, cmd := range root.Commands() {
		if cmd.Name() == name {
			return cmd
		}
	}

	return nil
}
