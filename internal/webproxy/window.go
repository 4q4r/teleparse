package webproxy

import (
	"context"
	"fmt"
	"math"
	"sync"
)

// Flow-control limits from PROTOCOL.md.
const (
	// InitialWindow is the per-stream credit in each direction.
	InitialWindow = 4 * 1024 * 1024
	// DataChunk is the relay's maximum DATA chunk size.
	DataChunk = 64 * 1024
	// windowRefillThreshold is the consumed byte count that triggers a
	// receive-side WINDOW grant.
	windowRefillThreshold = InitialWindow / 2
)

// sendWindow tracks the relay-granted credit available for client-to-relay
// DATA frames on one stream. Reserve blocks while credit is zero.
type sendWindow struct {
	mu        sync.Mutex
	available uint32
	aborted   error
	changed   chan struct{}
}

// NewSendWindow returns a window preloaded with the protocol initial credit.
func NewSendWindow() *sendWindow {
	return &sendWindow{available: InitialWindow, changed: make(chan struct{})}
}

// Reserve blocks until at least one credit is available and returns a chunk of
// at most want credits, never more than currently available.
func (w *sendWindow) Reserve(ctx context.Context, want int) (int, error) {
	if want <= 0 {
		return 0, fmt.Errorf("want %d: %w", want, ErrWindowAmount)
	}

	for {
		w.mu.Lock()
		if w.aborted != nil {
			err := w.aborted
			w.mu.Unlock()

			return 0, err
		}

		if w.available > 0 {
			got := min(want, int(w.available))
			w.available -= uint32(got) //nolint:gosec // got is bounded by available
			w.mu.Unlock()

			return got, nil
		}

		changed := w.changed
		w.mu.Unlock()

		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("reserve credit: %w", ctx.Err())
		case <-changed:
		}
	}
}

// Grant adds credit and wakes a blocked Reserve, saturating at the u32
// maximum instead of overflowing.
func (w *sendWindow) Grant(delta uint32) {
	if delta == 0 {
		return
	}

	w.mu.Lock()

	next := uint64(w.available) + uint64(delta)
	if next > math.MaxUint32 {
		next = math.MaxUint32
	}

	w.available = uint32(next)
	close(w.changed)
	w.changed = make(chan struct{})
	w.mu.Unlock()
}

// Abort permanently fails the window with err and unblocks pending Reserves.
// It is idempotent; the first error wins.
func (w *sendWindow) Abort(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.aborted != nil {
		return
	}

	w.aborted = err
	close(w.changed)
	w.changed = make(chan struct{})
}

// Available returns the current credit for inspection.
func (w *sendWindow) Available() uint32 {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.available
}

// recvWindow accumulates relay DATA bytes consumed by the application and
// reports the pending WINDOW grant once the refill threshold is crossed.
type recvWindow struct {
	mu        sync.Mutex
	ungranted int
}

// NewRecvWindow returns an empty receive-side credit account.
func NewRecvWindow() *recvWindow {
	return &recvWindow{}
}

// Consume records that the application drained n payload bytes and returns
// the WINDOW delta to grant back, or zero below the refill threshold.
// Non-positive n is ignored.
func (w *recvWindow) Consume(n int) uint32 {
	if n <= 0 {
		return 0
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	w.ungranted += n
	if w.ungranted < windowRefillThreshold {
		return 0
	}

	delta := uint32(w.ungranted) //nolint:gosec // ungranted is bounded by the window size
	w.ungranted = 0

	return delta
}

// DataChunkSize caps one outgoing DATA payload at the relay chunk size.
func DataChunkSize(remaining int) int {
	return min(remaining, DataChunk)
}
