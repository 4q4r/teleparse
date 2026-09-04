package webproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// idleCarrier parks the downlink pump so the session stays healthy while a
// test drives stream state transitions directly.
type idleCarrier struct {
	once sync.Once
	done chan struct{}
}

func newIdleCarrier() *idleCarrier {
	return &idleCarrier{done: make(chan struct{})}
}

func (c *idleCarrier) Send(_ []byte) error {
	return nil
}

func (c *idleCarrier) Recv() ([]byte, error) {
	<-c.done

	return nil, fmt.Errorf("idle carrier closed: %w", ErrCarrierClosed)
}

func (c *idleCarrier) Close() error {
	c.once.Do(func() { close(c.done) })

	return nil
}

// TestStreamWriteRejectedOnceEOFIsObservable pins the remote-close contract
// deterministically: the TypeClose critical section publishes recvErr (EOF)
// before it aborts the send window, so a strictly sequential caller that
// observed EOF must already have its Write rejected. The write gate reads
// the same mutex-guarded terminal state the reader observed.
func TestStreamWriteRejectedOnceEOFIsObservable(t *testing.T) {
	t.Parallel()

	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(relay.Close)

	carrier := newIdleCarrier()
	session := NewSession(Config{
		BaseURL: relay.URL,
		Host:    "proxy.example.com",
		Secret:  []byte{0x01},
	}, ModeWebSocket, "token-abc", carrier)
	t.Cleanup(func() { _ = session.Close() })

	stream, err := session.Open(context.Background())
	require.NoError(t, err)

	// Mirror handleRelay(TypeClose)'s critical section exactly: EOF becomes
	// observable while the send-window abort has not run yet.
	stream.mu.Lock()
	stream.recvErr = io.EOF
	stream.cond.Broadcast()
	stream.mu.Unlock()

	_, err = stream.Write([]byte("nope"))
	require.ErrorIs(t, err, ErrStreamClosed)
}
