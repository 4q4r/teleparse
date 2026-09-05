package scan

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/telegram/query/messages"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withNoopRetrySleep swaps the retry backoff for a no-op during the test.
func withNoopRetrySleep(t *testing.T) {
	t.Helper()

	original := walkRetrySleep
	walkRetrySleep = func(context.Context, int) error { return nil }
	t.Cleanup(func() { walkRetrySleep = original })
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
