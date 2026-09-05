package cli

import (
	"errors"
	"fmt"

	"github.com/gotd/td/tgerr"
)

// ErrTakeoutInitUnavailable reports a takeout session that Telegram refused
// to start (delay window or similar); wrapped with the original rpc error.
var ErrTakeoutInitUnavailable = errors.New(
	"takeout session unavailable: Telegram allows one takeout session per 24h - " +
		"rerun with --no-takeout or wait for the delay to pass")

// Delay math constants for the takeout-init notice.
const (
	secondsPerHour   = 3600
	secondsPerMinute = 60
)

// isTakeoutInitFailure reports whether err stems from the takeout session
// INITIALIZATION (TAKEOUT_INIT_DELAY and friends) rather than the walk.
func isTakeoutInitFailure(err error) bool {
	if err == nil {
		return false
	}

	if rpcErr, ok := tgerr.AsType(err, "TAKEOUT_INIT_DELAY"); ok && rpcErr != nil {
		return true
	}

	_, tooMany := tgerr.AsType(err, "TAKEOUT_SESSION_TOO_MANY")

	return tooMany
}

// takeoutFallback decides whether a failed takeout run should be retried
// without takeout: only auto-engaged sessions fall back (the user never
// asked for takeout explicitly); a forced session surfaces an actionable
// error instead. Returns the retry verdict and the notice line to print.
func takeoutFallback(autoEngaged bool, reason string, runErr error) (bool, string) {
	if runErr == nil || !isTakeoutInitFailure(runErr) {
		return false, ""
	}

	if !autoEngaged {
		return false, ""
	}

	delay := takeoutInitDelay(runErr)

	return true, fmt.Sprintf("takeout: unavailable (init delay %s) - continuing without export mode (%s)",
		delay, reason)
}

// takeoutInitDelay extracts the documented delay argument from the rpc
// error, defaulting to 24h when absent.
func takeoutInitDelay(err error) string {
	var rpcErr *tgerr.Error

	if errors.As(err, &rpcErr) && rpcErr.Argument > 0 {
		return fmt.Sprintf("%dh%02dm", rpcErr.Argument/secondsPerHour, rpcErr.Argument%secondsPerHour/secondsPerMinute)
	}

	return "24h00m"
}
