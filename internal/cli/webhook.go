package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/4q4r/teleparse/internal/config"
	"github.com/4q4r/teleparse/internal/download"
	"github.com/4q4r/teleparse/internal/notify"

	"github.com/spf13/cobra"
)

// webhookErrorsLimit caps the FAIL reasons carried in a webhook payload.
const webhookErrorsLimit = 3

// addNotifyWebhookFlag registers --notify-webhook on a dl-family command.
func addNotifyWebhookFlag(cmd *cobra.Command) {
	cmd.Flags().String("notify-webhook", "",
		"POST a JSON run summary to URL at run end and on flood-wait park\n"+
			"(default off; config run.notify_webhook, env TELEPARSE_WEBHOOK)")
}

// addRewriteExtFlag registers --rewrite-ext on a downloading command.
func addRewriteExtFlag(cmd *cobra.Command) {
	cmd.Flags().Bool("rewrite-ext", false,
		"rewrite a file's on-disk extension to the canonical one for its recorded\n"+
			"mime type when they disagree (config output.rewrite_ext)")
}

// notifyWebhookURL resolves the effective webhook endpoint: an explicitly
// set --notify-webhook flag wins (empty string = explicit off), then
// run.notify_webhook — where TELEPARSE_WEBHOOK already landed via env.
// An empty result disables delivery.
func notifyWebhookURL(cfg *config.Config, cmd *cobra.Command) string {
	if flag := cmd.Flags().Lookup("notify-webhook"); flag != nil && flag.Changed {
		return flag.Value.String()
	}

	return cfg.Run.NotifyWebhook
}

// rewriteExtMode resolves whether final paths rewrite their extension to
// the canonical one for the recorded mime type: an explicit --rewrite-ext
// flag wins, then output.rewrite_ext.
func rewriteExtMode(cfg *config.Config, cmd *cobra.Command) bool {
	if flag := cmd.Flags().Lookup("rewrite-ext"); flag != nil && flag.Changed {
		value, _ := cmd.Flags().GetBool("rewrite-ext")

		return value
	}

	return cfg.Output.RewriteExt
}

// webhookPayload builds the run-outcome body for event: counters mirrored
// from res, the run duration in seconds, parked_until on park events and
// the top FAIL reasons capped at webhookErrorsLimit.
func webhookPayload(
	event, runID, account string,
	chats, filesMatched int,
	res download.Result,
	took time.Duration,
) notify.Payload {
	payload := notify.Payload{
		Event:        event,
		RunID:        runID,
		Account:      account,
		Chats:        chats,
		FilesMatched: filesMatched,
		Downloaded:   res.Downloaded,
		Skipped:      res.Skipped,
		Failed:       res.Failed,
		Interrupted:  res.Interrupted,
		Duplicates:   res.Duplicates,
		Bytes:        res.Bytes,
		TookSeconds:  took.Seconds(),
		Errors:       res.TopFailures(webhookErrorsLimit),
	}

	if event == notify.EventParked {
		payload.ParkedUntil = res.ResumeAt.Format(time.RFC3339)
	}

	return payload
}

// postRunWebhook delivers the run-outcome webhook. Delivery is best-effort:
// an empty resolved URL is a no-op and a failing endpoint only dims a
// warning on stderr — it never fails the run.
func postRunWebhook(
	ctx context.Context,
	cmd *cobra.Command,
	app *App,
	event, runID, account string,
	chats, filesMatched int,
	res download.Result,
	took time.Duration,
) {
	url := notifyWebhookURL(app.cfg, cmd)
	if url == "" {
		return
	}

	payload := webhookPayload(event, runID, account, chats, filesMatched, res, took)

	if err := notify.New(url).Notify(ctx, payload); err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", app.errStyle.Dim("webhook: "+err.Error()))
	}
}
