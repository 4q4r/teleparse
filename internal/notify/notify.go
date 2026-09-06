// Package notify delivers run-outcome webhooks: one small JSON payload
// POSTed when a download run ends and when a flood wait parks it. Delivery
// is best-effort — a per-attempt timeout bounds each request, exactly one
// retry follows a failure, and errors surface only to the caller's
// discretion.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// ErrBadStatus marks a webhook endpoint that answered outside the 2xx
// range; the status code rides in the wrapped message.
var ErrBadStatus = errors.New("webhook endpoint answered a non-2xx status")

// Event names carried in Payload.Event.
const (
	EventDone   = "done"
	EventParked = "parked"
)

// Delivery tuning: the per-attempt HTTP timeout and the total attempt
// budget (the initial POST plus one retry).
const (
	requestTimeout = 5 * time.Second
	maxAttempts    = 2
)

// Payload is the webhook body describing one run outcome. Counters mirror
// the download Result; errors lists the top FAIL reasons.
type Payload struct {
	Event        string   `json:"event"`
	RunID        string   `json:"run_id"`
	Account      string   `json:"account"`
	Chats        int      `json:"chats"`
	FilesMatched int      `json:"files_matched"`
	Downloaded   int64    `json:"downloaded"`
	Skipped      int64    `json:"skipped"`
	Failed       int64    `json:"failed"`
	Interrupted  int64    `json:"interrupted"`
	Duplicates   int64    `json:"duplicates"`
	Bytes        int64    `json:"bytes"`
	TookSeconds  float64  `json:"took_seconds"`
	ParkedUntil  string   `json:"parked_until,omitempty"`
	Errors       []string `json:"errors"`
}

// Client posts payloads to one webhook endpoint.
type Client struct {
	url  string
	http *http.Client
}

// New returns a webhook client for url with the standard request timeout.
func New(url string) *Client {
	return &Client{url: url, http: &http.Client{Timeout: requestTimeout}}
}

// Notify POSTs payload as JSON. One retry follows a failed attempt, so at
// most maxAttempts requests leave; the last error is returned wrapped.
func (c *Client) Notify(ctx context.Context, payload Payload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal webhook payload: %w", err)
	}

	var lastErr error

	for range maxAttempts {
		err := c.post(ctx, body)
		if err == nil {
			return nil
		}

		lastErr = err
	}

	return fmt.Errorf("webhook %s: %d attempts: %w", c.url, maxAttempts, lastErr)
}

// post delivers one attempt: any transport error or non-2xx status fails.
func (c *Client) post(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build webhook request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("post webhook: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("status %d: %w", resp.StatusCode, ErrBadStatus)
	}

	return nil
}
