// Package transport classifies network-carrier failures for retry
// decisions: an error whose carrier connection died — a local proxy
// killing long-lived tunnels, a NAT reset, a truncated stream — means
// the next attempt needs a fresh connection, never a new item verdict.
package transport

import (
	"errors"
	"io"
	"strings"
	"syscall"
)

// IsDeadTransport reports whether err means the network carrier died:
// the write side of a socket the peer already closed (EPIPE, "broken
// pipe"), a reset (ECONNRESET, "connection reset by peer"), or an
// EOF-shaped death on the write path. Wrapped chains are followed.
//
// Deliberately NOT dead: cancellations and deadlines (they keep the
// existing interrupt paths), timeouts, dial refusals and rpc answers —
// callers classify those on their own paths.
func IsDeadTransport(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	// Exact errno identity through the wrap chain. The syscall
	// constants exist on every supported platform, but their runtime
	// values need not match the carrier's errno there (windows maps
	// unix names onto its own WSA codes), hence the message fallback.
	if errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}

	return deadTransportMessage(err.Error())
}

// deadTransportMessage scans the rendered chain for the carrier-death
// phrases as the portable fallback: Go renders these errnos with
// identical text on every supported platform (windows WSA codes
// included), so a wrap boundary that loses the error type still
// classifies. Matching is lowercase-phrase exact to stay narrow;
// errors compose their full chain into Error(), so one containment
// check per phrase covers every wrap level.
func deadTransportMessage(text string) bool {
	return strings.Contains(text, "broken pipe") || strings.Contains(text, "connection reset by peer")
}
