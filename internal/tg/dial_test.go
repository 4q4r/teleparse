package tg_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"teleparse/internal/tg"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	handshakeTimeout  = 5 * time.Second
	echoPayload       = "ping"
	echoReply         = "pong"
	nulByte           = '\x00'
	socks4HeaderLen   = 8
	socks4ReplyLen    = 8
	socks4GrantedByte = 0x5A
)

func serveSocks4(t *testing.T, code byte) string {
	t.Helper()

	listener := listenLocal(t)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}

		defer func() { _ = conn.Close() }()

		_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))

		head := make([]byte, socks4HeaderLen)
		if _, err := io.ReadFull(conn, head); err != nil {
			return
		}

		userID, err := bufio.NewReader(conn).ReadString(nulByte)
		if err != nil {
			return
		}

		_ = userID

		reply := make([]byte, socks4ReplyLen)
		reply[1] = code

		if _, err := conn.Write(reply); err != nil {
			return
		}

		buf := make([]byte, len(echoPayload))
		if _, err := io.ReadFull(conn, buf); err != nil {
			return
		}

		_, _ = conn.Write([]byte(echoReply))
	}()

	return listener.Addr().String()
}

func TestSocks4DialGranted(t *testing.T) {
	t.Parallel()

	proxyAddr := serveSocks4(t, socks4GrantedByte)

	dial := tg.Socks4DialFunc(proxyAddr, "teleparse")

	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	defer cancel()

	conn, err := dial(ctx, "tcp", "93.184.216.34:443")
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	_, err = conn.Write([]byte(echoPayload))
	require.NoError(t, err)

	buf := make([]byte, len(echoReply))
	_, err = io.ReadFull(conn, buf)
	require.NoError(t, err)
	assert.Equal(t, echoReply, string(buf))
}

func TestSocks4DialRefused(t *testing.T) {
	t.Parallel()

	proxyAddr := serveSocks4(t, 0x5B)

	dial := tg.Socks4DialFunc(proxyAddr, "")

	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	defer cancel()

	_, err := dial(ctx, "tcp", "93.184.216.34:443")
	assert.ErrorIs(t, err, tg.ErrProxyRefused)
}

func TestSocks4DialCanceledContext(t *testing.T) {
	t.Parallel()

	proxyAddr := serveSocks4(t, socks4GrantedByte)

	dial := tg.Socks4DialFunc(proxyAddr, "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := dial(ctx, "tcp", "93.184.216.34:443")
	assert.ErrorIs(t, err, context.Canceled)
}

func TestSocks4DialUnreachableProxy(t *testing.T) {
	t.Parallel()

	dial := tg.Socks4DialFunc("127.0.0.1:1", "")

	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	defer cancel()

	_, err := dial(ctx, "tcp", "1.2.3.4:443")
	assert.Error(t, err)
}

func TestSocks4DialResolvesHostname(t *testing.T) {
	t.Parallel()

	proxyAddr := serveSocks4(t, socks4GrantedByte)

	dial := tg.Socks4DialFunc(proxyAddr, "")

	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	defer cancel()

	conn, err := dial(ctx, "tcp", "localhost:443")
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
}

func TestSocks4DialRejectsBadTarget(t *testing.T) {
	t.Parallel()

	dial := tg.Socks4DialFunc("127.0.0.1:1", "")

	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	defer cancel()

	for _, target := range []string{"1.2.3.4", "1.2.3.4:http", "host:notaport"} {
		_, err := dial(ctx, "tcp", target)
		assert.Error(t, err, "target %q", target)
	}
}

func serveHTTPConnect(t *testing.T, statusLine string, got *string) string {
	t.Helper()

	listener := listenLocal(t)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}

		defer func() { _ = conn.Close() }()

		_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))

		reader := bufio.NewReader(conn)
		request, err := reader.ReadString('\n')
		if err != nil {
			return
		}

		headers := &strings.Builder{}
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}

			headers.WriteString(line)
			if line == "\r\n" {
				break
			}
		}

		*got = request + headers.String()

		body := statusLine + "\r\nContent-Length: 0\r\n\r\n"
		if _, err := conn.Write([]byte(body)); err != nil {
			return
		}

		buf := make([]byte, len(echoPayload))
		if _, err := io.ReadFull(conn, buf); err != nil {
			return
		}

		_, _ = conn.Write([]byte(echoReply))
	}()

	return listener.Addr().String()
}

