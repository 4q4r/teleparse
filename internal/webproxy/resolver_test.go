package webproxy_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/webproxy"

	"github.com/coder/websocket"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/mtproxy/obfuscated2"
	"github.com/gotd/td/telegram/dcs"
	"github.com/stretchr/testify/require"
)

// fakeMTProxy accepts obfuscated2 clients and echoes decrypted bytes back,
// recording the requested DC.
func fakeMTProxy(t *testing.T, secret []byte) (net.Addr, *atomic.Int32) {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	var seenDC atomic.Int32

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			go func() {
				defer func() { _ = conn.Close() }()

				rw, meta, err := obfuscated2.Accept(conn, secret)
				if err != nil {
					return
				}

				seenDC.Store(int32(meta.DC))

				_, _ = io.Copy(rw, rw)
			}()
		}
	}()

	return listener.Addr(), &seenDC
}

// fakeRelay is a minimal single-stream tproxy relay: bridge page, session
// create, multiplexed WebSocket carrier, and backend TCP forwarding.
func fakeRelay(t *testing.T, secret []byte, backend net.Addr) *httptest.Server {
	t.Helper()

	var (
		mu       sync.Mutex
		issued   = map[string]bool{}
		hostname = "proxy.example.com"
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			capability := webproxy.Capability(secret, hostname)
			if r.URL.RawQuery != "bridge="+capability {
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, "<html>index</html>")

				return
			}

			token := strings.Repeat("B", 43)
			mu.Lock()
			issued[token] = true
			mu.Unlock()

			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w,
				`<script>const bootstrap="`+token+`",carrierMode="websocket";</script>`)
		case "/api/v1/session":
			if r.Host != hostname {
				t.Errorf("session host %q", r.Host)
			}

			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusNoContent)

				return
			}

			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusNotFound)

				return
			}

			hello, err := webproxy.Decode(body)
			if err != nil || hello.Type != webproxy.TypeHello {
				w.WriteHeader(http.StatusNotFound)

				return
			}

			token := strings.Repeat("B", 43)
			w.Header().Set("X-Session-Token", token)
			w.Header().Set("X-Carrier-Mode", "websocket")
			w.Header().Set("X-Down-Cursor", "0")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(webproxy.Encode(webproxy.Frame{Type: webproxy.TypeWelcome}))
		case "/api/v1/ws":
			serveFakeWS(t, w, r, backend)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	return server
}

// serveFakeWS relays frames between the client WebSocket and the backend.
func serveFakeWS(t *testing.T, w http.ResponseWriter, r *http.Request, backend net.Addr) {
	t.Helper()

	protocol := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Protocol"))
	if !strings.HasPrefix(protocol, "tproxy-v1.") || strings.Contains(protocol, ",") {
		w.WriteHeader(http.StatusNotFound)

		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{protocol}})
	if err != nil {
		return
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "done") }()

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	upstream, err := (&net.Dialer{}).DialContext(r.Context(), "tcp", backend.String())
	if err != nil {
		return
	}
	defer func() { _ = upstream.Close() }()

	// backend -> client DATA frames.
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := upstream.Read(buf)
			if n > 0 {
				frame := webproxy.Encode(webproxy.Frame{
					Type: webproxy.TypeData, StreamID: 1, Payload: buf[:n],
				})
				writeCtx, cancelWrite := context.WithTimeout(ctx, 5*time.Second)

				err := conn.Write(writeCtx, websocket.MessageBinary, frame)
				cancelWrite()
				if err != nil {
					return
				}
			}

			if err != nil {
				closeCtx, cancelClose := context.WithTimeout(ctx, time.Second)
				_ = conn.Write(closeCtx, websocket.MessageBinary,
					webproxy.Encode(webproxy.Frame{Type: webproxy.TypeClose, StreamID: 1}))
				cancelClose()

				return
			}
		}
	}()

	// client -> backend.
	for {
		kind, batch, err := conn.Read(ctx)
		if err != nil {
			return
		}

		if kind != websocket.MessageBinary {
			return
		}

		frames, err := webproxy.DecodeAll(batch)
		if err != nil {
			t.Errorf("relay decode: %v", err)

			return
		}

		for _, frame := range frames {
			if frame.Type != webproxy.TypeData || frame.StreamID != 1 {
				continue
			}

			if _, err := upstream.Write(frame.Payload); err != nil {
				return
			}
		}
	}
}

func resolverTestEnv(t *testing.T) (*httptest.Server, *atomic.Int32, []byte) {
	t.Helper()

	secret := testSecret(t)
	backendAddr, seenDC := fakeMTProxy(t, secret)
	server := fakeRelay(t, secret, backendAddr)

	return server, seenDC, secret
}

func resolverConfig(server *httptest.Server, secret []byte) webproxy.Config {
	return webproxy.Config{
		Host:    "proxy.example.com",
		Secret:  secret,
		BaseURL: server.URL,
		Client:  server.Client(),
	}
}

func TestResolverPrimaryRoundtrip(t *testing.T) {
	t.Parallel()

	server, seenDC, secret := resolverTestEnv(t)

	resolver, err := webproxy.NewResolver(resolverConfig(server, secret))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var asResolver dcs.Resolver = resolver
	conn, err := asResolver.Primary(ctx, 2, dcs.List{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	require.Eventually(t, func() bool { return seenDC.Load() == 2 }, 5*time.Second, 5*time.Millisecond)

	payload := &bin.Buffer{Buf: []byte("hello over web proxy")}
	require.NoError(t, conn.Send(ctx, payload))

	received := &bin.Buffer{}
	require.NoError(t, conn.Recv(ctx, received))
	require.Equal(t, "hello over web proxy", string(received.Buf))
}

func TestResolverMediaOnlyNegatesDC(t *testing.T) {
	t.Parallel()

	server, seenDC, secret := resolverTestEnv(t)

	resolver, err := webproxy.NewResolver(resolverConfig(server, secret))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := resolver.MediaOnly(ctx, 3, dcs.List{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	negated := -3
	want := int32(uint16(negated))
	require.Eventually(t, func() bool {
		return seenDC.Load() == want
	}, 5*time.Second, 5*time.Millisecond)
}

func TestResolverCDNKeepsDC(t *testing.T) {
	t.Parallel()

	server, seenDC, secret := resolverTestEnv(t)

	resolver, err := webproxy.NewResolver(resolverConfig(server, secret))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := resolver.CDN(ctx, 4, dcs.List{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	require.Eventually(t, func() bool { return seenDC.Load() == 4 }, 5*time.Second, 5*time.Millisecond)
}

func TestResolverRejectsInvalidSecret(t *testing.T) {
	t.Parallel()

	_, err := webproxy.NewResolver(webproxy.Config{Host: "proxy.example.com", Secret: []byte{1, 2}})
	require.Error(t, err)
	require.NotErrorIs(t, err, webproxy.ErrSessionLost)
}
