package webproxy_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"teleparse/internal/webproxy"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeCarrier is an in-memory Carrier: uplink batches are recorded, downlink
// batches arrive via Push.
type fakeCarrier struct {
	mu     sync.Mutex
	sent   [][]byte
	signal chan struct{}
	down   chan []byte
	done   chan struct{}
	once   sync.Once
}

func newFakeCarrier() *fakeCarrier {
	return &fakeCarrier{
		signal: make(chan struct{}, 1),
		down:   make(chan []byte, 64),
		done:   make(chan struct{}),
	}
}

func (f *fakeCarrier) Send(batch []byte) error {
	f.mu.Lock()
	f.sent = append(f.sent, append([]byte(nil), batch...))
	f.mu.Unlock()

	select {
	case f.signal <- struct{}{}:
	default:
	}

	return nil
}

func (f *fakeCarrier) Recv() ([]byte, error) {
	select {
	case batch := <-f.down:
		return batch, nil
	case <-f.done:
		return nil, fmt.Errorf("fake carrier closed: %w", webproxy.ErrCarrierClosed)
	}
}

func (f *fakeCarrier) Close() error {
	f.once.Do(func() { close(f.done) })

	return nil
}

func (f *fakeCarrier) push(batch []byte) {
	select {
	case f.down <- batch:
	case <-f.done:
	}
}

func (f *fakeCarrier) frames(t *testing.T) []webproxy.Frame {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	var frames []webproxy.Frame

	for _, batch := range f.sent {
		decoded, err := webproxy.DecodeAll(batch)
		require.NoError(t, err)
		frames = append(frames, decoded...)
	}

	return frames
}

// waitFrames polls recorded uplink frames until want returns true for some
// prefix frame.
func (f *fakeCarrier) waitFrames(t *testing.T, want func(webproxy.Frame) bool) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, frame := range f.frames(t) {
			if want(frame) {
				return
			}
		}

		time.Sleep(2 * time.Millisecond)
	}

	t.Fatalf("expected frame not seen in uplink; got %+v", f.frames(t))
}

func pushFrames(t *testing.T, carrier *fakeCarrier, frames ...webproxy.Frame) {
	t.Helper()

	batch := make([]byte, 0, len(frames)*webproxy.HeaderSize)
	for _, frame := range frames {
		batch = append(batch, webproxy.Encode(frame)...)
	}

	carrier.push(batch)
}

func newTestSession(t *testing.T, carrier *fakeCarrier) *webproxy.Session {
	t.Helper()

	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(relay.Close)

	session := webproxy.NewSession(wsTestConfig(relay), webproxy.ModeWebSocket, "token-abc", carrier)
	t.Cleanup(func() { _ = session.Close() })

	return session
}

func TestSessionOpenWritesOPENFrame(t *testing.T) {
	t.Parallel()

	carrier := newFakeCarrier()
	session := newTestSession(t, carrier)

	stream, err := session.Open(context.Background())
	require.NoError(t, err)
	require.Equal(t, uint32(1), stream.ID())

	carrier.waitFrames(t, func(frame webproxy.Frame) bool {
		return frame.Type == webproxy.TypeOpen && frame.StreamID == 1
	})
}

func TestSessionStreamIDsIncrement(t *testing.T) {
	t.Parallel()

	carrier := newFakeCarrier()
	session := newTestSession(t, carrier)

	for want := uint32(1); want <= 3; want++ {
		stream, err := session.Open(context.Background())
		require.NoError(t, err)
		require.Equal(t, want, stream.ID())
	}
}

func TestSessionWriteChunksTo64KiB(t *testing.T) {
	t.Parallel()

	carrier := newFakeCarrier()
	session := newTestSession(t, carrier)

	stream, err := session.Open(context.Background())
	require.NoError(t, err)

	payload := bytes.Repeat([]byte{0xAB}, 200*1024)
	written, err := stream.Write(payload)
	require.NoError(t, err)
	require.Equal(t, len(payload), written)

	carrier.waitFrames(t, func(frame webproxy.Frame) bool {
		return frame.Type == webproxy.TypeData && frame.StreamID == 1 &&
			len(frame.Payload) == 8192
	})

	sizes := make([]int, 0, 4)
	var received []byte

	for _, frame := range carrier.frames(t) {
		if frame.Type == webproxy.TypeData && frame.StreamID == 1 {
			sizes = append(sizes, len(frame.Payload))
			received = append(received, frame.Payload...)
		}
	}
	require.Equal(t, []int{webproxy.DataChunk, webproxy.DataChunk, webproxy.DataChunk, 8192}, sizes)
	require.Equal(t, payload, received)
}

