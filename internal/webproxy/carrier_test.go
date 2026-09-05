package webproxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/webproxy"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func wsTestConfig(server *httptest.Server) webproxy.Config {
	return webproxy.Config{
		BaseURL: server.URL,
		Host:    "proxy.example.com",
		Secret:  []byte{0x01},
	}
}

func TestWebSocketCarrierRoundtrip(t *testing.T) {
	t.Parallel()

	received := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{"tproxy-v1.token-abc"},
		})
		if err != nil {
			t.Error(err)

			return
		}

		defer func() { _ = conn.Close(websocket.StatusNormalClosure, "done") }()

		if proto := conn.Subprotocol(); proto != "tproxy-v1.token-abc" {
			t.Errorf("subprotocol %q", proto)
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		kind, batch, err := conn.Read(ctx)
		if err != nil {
			t.Error(err)

			return
		}

		if kind != websocket.MessageBinary {
			t.Errorf("message kind %d", kind)

			return
		}

		received <- batch

		if err := conn.Write(ctx, websocket.MessageBinary, batch); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	carrier, err := webproxy.NewCarrier(ctx, wsTestConfig(server), webproxy.ModeWebSocket, "token-abc")
	require.NoError(t, err)
	t.Cleanup(func() { _ = carrier.Close() })

	batch := webproxy.Encode(webproxy.Frame{Type: webproxy.TypeOpen, StreamID: 1})
	require.NoError(t, carrier.Send(batch))

	require.Equal(t, batch, <-received)

	echo, err := carrier.Recv()
	require.NoError(t, err)
	require.Equal(t, batch, echo)
}

func TestWebSocketCarrierRejectsSubprotocolMismatch(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)

			return
		}

		_ = conn.Close(websocket.StatusNormalClosure, "done")
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := webproxy.NewCarrier(ctx, wsTestConfig(server), webproxy.ModeWebSocket, "token-abc")
	require.ErrorIs(t, err, webproxy.ErrCarrierClosed)
}

func TestWebSocketLaneCarrierProtocol(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{"tproxy-lane-v1.token-abc.7"},
		})
		if err != nil {
			t.Error(err)

			return
		}

		defer func() { _ = conn.Close(websocket.StatusNormalClosure, "done") }()

		if proto := conn.Subprotocol(); proto != "tproxy-lane-v1.token-abc.7" {
			t.Errorf("subprotocol %q", proto)
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	carrier, err := webproxy.NewLaneCarrier(ctx, wsTestConfig(server), webproxy.ModeWebSocketLanes,
		"token-abc", 7)
	require.NoError(t, err)
	t.Cleanup(func() { _ = carrier.Close() })
}

func TestHTTPSCarrierRoundtripAndSequencing(t *testing.T) {
	t.Parallel()

	var (
		mu          sync.Mutex
		upSeqs      []string
		downCursors []string
		downCount   int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/up":
			if r.Method != http.MethodPost {
				t.Errorf("method %q", r.Method)
			}

			if ct := r.Header.Get("Content-Type"); ct != "application/octet-stream" {
				t.Errorf("content type %q", ct)
			}

			if auth := r.Header.Get("Authorization"); auth != "Bearer token-abc" {
				t.Errorf("authorization %q", auth)
			}

			if lane := r.Header.Get("X-Lane-Id"); lane != "" {
				t.Errorf("lane %q", lane)
			}

			seq := r.Header.Get("X-Up-Seq")

			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}

			if len(body) == 0 {
				t.Error("empty up body")
			}

			mu.Lock()
			upSeqs = append(upSeqs, seq)
			mu.Unlock()

			w.Header().Set("X-Up-Ack", seq)
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/down":
			if r.Method != http.MethodPost {
				t.Errorf("method %q", r.Method)
			}

			if auth := r.Header.Get("Authorization"); auth != "Bearer token-abc" {
				t.Errorf("authorization %q", auth)
			}

			if ct := r.Header.Get("Content-Type"); ct != "" {
				t.Errorf("content type %q", ct)
			}

			if lane := r.Header.Get("X-Lane-Id"); lane != "" {
				t.Errorf("lane %q", lane)
			}

			cursor := r.Header.Get("X-Down-Cursor")
			mu.Lock()
			downCursors = append(downCursors, cursor)
			downCount++
			index := downCount
			mu.Unlock()

			wantCursors := map[int]string{1: "0", 2: "1"}

			switch index {
			case 1:
				if cursor != wantCursors[index] {
					t.Errorf("cursor %q", cursor)
				}

				w.Header().Set("X-Down-Cursor", "1")
				w.Header().Set("Content-Type", "application/octet-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(webproxy.Encode(webproxy.Frame{
					Type: webproxy.TypeData, StreamID: 1, Payload: []byte("relay-hello"),
				}))
			case 2:
				if cursor != wantCursors[index] {
					t.Errorf("cursor %q", cursor)
				}

				w.Header().Set("X-Down-Cursor", "2")
				w.Header().Set("Content-Type", "application/octet-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(webproxy.Encode(webproxy.Frame{
					Type: webproxy.TypeWindow, StreamID: 1, Payload: webproxy.WindowPayload(9),
				}))
			default:
				if cursor != "2" {
					t.Errorf("cursor %q", cursor)
				}

				time.Sleep(20 * time.Millisecond)
				w.Header().Set("X-Down-Cursor", "2")
				w.WriteHeader(http.StatusNoContent)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	carrier, err := webproxy.NewCarrier(ctx, wsTestConfig(server), webproxy.ModeHTTPS, "token-abc")
	require.NoError(t, err)
	t.Cleanup(func() { _ = carrier.Close() })

	first := webproxy.Encode(webproxy.Frame{Type: webproxy.TypeOpen, StreamID: 1})
	require.NoError(t, carrier.Send(first))

	batch, err := carrier.Recv()
	require.NoError(t, err)
	frames, err := webproxy.DecodeAll(batch)
	require.NoError(t, err)
	require.Len(t, frames, 1)
	require.Equal(t, webproxy.TypeData, frames[0].Type)
	require.Equal(t, "relay-hello", string(frames[0].Payload))

	second := webproxy.Encode(webproxy.Frame{
		Type: webproxy.TypeData, StreamID: 1, Payload: []byte("client-data"),
	})
	require.NoError(t, carrier.Send(second))

	batch, err = carrier.Recv()
	require.NoError(t, err)
	frames, err = webproxy.DecodeAll(batch)
	require.NoError(t, err)
	require.Len(t, frames, 1)
	require.Equal(t, webproxy.TypeWindow, frames[0].Type)

	mu.Lock()
	require.Equal(t, []string{"1", "2"}, upSeqs)
	require.Equal(t, []string{"0", "1"}, downCursors[:2])
	mu.Unlock()
}

