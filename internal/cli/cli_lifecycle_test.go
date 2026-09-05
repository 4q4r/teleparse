package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tempConfigPath returns a fresh, not-yet-existing config path plus its dir.
func tempConfigPath(t *testing.T) string {
	t.Helper()

	return filepath.Join(t.TempDir(), "config.toml")
}

func TestProfileRoundtrip(t *testing.T) {
	t.Parallel()

	cfg := tempConfigPath(t)

	out, err := execute(t, "--config", cfg, "profile", "save", "media-hounds")
	require.NoError(t, err)
	assert.Contains(t, out, `saved profile "media-hounds"`)

	// A fresh command tree must see the profile through disk alone.
	out, err = execute(t, "--config", cfg, "profile", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "media-hounds")

	out, err = execute(t, "--config", cfg, "profile", "show", "media-hounds")
	require.NoError(t, err)
	assert.Contains(t, out, "dedupe", "the saved profile round-trips its filters as TOML")

	out, err = execute(t, "--config", cfg, "profile", "rm", "media-hounds")
	require.NoError(t, err)
	assert.Contains(t, out, `removed profile "media-hounds"`)

	out, err = execute(t, "--config", cfg, "profile", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "no profiles")
}

func TestProfileRejectsInvalidAndAbsentNames(t *testing.T) {
	t.Parallel()

	cfg := tempConfigPath(t)

	_, err := execute(t, "--config", cfg, "profile", "save", "Not Valid!")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "profile name must match")

	_, err = execute(t, "--config", cfg, "profile", "show", "ghost")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "profile not found")

	_, err = execute(t, "--config", cfg, "profile", "rm", "ghost")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "profile not found")
}

func TestRootRejectsBadTOML(t *testing.T) {
	t.Parallel()

	cfg := tempConfigPath(t)
	require.NoError(t, os.WriteFile(cfg, []byte("[output\nbroken ="), 0o600))

	_, err := execute(t, "--config", cfg, "stats")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load config")
}

func TestRootVersionPrints(t *testing.T) {
	t.Parallel()

	out, err := execute(t, "--version")
	require.NoError(t, err)
	assert.Contains(t, out, "teleparse version")
}

func TestDlHelpDocumentsFilterSurface(t *testing.T) {
	t.Parallel()

	out, err := execute(t, "dl", "--help")
	require.NoError(t, err)
	assert.Contains(t, out, "chat-glob", "reflective filter flags appear in help")
	assert.Contains(t, out, "count-only")
	assert.Contains(t, out, "takeout")
}

func TestProxyShowDefaultsToDirect(t *testing.T) {
	// Serial: t.Setenv cannot run in parallel tests.

	clearProxyEnv(t)

	out, err := execute(t, "--config", tempConfigPath(t), "proxy", "show")
	require.NoError(t, err)
	assert.Contains(t, out, "proxy: (direct)")
}

// seedState opens the CLI's state database under a redirected
// XDG_DATA_HOME and fills it with a small finished-history scenario.
func seedState(t *testing.T) string {
	t.Helper()

	dataHome := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_DATA_HOME", dataHome)

	stateDir := filepath.Join(dataHome, "teleparse")
	require.NoError(t, os.MkdirAll(stateDir, 0o700))

	ctx := t.Context()

	st, err := store.Open(filepath.Join(stateDir, "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	require.NoError(t, st.UpsertChat(ctx, store.Chat{
		ChatID: 42, Type: "channel", Title: ptr("News Room"),
	}))

	for _, msg := range []int64{101, 102} {
		item := &store.MediaItem{
			ChatID: 42, MessageID: msg, MediaIndex: 0,
			MediaClass: "document", MediaID: msg + 1000,
			Size: ptr(int64(1536)), Filename: ptr("file.bin"),
			Status: store.StatusDone, Path: ptr("out/file.bin"),
		}
		require.NoError(t, st.UpsertMedia(ctx, item))
	}

	pending := &store.MediaItem{
		ChatID: 43, MessageID: 1, MediaIndex: 0,
		MediaClass: "photo", MediaID: 2001, Status: store.StatusQueued,
	}
	require.NoError(t, st.UpsertMedia(ctx, pending))

	require.NoError(t, st.CreateRun(ctx, &store.Run{
		RunID: "run-seed-done", Account: "main", FilterJSON: "media = [\"photo\"]",
	}))
	require.NoError(t, st.FinishRun(ctx, "run-seed-done", store.StatusDone, ""))
	require.NoError(t, st.CreateRun(ctx, &store.Run{
		RunID: "run-seed-parked", Account: "main", FilterJSON: "media = [\"document\"]",
	}))
	require.NoError(t, st.SetResumeAt(ctx, "run-seed-parked", time.Now().Add(time.Hour)))
	require.NoError(t, st.FinishRun(ctx, "run-seed-parked", store.StatusParked, ""))

	return dataHome
}