func TestSessionReadDeliversDownlinkData(t *testing.T) {
	t.Parallel()

	carrier := newFakeCarrier()
	session := newTestSession(t, carrier)

	stream, err := session.Open(context.Background())
	require.NoError(t, err)

	pushFrames(t, carrier, webproxy.Frame{
		Type: webproxy.TypeData, StreamID: 1, Payload: []byte("hello relay"),
	})

	buf := make([]byte, 32)
	n, err := stream.Read(buf)
	require.NoError(t, err)
	require.Equal(t, "hello relay", string(buf[:n]))
}

func TestSessionGrantsWindowAfterHalfConsumed(t *testing.T) {
	t.Parallel()

	carrier := newFakeCarrier()
	session := newTestSession(t, carrier)

	stream, err := session.Open(context.Background())
	require.NoError(t, err)

	half := webproxy.InitialWindow / 2
	bigFrames := make([]webproxy.Frame, 0, half/webproxy.DataChunk)
	for offset := 0; offset < half; offset += webproxy.DataChunk {
		size := min(webproxy.DataChunk, half-offset)
		bigFrames = append(bigFrames, webproxy.Frame{
			Type: webproxy.TypeData, StreamID: 1, Payload: bytes.Repeat([]byte{1}, size),
		})
	}
	pushFrames(t, carrier, bigFrames...)

	read := make([]byte, 32*1024)
	total := 0

	for total < half {
		n, err := stream.Read(read)
		require.NoError(t, err)
		total += n
	}
	require.Equal(t, half, total)

	carrier.waitFrames(t, func(frame webproxy.Frame) bool {
		amount, err := webproxy.WindowAmount(frame.Payload)

		return err == nil && frame.Type == webproxy.TypeWindow &&
			frame.StreamID == 1 && amount == uint32(half)
	})
}

func TestSessionRemoteCloseDrainsThenEOF(t *testing.T) {
	t.Parallel()

	carrier := newFakeCarrier()
	session := newTestSession(t, carrier)

	stream, err := session.Open(context.Background())
	require.NoError(t, err)

	pushFrames(t, carrier,
		webproxy.Frame{Type: webproxy.TypeData, StreamID: 1, Payload: []byte("tail")},
		webproxy.Frame{Type: webproxy.TypeClose, StreamID: 1},
	)

	buf := make([]byte, 64)
	n, err := stream.Read(buf)
	require.NoError(t, err)
	require.Equal(t, "tail", string(buf[:n]))

	_, err = stream.Read(buf)
	require.ErrorIs(t, err, io.EOF)

	_, err = stream.Write([]byte("nope"))
	require.Error(t, err)
}

func TestSessionLocalCloseSendsCLOSEAndTombstones(t *testing.T) {
	t.Parallel()

	carrier := newFakeCarrier()
	session := newTestSession(t, carrier)

	stream, err := session.Open(context.Background())
	require.NoError(t, err)

	require.NoError(t, stream.Close())

	carrier.waitFrames(t, func(frame webproxy.Frame) bool {
		return frame.Type == webproxy.TypeClose && frame.StreamID == 1
	})

	pushFrames(t, carrier,
		webproxy.Frame{Type: webproxy.TypeData, StreamID: 1, Payload: []byte("late")},
		webproxy.Frame{Type: webproxy.TypeClose, StreamID: 1},
	)

	buf := make([]byte, 8)
	_, err = stream.Read(buf)
	require.ErrorIs(t, err, webproxy.ErrStreamClosed)

	another, err := session.Open(context.Background())
	require.NoError(t, err)
	require.Equal(t, uint32(2), another.ID())
}

func TestSessionPingRepliesPong(t *testing.T) {
	t.Parallel()

	carrier := newFakeCarrier()
	session := newTestSession(t, carrier)

	_, err := session.Open(context.Background())
	require.NoError(t, err)

	pushFrames(t, carrier, webproxy.Frame{
		Type: webproxy.TypePing, Payload: []byte("echo-token"),
	})

	carrier.waitFrames(t, func(frame webproxy.Frame) bool {
		return frame.Type == webproxy.TypePong && string(frame.Payload) == "echo-token"
	})
}

