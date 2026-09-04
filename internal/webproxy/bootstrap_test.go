package webproxy_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"teleparse/internal/webproxy"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var testBridgeToken = strings.Repeat("A", 43)

// bridgeHTML mirrors the reference bridge page shape: the bootstrap token is
// embedded as a JSON-quoted assignment inside the inline script.
var bridgeHTML = `<!doctype html>
<html><head><title>Connection</title></head>
<body><script nonce="nonce">
(()=>{'use strict';
const relayOrigin="https://proxy.example.com",bootstrap="` + testBridgeToken + `",carrierMode="websocket";
})();
</script></body></html>`

func testSecret(t *testing.T) []byte {
	t.Helper()

	secret, err := hex.DecodeString("000102030405060708090a0b0c0d0e0f")
	require.NoError(t, err)

	return secret
}

func TestCapabilityGoldenVectors(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		secret string
		want   string
	}{
		{
			name:   "simple secret",
			secret: "000102030405060708090a0b0c0d0e0f",
			want:   "MHLEY5PmW1GWqJkSrlmJpvJUiLhBH_QKy6yKg8a0JPk",
		},
		{
			name:   "dd-prefixed secret retained",
			secret: "dd000102030405060708090a0b0c0d0e0f",
			want:   "IpJrt3e7sKtzPyoXy6w-Zj6GGEvsvclN66JzQEfPYLA",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			secret, err := hex.DecodeString(tc.secret)
			require.NoError(t, err)

			require.Equal(t, tc.want, webproxy.Capability(secret, "proxy.example.com"))
		})
	}
}

func TestCapabilityDiffersPerHost(t *testing.T) {
	t.Parallel()

	secret := testSecret(t)
	require.NotEqual(t,
		webproxy.Capability(secret, "proxy.example.com"),
		webproxy.Capability(secret, "other.example.com"),
	)
}

func TestBootstrapExchangesTokenForSession(t *testing.T) {
	t.Parallel()

	secret := testSecret(t)
	var sessionCalls atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			capability := webproxy.Capability(secret, "proxy.example.com")
			if r.URL.RawQuery != "bridge="+capability {
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, "<html><body>public index</body></html>")

				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, bridgeHTML)
		case "/api/v1/session":
			sessionCalls.Add(1)
			if r.Method != http.MethodPost {
				t.Errorf("method %q", r.Method)
			}

			if auth := r.Header.Get("Authorization"); auth != "Bearer "+testBridgeToken {
				t.Errorf("authorization %q", auth)
			}

			if ct := r.Header.Get("Content-Type"); ct != "application/octet-stream" {
				t.Errorf("content type %q", ct)
			}

			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}

			wantHello := webproxy.Encode(webproxy.Frame{
				Type:    webproxy.TypeHello,
				Payload: []byte{0x01},
			})
			if !bytes.Equal(wantHello, body) {
				t.Errorf("HELLO body mismatch: got %x want %x", body, wantHello)
			}

			w.Header().Set("X-Session-Token", "SessIonToken-32bytes-aaaaaaaaaaa")
			w.Header().Set("X-Carrier-Mode", "websocket")
			w.Header().Set("X-Down-Cursor", "0")
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(webproxy.Encode(webproxy.Frame{Type: webproxy.TypeWelcome}))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := webproxy.RunBootstrap(ctx, webproxy.BootstrapOptions{
		Client:  server.Client(),
		BaseURL: server.URL,
		Host:    "proxy.example.com",
		Secret:  secret,
		Mode:    webproxy.ModeAuto,
	})
	require.NoError(t, err)
	require.Equal(t, "SessIonToken-32bytes-aaaaaaaaaaa", result.SessionToken)
	require.Equal(t, webproxy.ModeWebSocket, result.CarrierMode)
	require.Equal(t, uint64(0), result.DownCursor)
	require.Equal(t, int64(1), sessionCalls.Load())
}

func TestBootstrapRejectsWrongCapability(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "<html><body>public index, no token</body></html>")
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := webproxy.RunBootstrap(ctx, webproxy.BootstrapOptions{
		Client:  server.Client(),
		BaseURL: server.URL,
		Host:    "proxy.example.com",
		Secret:  []byte{0xAA, 0xBB},
		Mode:    webproxy.ModeAuto,
	})
	require.ErrorIs(t, err, webproxy.ErrBootstrapFailed)
}

func TestBootstrapRetriesServiceUnavailable(t *testing.T) {
	t.Parallel()

	secret := testSecret(t)
	var attempts atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, bridgeHTML)
		case "/api/v1/session":
			if attempts.Add(1) == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusServiceUnavailable)

				return
			}
			w.Header().Set("X-Session-Token", "tok")
			w.Header().Set("X-Carrier-Mode", "https")
			w.Header().Set("X-Down-Cursor", "0")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(webproxy.Encode(webproxy.Frame{Type: webproxy.TypeWelcome}))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := webproxy.RunBootstrap(ctx, webproxy.BootstrapOptions{
		Client:  server.Client(),
		BaseURL: server.URL,
		Host:    "proxy.example.com",
		Secret:  secret,
		Mode:    webproxy.ModeAuto,
	})
	require.NoError(t, err)
	require.Equal(t, "tok", result.SessionToken)
	require.Equal(t, int64(2), attempts.Load())
}

func TestBootstrapRejectsCarrierModeMismatch(t *testing.T) {
	t.Parallel()

	secret := testSecret(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, bridgeHTML)
		case "/api/v1/session":
			w.Header().Set("X-Session-Token", "tok")
			w.Header().Set("X-Carrier-Mode", "https")
			w.Header().Set("X-Down-Cursor", "0")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(webproxy.Encode(webproxy.Frame{Type: webproxy.TypeWelcome}))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := webproxy.RunBootstrap(ctx, webproxy.BootstrapOptions{
		Client:  server.Client(),
		BaseURL: server.URL,
		Host:    "proxy.example.com",
		Secret:  secret,
		Mode:    webproxy.ModeWebSocketLanes,
	})
	require.ErrorIs(t, err, webproxy.ErrCarrierModeMismatch)
}

func TestBootstrapRejectsNonWelcomeBody(t *testing.T) {
	t.Parallel()

	secret := testSecret(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, bridgeHTML)
		case "/api/v1/session":
			w.Header().Set("X-Session-Token", "tok")
			w.Header().Set("X-Carrier-Mode", "websocket")
			w.Header().Set("X-Down-Cursor", "0")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("garbage"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := webproxy.RunBootstrap(ctx, webproxy.BootstrapOptions{
		Client:  server.Client(),
		BaseURL: server.URL,
		Host:    "proxy.example.com",
		Secret:  secret,
		Mode:    webproxy.ModeAuto,
	})
	require.ErrorIs(t, err, webproxy.ErrBootstrapFailed)
}

func TestDeleteSessionClosesSession(t *testing.T) {
	t.Parallel()

	var deleted atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/session" {
			t.Errorf("path %q", r.URL.Path)
		}

		if r.Method != http.MethodDelete {
			t.Errorf("method %q", r.Method)
		}

		if auth := r.Header.Get("Authorization"); auth != "Bearer tok" {
			t.Errorf("authorization %q", auth)
		}

		if ct := r.Header.Get("Content-Type"); ct != "" {
			t.Errorf("content type %q", ct)
		}

		deleted.Store(true)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	err := webproxy.DeleteSession(context.Background(), server.Client(), server.URL, "proxy.example.com", "tok")
	require.NoError(t, err)
	require.True(t, deleted.Load())
}
