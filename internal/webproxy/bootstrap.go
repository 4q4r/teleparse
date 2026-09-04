package webproxy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Bootstrap wire constants from PROTOCOL.md and the reference relay.
const (
	// capabilityLabel is the frozen v1 domain-separation label.
	capabilityLabel = "tdesktop-web-proxy-bridge-v1"
	// sessionPath is the session create and delete endpoint.
	sessionPath = "/api/v1/session"
	// helloProtocolVersion is the single HELLO payload byte.
	helloProtocolVersion = 0x01
	// maxRetryAfter caps a single honored Retry-After delay.
	maxRetryAfter = 30 * time.Second
	// requestRetryBudget bounds the total 503 retry window.
	requestRetryBudget = 90 * time.Second
	// maxNetworkErrors bounds consecutive transport-level failures.
	maxNetworkErrors = 9
	// retryBackoffBase is the initial network-error backoff.
	retryBackoffBase = 250 * time.Millisecond
	// retryBackoffCap is the maximum network-error backoff.
	retryBackoffCap = 5 * time.Second
	// maxBridgePageSize bounds the bridge page read.
	maxBridgePageSize = 1 << 20
	// maxSessionBodySize bounds the session endpoint bodies.
	maxSessionBodySize = HeaderSize + MaxPayload
)

// Bootstrap sentinels.
var (
	// ErrBootstrapFailed reports any failed bootstrap exchange.
	ErrBootstrapFailed = errors.New("bootstrap failed")
	// ErrCarrierModeMismatch reports a server mode conflicting with the
	// configured preference.
	ErrCarrierModeMismatch = errors.New("carrier mode mismatch")
)

var (
	// bootstrapTokenPattern matches the reference bridge page assignment.
	bootstrapTokenPattern = regexp.MustCompile(`bootstrap\s*=\s*"([A-Za-z0-9_-]{43})"`)
	// anyTokenPattern is the lenient fallback for a quoted 43-char token.
	anyTokenPattern = regexp.MustCompile(`"([A-Za-z0-9_-]{43})"`)
)

// Capability derives the bridge capability: unpadded base64url of
// HMAC-SHA256 keyed by the decoded secret over the label, a newline, and the
// canonical host.
func Capability(secret []byte, host string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(capabilityLabel + "\n" + host))

	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// BootstrapOptions configures one bootstrap exchange.
type BootstrapOptions struct {
	// Client performs the HTTP requests.
	Client *http.Client
	// BaseURL is the relay origin, https://Host in production.
	BaseURL string
	// Host is the canonical lowercase hostname.
	Host string
	// Secret is the decoded MTProxy secret, dd or ee prefix retained.
	Secret []byte
	// Mode is the preferred carrier mode; ModeAuto accepts the server choice.
	Mode CarrierMode
}

// BootstrapResult is the outcome of a successful session creation.
type BootstrapResult struct {
	// SessionToken authenticates the created carrier session.
	SessionToken string
	// CarrierMode is the relay-selected carrier transport.
	CarrierMode CarrierMode
	// DownCursor is the initial downlink cursor.
	DownCursor uint64
}

// RunBootstrap loads the bridge page, parses the bootstrap token, and
// exchanges a HELLO frame for a session token and carrier mode.
func RunBootstrap(ctx context.Context, opts BootstrapOptions) (BootstrapResult, error) {
	result := BootstrapResult{}

	if opts.Mode == "" {
		opts.Mode = ModeAuto
	}

	page, err := fetchBridgePage(ctx, opts.Client, opts.BaseURL, opts.Host,
		Capability(opts.Secret, opts.Host))
	if err != nil {
		return result, err
	}

	token, err := parseBootstrapToken(page)
	if err != nil {
		return result, err
	}

	hello := Encode(Frame{Type: TypeHello, Payload: []byte{helloProtocolVersion}})

	body, header, err := postSession(ctx, opts.Client, opts.BaseURL, opts.Host, token, hello)
	if err != nil {
		return result, err
	}

	welcome, err := Decode(body)
	if err != nil {
		return result, fmt.Errorf("%w: decode WELCOME: %w", ErrBootstrapFailed, err)
	}

	if welcome.Type != TypeWelcome || welcome.StreamID != 0 || len(welcome.Payload) != 0 {
		return result, fmt.Errorf("%w: body is not a single WELCOME frame", ErrBootstrapFailed)
	}

	mode := CarrierMode(header.Get("X-Carrier-Mode"))
	if !mode.Offered() {
		return result, fmt.Errorf("%w: server offered unknown mode %q", ErrBootstrapFailed, mode)
	}

	if opts.Mode != ModeAuto && opts.Mode != mode {
		return result, fmt.Errorf("%w: want %q, server offers %q",
			ErrCarrierModeMismatch, opts.Mode, mode)
	}

	result.SessionToken = header.Get("X-Session-Token")
	if result.SessionToken == "" {
		return result, fmt.Errorf("%w: missing X-Session-Token", ErrBootstrapFailed)
	}

	result.CarrierMode = mode

	cursor, err := strconv.ParseUint(header.Get("X-Down-Cursor"), 10, 64)
	if err != nil {
		return result, fmt.Errorf("%w: parse X-Down-Cursor: %w", ErrBootstrapFailed, err)
	}

	result.DownCursor = cursor

	return result, nil
}