func TestSessionByeFailsStreams(t *testing.T) {
	t.Parallel()

	carrier := newFakeCarrier()
	session := newTestSession(t, carrier)

	stream, err := session.Open(context.Background())
	require.NoError(t, err)

	pushFrames(t, carrier, webproxy.Frame{Type: webproxy.TypeBye})

	buf := make([]byte, 8)
	_, err = stream.Read(buf)
	require.ErrorIs(t, err, webproxy.ErrSessionLost)

	_, err = session.Open(context.Background())
	require.ErrorIs(t, err, webproxy.ErrSessionLost)
}

func TestSessionCarrierLossFailsStreams(t *testing.T) {
	t.Parallel()

	carrier := newFakeCarrier()
	session := newTestSession(t, carrier)

	stream, err := session.Open(context.Background())
	require.NoError(t, err)

	require.NoError(t, carrier.Close())

	buf := make([]byte, 8)
	_, err = stream.Read(buf)
	require.ErrorIs(t, err, webproxy.ErrSessionLost)
}

func TestSessionProtocolViolationFailsSession(t *testing.T) {
	t.Parallel()

	carrier := newFakeCarrier()
	session := newTestSession(t, carrier)

	stream, err := session.Open(context.Background())
	require.NoError(t, err)

	pushFrames(t, carrier, webproxy.Frame{Type: webproxy.TypeOpen, StreamID: 9})

	buf := make([]byte, 8)
	_, err = stream.Read(buf)
	require.ErrorIs(t, err, webproxy.ErrSessionLost)
}

func TestSessionWriteBlocksOnWindowExhaustion(t *testing.T) {
	t.Parallel()

	carrier := newFakeCarrier()
	session := newTestSession(t, carrier)

	stream, err := session.Open(context.Background())
	require.NoError(t, err)

	payload := bytes.Repeat([]byte{0xCD}, webproxy.InitialWindow)
	written, err := stream.Write(payload)
	require.NoError(t, err)
	require.Equal(t, webproxy.InitialWindow, written)

	done := make(chan error, 1)
	go func() {
		_, err := stream.Write(bytes.Repeat([]byte{0xCE}, webproxy.DataChunk))
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("write completed without credit: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	pushFrames(t, carrier, webproxy.Frame{
		Type: webproxy.TypeWindow, StreamID: 1, Payload: webproxy.WindowPayload(webproxy.DataChunk),
	})

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("write did not complete after window grant")
	}
}

func TestSessionCloseFailsStreams(t *testing.T) {
	t.Parallel()

	carrier := newFakeCarrier()
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(relay.Close)

	session := webproxy.NewSession(wsTestConfig(relay), webproxy.ModeWebSocket, "token-abc", carrier)

	stream, err := session.Open(context.Background())
	require.NoError(t, err)

	require.NoError(t, session.Close())

	buf := make([]byte, 8)
	_, err = stream.Read(buf)
	require.ErrorIs(t, err, webproxy.ErrSessionClosed)

	_, err = session.Open(context.Background())
	require.ErrorIs(t, err, webproxy.ErrSessionClosed)
}

func TestSessionUnknownStreamFramesIgnored(t *testing.T) {
	t.Parallel()

	carrier := newFakeCarrier()
	session := newTestSession(t, carrier)

	stream, err := session.Open(context.Background())
	require.NoError(t, err)

	pushFrames(t, carrier,
		webproxy.Frame{Type: webproxy.TypeData, StreamID: 42, Payload: []byte("stray")},
		webproxy.Frame{
			Type: webproxy.TypeWindow, StreamID: 42, Payload: webproxy.WindowPayload(16),
		},
		webproxy.Frame{Type: webproxy.TypeClose, StreamID: 42},
	)

	pushFrames(t, carrier, webproxy.Frame{
		Type: webproxy.TypeData, StreamID: 1, Payload: []byte("mine"),
	})

	buf := make([]byte, 16)
	n, err := stream.Read(buf)
	require.NoError(t, err)
	require.Equal(t, "mine", string(buf[:n]))
}

func TestSessionLostErrorIsDistinct(t *testing.T) {
	t.Parallel()

	require.NotErrorIs(t, webproxy.ErrSessionLost, webproxy.ErrStreamClosed)
	require.NotErrorIs(t, webproxy.ErrStreamClosed, webproxy.ErrSessionLost)
}
