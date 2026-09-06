package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/config"
	"github.com/4q4r/teleparse/internal/download"
	"github.com/4q4r/teleparse/internal/notify"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// webhookCmdForTest builds a bare command with both new flags registered,
// mirroring the dl/get registration.
func webhookCmdForTest() *cobra.Command {
	cmd := &cobra.Command{Use: "test"}
	addNotifyWebhookFlag(cmd)
	addRewriteExtFlag(cmd)

	return cmd
}

func TestNotifyWebhookURLPrecedence(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.Run.NotifyWebhook = "https://config.example/hook"

	t.Run("flag wins", func(t *testing.T) {
		t.Parallel()

		cmd := webhookCmdForTest()
		require.NoError(t, cmd.Flags().Set("notify-webhook", "https://flag.example/hook"))

		assert.Equal(t, "https://flag.example/hook", notifyWebhookURL(cfg, cmd))
	})

	t.Run("explicit empty flag disables", func(t *testing.T) {
		t.Parallel()

		cmd := webhookCmdForTest()
		require.NoError(t, cmd.Flags().Set("notify-webhook", ""))

		assert.Empty(t, notifyWebhookURL(cfg, cmd))
	})

	t.Run("config serves when flag untouched", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, "https://config.example/hook", notifyWebhookURL(cfg, webhookCmdForTest()))
	})

	t.Run("no flag, no config disables", func(t *testing.T) {
		t.Parallel()

		assert.Empty(t, notifyWebhookURL(config.Default(), &cobra.Command{Use: "bare"}))
	})
}

func TestRewriteExtModePrecedence(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.Output.RewriteExt = true

	t.Run("flag on wins", func(t *testing.T) {
		t.Parallel()

		cmd := webhookCmdForTest()
		require.NoError(t, cmd.Flags().Set("rewrite-ext", "true"))

		assert.True(t, rewriteExtMode(config.Default(), cmd))
	})

	t.Run("flag off wins over config", func(t *testing.T) {
		t.Parallel()

		cmd := webhookCmdForTest()
		require.NoError(t, cmd.Flags().Set("rewrite-ext", "false"))

		assert.False(t, rewriteExtMode(cfg, cmd))
	})

	t.Run("config serves when flag untouched", func(t *testing.T) {
		t.Parallel()

		assert.True(t, rewriteExtMode(cfg, webhookCmdForTest()))
	})

	t.Run("default off", func(t *testing.T) {
		t.Parallel()

		assert.False(t, rewriteExtMode(config.Default(), &cobra.Command{Use: "bare"}))
	})
}

// TestWebhookPayloadPinsShape pins the payload builder: counters copied,
// took converted to seconds, parked_until only on park, errors capped at 3.
func TestWebhookPayloadPinsShape(t *testing.T) {
	t.Parallel()

	res := download.Result{
		Downloaded: 7, Skipped: 1, Failed: 4, Interrupted: 2, Duplicates: 3, Bytes: 4096,
		ResumeAt:    time.Date(2026, 9, 7, 12, 1, 0, 0, time.UTC),
		FailReasons: map[string]int64{"a": 3, "b": 2, "c": 2, "d": 1},
	}

	payload := webhookPayload(notify.EventParked, "run-1", "default", 5, 9, res, 1500*time.Millisecond)

	assert.Equal(t, notify.EventParked, payload.Event)
	assert.Equal(t, "run-1", payload.RunID)
	assert.Equal(t, "default", payload.Account)
	assert.Equal(t, 5, payload.Chats)
	assert.Equal(t, 9, payload.FilesMatched)
	assert.EqualValues(t, 7, payload.Downloaded)
	assert.EqualValues(t, 1, payload.Skipped)
	assert.EqualValues(t, 4, payload.Failed)
	assert.EqualValues(t, 2, payload.Interrupted)
	assert.EqualValues(t, 3, payload.Duplicates)
	assert.EqualValues(t, 4096, payload.Bytes)
	assert.InDelta(t, 1.5, payload.TookSeconds, 1e-9)
	assert.Equal(t, "2026-09-07T12:01:00Z", payload.ParkedUntil)
	assert.Equal(t, []string{"a", "b", "c"}, payload.Errors, "top-3 FAIL reasons, ties lexical")

	done := webhookPayload(notify.EventDone, "run-1", "default", 5, 9, download.Result{FailReasons: map[string]int64{"a": 1}}, time.Second)
	assert.Empty(t, done.ParkedUntil, "parked_until rides only on park events")
	assert.Equal(t, []string{"a"}, done.Errors)
}

// TestPostRunWebhookFiresBothEvents pins the wiring: done and parked both
// POST their payload, and a dead endpoint only dims a warning on stderr.
func TestPostRunWebhookFiresBothEvents(t *testing.T) {
	t.Parallel()

	var (
		mu       sync.Mutex
		received []notify.Payload
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload notify.Payload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode webhook payload: %v", err)

			return
		}

		mu.Lock()
		received = append(received, payload)
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cmd := webhookCmdForTest()
	require.NoError(t, cmd.Flags().Set("notify-webhook", server.URL))

	app := &App{cfg: config.Default()}
	app.errStyle, _ = stylersFor(StyleOptions{}, false, false)

	res := download.Result{Downloaded: 2, ResumeAt: time.Date(2026, 9, 7, 12, 1, 0, 0, time.UTC)}

	postRunWebhook(t.Context(), cmd, app, notify.EventDone, "run-1", "acc", 1, 2, download.Result{Downloaded: 2}, 2*time.Second)
	postRunWebhook(t.Context(), cmd, app, notify.EventParked, "run-1", "acc", 1, 2, res, 2*time.Second)

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, received, 2)
	assert.Equal(t, notify.EventDone, received[0].Event)
	assert.Empty(t, received[0].ParkedUntil)
	assert.Equal(t, notify.EventParked, received[1].Event)
	assert.Equal(t, "2026-09-07T12:01:00Z", received[1].ParkedUntil)
}

func TestPostRunWebhookDisabledWithoutURL(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("no URL configured: nothing must POST")
	}))
	defer server.Close()

	cmd := &cobra.Command{Use: "bare"}

	var errOut bytes.Buffer

	cmd.SetErr(&errOut)

	app := &App{cfg: config.Default()}
	app.errStyle, _ = stylersFor(StyleOptions{}, false, false)

	postRunWebhook(t.Context(), cmd, app, notify.EventDone, "run-1", "acc", 1, 0, download.Result{}, time.Second)

	assert.Empty(t, errOut.String())
}

// TestPostRunWebhookFailureIsDimWarning pins the never-fails-the-run
// contract: a dead endpoint writes one dim warning to stderr and returns.
func TestPostRunWebhookFailureIsDimWarning(t *testing.T) {
	t.Parallel()

	cmd := webhookCmdForTest()
	require.NoError(t, cmd.Flags().Set("notify-webhook", "http://127.0.0.1:1/dead"))

	var errOut bytes.Buffer

	cmd.SetErr(&errOut)

	app := &App{cfg: config.Default()}
	app.errStyle, _ = stylersFor(StyleOptions{}, false, false)

	postRunWebhook(t.Context(), cmd, app, notify.EventDone, "run-1", "acc", 1, 0, download.Result{}, time.Second)

	assert.Contains(t, errOut.String(), "webhook", "the failure surfaces as a dim stderr warning")
}
