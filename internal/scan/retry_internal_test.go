package scan

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"

	"github.com/gotd/td/telegram/query/messages"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withNoopRetrySleep swaps the retry backoff for a no-op during the test;
// the swap is atomic, so parallel tests never trip the race detector.
func withNoopRetrySleep(t *testing.T) {
	t.Helper()

	noop := retrySleepFunc(func(context.Context, int) error { return nil })
	walkRetrySleep.Store(&noop)
	t.Cleanup(func() { walkRetrySleep.Store(nil) })
}

func TestRetryableWalkRPC(t *testing.T) {
	t.Parallel()

	assert.True(t, retryableWalkRPC(tgerr.New(500, "RPC_CALL_FAIL")))
	assert.True(t, retryableWalkRPC(tgerr.New(500, "INTERNAL")))
	assert.True(t, retryableWalkRPC(tgerr.New(520, "UNKNOWN")))
	assert.False(t, retryableWalkRPC(tgerr.New(400, "CHANNEL_PRIVATE")))
	assert.False(t, retryableWalkRPC(tgerr.New(420, "FLOOD_WAIT_30")),
		"flood waits have their own pacing path")
	assert.False(t, retryableWalkRPC(nil))
	assert.False(t, retryableWalkRPC(context.Canceled))
	assert.False(t, retryableWalkRPC(errors.Join(context.Canceled)))
}

// proxyKilledConn reproduces the production walk failure behind a local
// HTTP CONNECT proxy: gotd's send/write/write-intermediate wraps over a
// *net.OpError carrying the errno of a write to a socket the proxy
// already closed. The legacy Temporary() interface answers false for
// EPIPE and ECONNRESET, so these must classify through the shared
// dead-transport predicate instead.
func proxyKilledConn(errno syscall.Errno) error {
	return fmt.Errorf("send: %w", fmt.Errorf("write: %w", &net.OpError{
		Op:  "write",
		Net: "tcp",
		Err: os.NewSyscallError("write", errno),
	}))
}

// TestRetryableWalkRPCDeadTransport pins that carrier deaths retry the
// history page: the same idempotent paginated request re-issues over the
// redialed proxied connection.
func TestRetryableWalkRPCDeadTransport(t *testing.T) {
	t.Parallel()

	assert.True(t, retryableWalkRPC(proxyKilledConn(syscall.EPIPE)),
		"a broken-pipe write to the dead proxy socket must retry")
	assert.True(t, retryableWalkRPC(proxyKilledConn(syscall.ECONNRESET)),
		"a reset carrier must retry")
	assert.True(t, retryableWalkRPC(errors.New("write: broken pipe")),
		"the type-less chain must retry through the message fallback")
}

func TestRetryingQueryRecoversTransientErrors(t *testing.T) {
	t.Parallel()
	withNoopRetrySleep(t)

	inner := &scriptedQuery{
		failures: []error{
			tgerr.New(500, "RPC_CALL_FAIL"),
			tgerr.New(500, "RPC_CALL_FAIL"),
		},
	}

	got, err := retryingQuery{inner: inner}.Query(context.Background(), messages.Request{})
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 3, inner.calls, "two failures then success")
}

func TestRetryingQueryGivesUpAfterMaxAttempts(t *testing.T) {
	t.Parallel()
	withNoopRetrySleep(t)

	inner := &scriptedQuery{always: tgerr.New(500, "RPC_CALL_FAIL")}
	wrapped := retryingQuery{inner: inner}

	_, err := wrapped.Query(context.Background(), messages.Request{})
	require.Error(t, err)
	assert.Equal(t, 4, inner.calls, "initial attempt plus three retries")

	var rpcErr *tgerr.Error
	require.ErrorAs(t, err, &rpcErr)
	assert.Equal(t, "RPC_CALL_FAIL", rpcErr.Type)
}

func TestRetryingQueryDoesNotRetryClientErrors(t *testing.T) {
	t.Parallel()
	withNoopRetrySleep(t)

	inner := &scriptedQuery{always: tgerr.New(400, "PEER_ID_INVALID")}
	wrapped := retryingQuery{inner: inner}

	_, err := wrapped.Query(context.Background(), messages.Request{})
	require.Error(t, err)
	assert.Equal(t, 1, inner.calls, "client errors fail immediately")
}

func TestRetryingQueryHonorsCanceledContext(t *testing.T) {
	t.Parallel()
	withNoopRetrySleep(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	inner := &scriptedQuery{always: tgerr.New(500, "RPC_CALL_FAIL")}
	wrapped := retryingQuery{inner: inner}

	_, err := wrapped.Query(ctx, messages.Request{})
	require.Error(t, err)
}

type scriptedQuery struct {
	failures []error // consumed before the success result
	always   error   // when set, every call fails
	calls    int
}

//nolint:ireturn // implements the gotd messages.Query interface
func (q *scriptedQuery) Query(
	context.Context, messages.Request,
) (tg.MessagesMessagesClass, error) {
	q.calls++

	if q.always != nil {
		return nil, q.always
	}

	if q.calls <= len(q.failures) {
		return nil, q.failures[q.calls-1]
	}

	return &tg.MessagesMessages{}, nil
}