// DeleteSession closes the session identified by token; it is idempotent for
// a currently authenticated session. The host stamps the canonical Host
// header.
func DeleteSession(ctx context.Context, client *http.Client, baseURL, host, token string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, baseURL+sessionPath, nil)
	if err != nil {
		return fmt.Errorf("build delete request: %w", err)
	}

	request.Host = host
	request.Header.Set("Authorization", "Bearer "+token)

	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}

	defer response.Body.Close()

	_, _ = io.Copy(io.Discard, response.Body)

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("delete session: status %d: %w", response.StatusCode, ErrSessionClosed)
	}

	return nil
}

func fetchBridgePage(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	host string,
	capability string,
) ([]byte, error) {
	response, err := exchange(ctx, client, func() (*http.Request, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/?bridge="+capability, nil)
		if err != nil {
			return nil, fmt.Errorf("build bridge request: %w", err)
		}

		request.Host = host

		return request, nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: fetch bridge page: %w", ErrBootstrapFailed, err)
	}

	defer response.Body.Close()

	page, err := io.ReadAll(io.LimitReader(response.Body, maxBridgePageSize))
	if err != nil {
		return nil, fmt.Errorf("%w: read bridge page: %w", ErrBootstrapFailed, err)
	}

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: bridge page status %d", ErrBootstrapFailed, response.StatusCode)
	}

	return page, nil
}

func parseBootstrapToken(page []byte) (string, error) {
	if match := bootstrapTokenPattern.FindSubmatch(page); match != nil {
		return string(match[1]), nil
	}

	if match := anyTokenPattern.FindSubmatch(page); match != nil {
		return string(match[1]), nil
	}

	return "", fmt.Errorf("%w: bridge page carries no bootstrap token", ErrBootstrapFailed)
}

func postSession(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	host string,
	token string,
	hello []byte,
) ([]byte, http.Header, error) {
	response, err := exchange(ctx, client, func() (*http.Request, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+sessionPath,
			strings.NewReader(string(hello)))
		if err != nil {
			return nil, fmt.Errorf("build session request: %w", err)
		}

		request.Host = host
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", octetStream)
		request.ContentLength = int64(len(hello))

		return request, nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("%w: create session: %w", ErrBootstrapFailed, err)
	}

	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxSessionBodySize))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: read session body: %w", ErrBootstrapFailed, err)
	}

	if response.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("%w: create session: status %d",
			ErrBootstrapFailed, response.StatusCode)
	}

	return body, response.Header, nil
}

// exchange performs an HTTP request, retrying 503 responses after the
// advertised Retry-After and network errors with bounded backoff, mirroring
// the reference bridge carrier loop. The caller owns the response body.
func exchange(
	ctx context.Context,
	client *http.Client,
	build func() (*http.Request, error),
) (*http.Response, error) {
	deadline := time.Now().Add(requestRetryBudget)
	delay := retryBackoffBase
	failures := 0

	for {
		request, err := build()
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}

		response, err := client.Do(request)

		switch {
		case err != nil:
			if ctx.Err() != nil {
				return nil, fmt.Errorf("request: %w", err)
			}

			failures++
			if failures >= maxNetworkErrors {
				return nil, fmt.Errorf("request: %w", err)
			}
		case response.StatusCode != http.StatusServiceUnavailable:
			return response, nil
		default:
			wait := retryAfter(response.Header.Get("Retry-After"))
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()

			if time.Now().Add(wait).After(deadline) {
				return nil, fmt.Errorf("service unavailable retry budget exceeded: %w", ErrUnavailable)
			}

			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("retry wait: %w", ctx.Err())
			case <-time.After(wait):
			}

			continue
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("retry budget exceeded: %w", ErrUnavailable)
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("retry wait: %w", ctx.Err())
		case <-time.After(delay):
		}

		delay = min(delay*2, retryBackoffCap)
	}
}

func retryAfter(value string) time.Duration {
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil || seconds < 0 {
		return 0
	}

	return min(time.Duration(seconds*float64(time.Second)), maxRetryAfter)
}