func ptr[T any](v T) *T { return &v }

func TestExportStatsRunsAgainstSeededState(t *testing.T) {
	// Serial: t.Setenv cannot run in parallel tests.

	cfg := tempConfigPath(t)
	seedState(t)

	out, err := execute(t, "--config", cfg, "stats")
	require.NoError(t, err)
	assert.Contains(t, out, "News Room")
	assert.Contains(t, out, "total: 2 file(s), 3.0KiB")

	out, err = execute(t, "--config", cfg, "stats", "--chat", "43")
	require.NoError(t, err)
	assert.NotContains(t, out, "News Room", "chat scoping hides other chats")

	out, err = execute(t, "--config", cfg, "runs", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "run-seed-parked")
	assert.Contains(t, out, "run-seed-done")

	out, err = execute(t, "--config", cfg, "runs", "show", "run-seed-parked")
	require.NoError(t, err)
	assert.Contains(t, out, "run id:    run-seed-parked")
	assert.Contains(t, out, "status:    parked")
	assert.Contains(t, out, "media = [\"document\"]")

	_, err = execute(t, "--config", cfg, "runs", "show", "run-missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "run not found")

	out, err = execute(t, "--config", cfg, "runs", "clean", "--older-than", "1ns")
	require.NoError(t, err)
	assert.Contains(t, out, "removed 1 finished run(s)", "the parked run survives cleanup")

	out, err = execute(t, "--config", cfg, "export", "jsonl")
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 3, "every seeded row exports")

	var first store.MediaRow

	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	assert.Equal(t, int64(42), first.ChatID)
	assert.Equal(t, int64(101), first.MessageID)
	assert.Equal(t, "done", first.Status)

	out, err = execute(t, "--config", cfg, "export", "csv")
	require.NoError(t, err)
	assert.Contains(t, out, "chat_id,message_id,media_index")
	assert.Contains(t, out, "43", "the queued photo row is present")

	target := filepath.Join(t.TempDir(), "export.jsonl")
	_, err = execute(t, "--config", cfg, "export", "jsonl", "--out", target)
	require.NoError(t, err)

	raw, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Len(t, strings.Split(strings.TrimSpace(string(raw)), "\n"), 3)
}

func TestDoctorFailsWithoutCreds(t *testing.T) {
	// Serial: t.Setenv cannot run in parallel tests.

	t.Setenv("TELEPARSE_API_ID", "")
	t.Setenv("TELEPARSE_API_HASH", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))

	out, err := execute(t, "--config", tempConfigPath(t), "doctor")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "doctor checks failed")
	assert.Contains(t, out, "FAIL api credentials")
}

func TestDoctorPassesWithCreds(t *testing.T) {
	// Serial: t.Setenv cannot run in parallel tests.

	clearProxyEnv(t)
	t.Setenv("TELEPARSE_API_ID", "123456")
	t.Setenv("TELEPARSE_API_HASH", "0123456789abcdef0123456789abcdef")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))

	out, err := execute(t, "--config", tempConfigPath(t), "doctor")
	require.NoError(t, err)
	assert.Contains(t, out, "PASS api credentials")
	assert.NotContains(t, out, "FAIL")
}

func TestProxyShowHonorsEnv(t *testing.T) {
	// Serial: t.Setenv cannot run in parallel tests.

	t.Setenv("TELEPARSE_PROXY", "socks5://127.0.0.1:1080")

	out, err := execute(t, "--config", tempConfigPath(t), "proxy", "show")
	require.NoError(t, err)
	assert.Contains(t, out, "proxy: socks5://127.0.0.1:1080")
}

func TestNetworkCommandsFailFastWithoutCreds(t *testing.T) {
	// Serial: t.Setenv cannot run in parallel tests.

	t.Setenv("TELEPARSE_API_ID", "")
	t.Setenv("TELEPARSE_API_HASH", "")

	for _, name := range []string{"dl", "scan", "sync"} {
		started := time.Now()

		_, err := execute(t, "--config", tempConfigPath(t), name, "all")

		elapsed := time.Since(started)
		require.Error(t, err, "%s without credentials must fail", name)
		assert.Contains(t, err.Error(), "TELEPARSE_API_ID",
			"%s must name the missing credential variables", name)
		assert.Less(t, elapsed, 2*time.Second, "%s must fail before any network dial", name)
	}
}

