package transport_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
	"testing"

	"github.com/4q4r/teleparse/internal/transport"

	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/assert"
)

// proxyKillChain reproduces the production error shape of a local HTTP
// CONNECT proxy killing a long-lived tunnel mid-write: gotd's rpc engine
// ("send"), the transport connection ("write"), the intermediate codec
// ("write intermediate") and the carrier's *net.OpError over the errno.
func proxyKillChain(errno syscall.Errno) error {
	return fmt.Errorf("send: %w", fmt.Errorf("write: %w", fmt.Errorf("write intermediate: %w", &net.OpError{
		Op:     "write",
		Net:    "tcp",
		Source: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 38246},
		Addr:   &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 10809},
		Err:    os.NewSyscallError("write", errno),
	})))
}

// TestIsDeadTransportClassification pins the typed matches: dead-carrier
// errnos through wrapped chains, EOF-shaped write deaths, and the errors
// that must keep their own paths — cancellations, timeouts, dial
// refusals and rpc answers of either severity.
func TestIsDeadTransportClassification(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		err  error
		dead bool
	}{
		"nil":                     {err: nil, dead: false},
		"epipe chain":             {err: proxyKillChain(syscall.EPIPE), dead: true},
		"reset chain":             {err: proxyKillChain(syscall.ECONNRESET), dead: true},
		"bare epipe":              {err: syscall.EPIPE, dead: true},
		"wrapped unexpected eof":  {err: fmt.Errorf("rpc get file at 0: %w", io.ErrUnexpectedEOF), dead: true},
		"bare eof":                {err: io.EOF, dead: true},
		"canceled":                {err: fmt.Errorf("chunk at 0: %w", context.Canceled), dead: false},
		"deadline":                {err: fmt.Errorf("pace before 1/2: %w", context.DeadlineExceeded), dead: false},
		"rpc client error":        {err: tgerr.New(400, "MESSAGE_ID_INVALID"), dead: false},
		"rpc server error":        {err: tgerr.New(500, "RPC_CALL_FAIL"), dead: false},
		"read timeout":            {err: &net.OpError{Op: "read", Net: "tcp", Err: context.DeadlineExceeded}, dead: false},
		"dial refused":            {err: os.NewSyscallError("connect", syscall.ECONNREFUSED), dead: false},
		"application error":       {err: errors.New("downloaded size mismatch"), dead: false},
		"short file sentinel":     {err: fmt.Errorf("short file: 1 of 2 bytes at 0: %w", errors.New("unexpected EOF")), dead: false},
		"empty broken only":       {err: errors.New("pipe"), dead: false},
		"unrelated reset wording": {err: errors.New("counter reset by policy"), dead: false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.dead, transport.IsDeadTransport(tc.err))
		})
	}
}

// TestIsDeadTransportStringFallback pins the portable fallback: carriers
// dying behind wraps that lose the error type still classify through the
// rendered chain, because Go renders these errnos identically on every
// platform (windows WSA codes included).
func TestIsDeadTransportStringFallback(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		err  error
		dead bool
	}{
		"broken pipe text": {
			err:  errors.New("send: write: write intermediate: write tcp 127.0.0.1:38246->127.0.0.1:10809: write: broken pipe"),
			dead: true,
		},
		"wrapped broken pipe text": {
			err:  fmt.Errorf("history page after 3 attempts: %w", errors.New("write: broken pipe")),
			dead: true,
		},
		"reset text": {
			err:  fmt.Errorf("read loop: %w", errors.New("read tcp [::1]:52->[::1]:1080: read: connection reset by peer")),
			dead: true,
		},
		"rpc text": {err: errors.New("rpc error code 400: PIPELINE_EMPTY"), dead: false},
		"dns text": {err: errors.New("dial tcp: lookup example.com: no such host"), dead: false},
		"timeout text": {
			err:  errors.New("dial tcp 127.0.0.1:10809: i/o timeout"),
			dead: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.dead, transport.IsDeadTransport(tc.err))
		})
	}
}