func TestHTTPSCarrierRetriesBackpressureByteIdentical(t *testing.T) {
	t.Parallel()

	var attempts int
	var bodies [][]byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/up":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}

			attempts++
			bodies = append(bodies, body)

			if attempts == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusServiceUnavailable)

				return
			}

			w.Header().Set("X-Up-Ack", "1")
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/down":
			time.Sleep(50 * time.Millisecond)
			w.Header().Set("X-Down-Cursor", "0")
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	carrier, err := webproxy.NewCarrier(ctx, wsTestConfig(server), webproxy.ModeHTTPS, "token-abc")
	require.NoError(t, err)
	t.Cleanup(func() { _ = carrier.Close() })

	batch := webproxy.Encode(webproxy.Frame{
		Type: webproxy.TypeData, StreamID: 1, Payload: []byte("retry-me"),
	})
	require.NoError(t, carrier.Send(batch))
	require.Equal(t, 2, attempts)
	require.Equal(t, bodies[0], bodies[1])
}

func TestHTTPSLaneCarrierHeadersAndClosure(t *testing.T) {
	t.Parallel()

	var polls int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/up":
			if lane := r.Header.Get("X-Lane-Id"); lane != "7" {
				t.Errorf("up lane %q", lane)
			}

			w.Header().Set("X-Up-Ack", r.Header.Get("X-Up-Seq"))
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/down":
			if lane := r.Header.Get("X-Lane-Id"); lane != "7" {
				t.Errorf("down lane %q", lane)
			}

			polls++

			if polls == 1 {
				w.Header().Set("X-Down-Cursor", "1")
				w.Header().Set("Content-Type", "application/octet-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(webproxy.Encode(webproxy.Frame{
					Type: webproxy.TypeClose, StreamID: 7,
				}))

				return
			}

			time.Sleep(20 * time.Millisecond)
			w.Header().Set("X-Down-Cursor", "1")
			w.Header().Set("X-Lane-Closed", "1")
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	carrier, err := webproxy.NewLaneCarrier(ctx, wsTestConfig(server), webproxy.ModeHTTPSLanes,
		"token-abc", 7)
	require.NoError(t, err)
	t.Cleanup(func() { _ = carrier.Close() })

	require.NoError(t, carrier.Send(webproxy.Encode(webproxy.Frame{Type: webproxy.TypeOpen, StreamID: 7})))

	batch, err := carrier.Recv()
	require.NoError(t, err)
	require.Equal(t, webproxy.Encode(webproxy.Frame{Type: webproxy.TypeClose, StreamID: 7}), batch)

	_, err = carrier.Recv()
	require.ErrorIs(t, err, webproxy.ErrLaneClosed)
}

func TestHTTPSCarrierFatalDownlinkFailsRecv(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/up":
			w.Header().Set("X-Up-Ack", r.Header.Get("X-Up-Seq"))
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/down":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	carrier, err := webproxy.NewCarrier(ctx, wsTestConfig(server), webproxy.ModeHTTPS, "token-abc")
	require.NoError(t, err)
	t.Cleanup(func() { _ = carrier.Close() })

	_, err = carrier.Recv()
	require.Error(t, err)
	require.NotErrorIs(t, err, webproxy.ErrLaneClosed)
	require.NotEmpty(t, err.Error())
}
