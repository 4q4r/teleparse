package scan

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/4q4r/teleparse/internal/transport"

	"github.com/gotd/td/telegram/query/messages"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// Transient-server retry tuning: how many times a history page is retried
// when Telegram answers with a transient server error, and the backoff
// schedule between attempts.
const (
	walkRetryMax    = 3
	walkRetryBase   = time.Second
	walkRetryFactor = 3
)

// rpcServerCodeFloor marks rpc codes at or above which errors are server
// noise worth retrying.
const rpcServerCodeFloor = 500

// retryableWalkRPC reports whether a history-page error is transient
// server noise worth retrying: 5xx rpc codes, transport timeouts and
// dead carriers. Context cancellation and 4xx client errors never retry.
func retryableWalkRPC(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	var rpcErr *tgerr.Error
	if errors.As(err, &rpcErr) {
		return rpcErr.Code >= rpcServerCodeFloor
	}

	// Transport-level failures retry: timeouts and resets via the legacy
	// Temporary() interface, dead carriers (EPIPE/reset writes to a
	// socket the local proxy already closed — Temporary() answers false
	// for those errnos) via the shared dead-transport predicate, which
	// also carries the portable message fallback for type-less chains.
	return isTemporaryTransport(err) || transport.IsDeadTransport(err)
}

// walkRetrySleep pauses between retries with exponential backoff; held in
// an atomic pointer so parallel tests can swap in a no-op without tripping
// the race detector. Nil means the default backoff.
var walkRetrySleep atomic.Pointer[retrySleepFunc] //nolint:gochecknoglobals // test seam

// retrySleepFunc is the backoff signature of walkRetrySleep.
type retrySleepFunc func(ctx context.Context, attempt int) error

// defaultWalkRetrySleep backs off exponentially between history-page
// retries.
func defaultWalkRetrySleep(ctx context.Context, attempt int) error {
	delay := walkRetryBase * time.Duration(walkRetryFactor^attempt)

	select {
	case <-ctx.Done():
		return fmt.Errorf("backoff: %w", ctx.Err())
	case <-time.After(delay):
	}

	return nil
}

// sleepBetweenRetries runs the currently installed backoff function,
// falling back to the default when no override is installed.
func sleepBetweenRetries(ctx context.Context, attempt int) error {
	sleep := walkRetrySleep.Load()
	if sleep == nil {
		return defaultWalkRetrySleep(ctx, attempt)
	}

	return (*sleep)(ctx, attempt)
}

// retryingQuery wraps a messages.Query with transient-error retries:
// RPC_CALL_FAIL and friends kill hour-long walks otherwise. Retries
// re-issue the SAME paginated request (same offset), which is idempotent
// server-side.
type retryingQuery struct {
	inner messages.Query
}

//nolint:ireturn // implements the gotd messages.Query interface
func (q retryingQuery) Query(ctx context.Context, req messages.Request) (tg.MessagesMessagesClass, error) {
	var (
		result tg.MessagesMessagesClass
		err    error
	)

	for attempt := range walkRetryMax + 1 {
		result, err = q.inner.Query(ctx, req)
		if err == nil {
			return result, nil
		}

		if !retryableWalkRPC(err) || attempt == walkRetryMax {
			return nil, fmt.Errorf("history page after %d attempts: %w", attempt+1, err)
		}

		if err := sleepBetweenRetries(ctx, attempt); err != nil {
			return nil, fmt.Errorf("walk retry canceled: %w", err)
		}
	}

	if err != nil {
		return nil, fmt.Errorf("history page: %w", err)
	}

	return result, nil
}

// isTemporaryTransport reports timeout/reset-style transport errors.
func isTemporaryTransport(err error) bool {
	type temporary interface{ Temporary() bool }

	for err != nil {
		if temp, ok := err.(temporary); ok && temp.Temporary() {
			return true
		}

		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}

		err = unwrapper.Unwrap()
	}

	return false
}