func TestAccountAllEnumeratesLocalSessions(t *testing.T) {
	// Serial: t.Setenv cannot run in parallel tests.

	t.Setenv("TELEPARSE_API_ID", "")
	t.Setenv("TELEPARSE_API_HASH", "")

	configHome := filepath.Join(t.TempDir(), "config")
	t.Setenv("XDG_CONFIG_HOME", configHome)

	accounts := filepath.Join(configHome, "teleparse", "accounts", "spare")
	require.NoError(t, os.MkdirAll(accounts, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(accounts, "session.json"), []byte("{}"), 0o600))

	// The spare session is enumerated locally; the run then stops at the
	// missing credentials exactly as the single-account path does.
	_, err := execute(t, "--config", tempConfigPath(t), "--account", "all", "dl", "all")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TELEPARSE_API_ID")
}

func TestAuthListAndExportAgainstLocalSessions(t *testing.T) {
	// Serial: t.Setenv cannot run in parallel tests.

	configHome := filepath.Join(t.TempDir(), "config")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	t.Setenv("TELEPARSE_ACCOUNT", "main")

	cfg := tempConfigPath(t)

	out, err := execute(t, "--config", cfg, "auth", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "no accounts")

	accounts := filepath.Join(configHome, "teleparse", "accounts")
	for _, name := range []string{"main", "spare"} {
		dir := filepath.Join(accounts, name)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "session.json"),
			[]byte(`{"version":2}`), 0o600))
	}

	out, err = execute(t, "--config", cfg, "auth", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "main (default)", "the config account is marked")
	assert.Contains(t, out, "spare")

	out, err = execute(t, "--config", cfg, "auth", "export")
	require.NoError(t, err)
	assert.Contains(t, out, `"version": 2`, "the raw session JSON is pretty-printed")

	_, err = execute(t, "--config", cfg, "--account", "ghost", "auth", "export")
	require.Error(t, err, "an account without a session file must fail loudly")
	assert.Contains(t, err.Error(), "read session")
}

func TestAuthCommandsFailFastWithoutCreds(t *testing.T) {
	// Serial: t.Setenv cannot run in parallel tests.

	t.Setenv("TELEPARSE_API_ID", "")
	t.Setenv("TELEPARSE_API_HASH", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))

	cfg := tempConfigPath(t)

	for _, args := range [][]string{
		{"auth", "login"},
		{"auth", "logout"},
		{"auth", "status"},
	} {
		_, err := execute(t, append([]string{"--config", cfg}, args...)...)
		require.Error(t, err, "%v without credentials must fail", args)
		assert.Contains(t, err.Error(), "TELEPARSE_API_ID")
	}
}

func TestResumeFlows(t *testing.T) {
	// Serial: t.Setenv cannot run in parallel tests.

	t.Setenv("TELEPARSE_API_ID", "")
	t.Setenv("TELEPARSE_API_HASH", "")

	cfg := tempConfigPath(t)
	dataHome := seedState(t)

	_, err := execute(t, "--config", cfg, "resume", "run-missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "run not found")

	// The seeded parked run still has a future flood deadline.
	out, err := execute(t, "--config", cfg, "resume", "run-seed-parked")
	require.NoError(t, err)
	assert.Contains(t, out, "wait until", "a future resume_at must block, not dial")

	// A parked run whose deadline passed re-enters the pipeline and stops
	// at the missing credentials, proving the stored payload decoded.
	ctx := t.Context()

	st, err := store.Open(filepath.Join(dataHome, "teleparse", "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	require.NoError(t, st.CreateRun(ctx, &store.Run{
		RunID: "run-seed-past", Account: "main", FilterJSON: `chats = ["all"]`,
	}))
	require.NoError(t, st.SetResumeAt(ctx, "run-seed-past", time.Now().Add(-time.Minute)))
	require.NoError(t, st.FinishRun(ctx, "run-seed-past", store.StatusParked, ""))

	out, err = execute(t, "--config", cfg, "resume", "run-seed-past")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TELEPARSE_API_ID")
	assert.Contains(t, out, "resuming run-seed-past (account main, 1 chat spec(s))")
}