func TestHTTPConnectDialGranted(t *testing.T) {
	t.Parallel()

	got := ""
	proxyAddr := serveHTTPConnect(t, "HTTP/1.1 200 Connection established", &got)

	dial := tg.HTTPConnectDialFunc(proxyAddr, "proxyuser", "proxypass")

	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	defer cancel()

	conn, err := dial(ctx, "tcp", "example.com:443")
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	assert.Contains(t, got, "CONNECT example.com:443 HTTP/1.1\r\n")
	assert.Contains(t, got, "Host: example.com:443\r\n")

	expected := "Basic " + base64.StdEncoding.EncodeToString([]byte("proxyuser:proxypass"))
	assert.Contains(t, got, "Proxy-Authorization: "+expected+"\r\n")

	_, err = conn.Write([]byte(echoPayload))
	require.NoError(t, err)

	buf := make([]byte, len(echoReply))
	_, err = io.ReadFull(conn, buf)
	require.NoError(t, err)
	assert.Equal(t, echoReply, string(buf))
}

func TestHTTPConnectDialNoAuth(t *testing.T) {
	t.Parallel()

	got := ""
	proxyAddr := serveHTTPConnect(t, "HTTP/1.1 200 OK", &got)

	dial := tg.HTTPConnectDialFunc(proxyAddr, "", "")

	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	defer cancel()

	conn, err := dial(ctx, "tcp", "example.com:443")
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	assert.NotContains(t, got, "Proxy-Authorization")
}

func TestHTTPConnectDialRefused(t *testing.T) {
	t.Parallel()

	got := ""
	proxyAddr := serveHTTPConnect(t, "HTTP/1.1 407 Proxy Authentication Required", &got)

	dial := tg.HTTPConnectDialFunc(proxyAddr, "user", "pass")

	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	defer cancel()

	_, err := dial(ctx, "tcp", "example.com:443")
	assert.ErrorIs(t, err, tg.ErrProxyRefused)
}

func TestSocks4RequestBytes(t *testing.T) {
	t.Parallel()

	listener := listenLocal(t)

	type request struct {
		head   []byte
		userID string
	}
	captured := make(chan request, 1)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			close(captured)
			return
		}

		defer func() { _ = conn.Close() }()

		_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))

		head := make([]byte, socks4HeaderLen)
		if _, err := io.ReadFull(conn, head); err != nil {
			close(captured)
			return
		}

		userID, err := bufio.NewReader(conn).ReadString(nulByte)
		if err != nil {
			close(captured)
			return
		}

		reply := make([]byte, socks4ReplyLen)
		reply[1] = socks4GrantedByte
		_, _ = conn.Write(reply)

		captured <- request{head: head, userID: userID}
	}()

	dial := tg.Socks4DialFunc(listener.Addr().String(), "tester")

	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	defer cancel()

	conn, err := dial(ctx, "tcp", "10.1.2.3:8080")
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	req, ok := <-captured
	require.True(t, ok)

	assert.Equal(t, byte(0x04), req.head[0])
	assert.Equal(t, byte(0x01), req.head[1])
	assert.Equal(t, uint16(8080), binary.BigEndian.Uint16(req.head[2:4]))
	assert.Equal(t, net.IP{10, 1, 2, 3}, net.IP(req.head[4:8]))
	assert.Equal(t, "tester\x00", req.userID)
}

// listenLocal opens a throwaway loopback listener for handshake tests.
func listenLocal(t *testing.T) net.Listener {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	return listener
}
