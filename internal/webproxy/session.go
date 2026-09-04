package webproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Multiplexer limits from PROTOCOL.md.
const (
	// tombstoneLimit is the number of recently closed stream ids retained so
	// late frames from close races are ignored instead of failing the
	// session.
	tombstoneLimit = 4096
	// writerQueueSize bounds queued uplink frames per writer.
	writerQueueSize = 1024
	// sessionDeleteTimeout bounds the best-effort session delete request.
	sessionDeleteTimeout = 10 * time.Second
)

// Session sentinels.
var (
	// ErrSessionLost reports a carrier session lost with no resume path.
	ErrSessionLost = errors.New("web proxy session lost")
	// ErrStreamClosed reports a stream closed locally or by the relay.
	ErrStreamClosed = errors.New("web proxy stream closed")
	// ErrStreamsExhausted reports that no fresh stream id remains.
	ErrStreamsExhausted = errors.New("web proxy stream ids exhausted")
	// ErrProtocolViolation reports an invalid frame from the relay.
	ErrProtocolViolation = errors.New("web proxy protocol violation")
)

// Session multiplexes logical streams over one carrier session. A session
// loss is terminal: the caller must run a full re-bootstrap.
type Session struct {
	cfg   Config
	mode  CarrierMode
	token string

	laneDial    func(ctx context.Context, streamID uint32) (Carrier, error)
	deleteRelay func(ctx context.Context) error

	mu         sync.Mutex
	streams    map[uint32]*Stream
	nextID     uint32
	tombstones map[uint32]struct{}
	tombOrder  []uint32
	lost       error

	stop     context.CancelFunc
	done     <-chan struct{}
	writer   *carrierWriter
	onceFail sync.Once
}

// NewSession creates a multiplexer over an established session-level
// carrier. The carrier is the multiplexed connection for websocket and https
// modes, the lane-zero poller for https-lanes, and nil for websocket-lanes
// where every stream dials its own lane.
func NewSession(cfg Config, mode CarrierMode, token string, carrier Carrier) *Session {
	ctx, cancel := context.WithCancel(context.Background())

	session := &Session{
		cfg:        cfg,
		mode:       mode,
		token:      token,
		streams:    make(map[uint32]*Stream),
		nextID:     1,
		tombstones: make(map[uint32]struct{}),
		stop:       cancel,
		done:       ctx.Done(),
		deleteRelay: func(ctx context.Context) error {
			return DeleteSession(ctx, cfg.httpClient(), cfg.baseURL(), cfg.Host, token)
		},
	}

	switch {
	case carrier != nil:
		session.writer = newCarrierWriter(carrier, session.done, session.fail)
		go session.writer.run(ctx)
		go runCarrierPump(carrier, session.handleFrame, session.fail)
	case mode == ModeWebSocketLanes || mode == ModeHTTPSLanes:
		session.laneDial = func(ctx context.Context, streamID uint32) (Carrier, error) {
			carrier, err := NewLaneCarrier(ctx, cfg, mode, token, streamID)

			return carrier, err
		}
	}

	return session
}

// Open allocates a fresh nonzero stream id and emits its OPEN frame.
func (s *Session) Open(ctx context.Context) (*Stream, error) {
	s.mu.Lock()

	if s.lost != nil {
		err := s.lost
		s.mu.Unlock()

		return nil, fmt.Errorf("open stream: %w", err)
	}

	if s.nextID > MaxStreamID {
		s.mu.Unlock()

		return nil, fmt.Errorf("open stream: %w", ErrStreamsExhausted)
	}

	id := s.nextID
	s.nextID++

	stream := newStream(s, id)
	s.streams[id] = stream
	s.mu.Unlock()

	if err := stream.start(ctx); err != nil {
		s.dropStream(id)

		return nil, err
	}

	if err := stream.send(Frame{Type: TypeOpen, StreamID: id}); err != nil {
		s.dropStream(id)

		return nil, err
	}

	return stream, nil
}

// Close tears the session and all streams down, deleting the relay session
// best effort.
func (s *Session) Close() error {
	s.fail(ErrSessionClosed)

	ctx, cancel := context.WithTimeout(context.Background(), sessionDeleteTimeout)
	defer cancel()

	if err := s.deleteRelay(ctx); err != nil {
		return fmt.Errorf("session close: %w", err)
	}

	return nil
}

// Mode returns the carrier mode the session runs on.
func (s *Session) Mode() CarrierMode {
	return s.mode
}

func (s *Session) handleFrame(frame Frame) error {
	if err := ValidateServer(frame); err != nil {
		return fmt.Errorf("relay frame: %w: %w", err, ErrProtocolViolation)
	}

	switch frame.Type {
	case TypeBye:
		return fmt.Errorf("relay BYE: %w", ErrSessionLost)
	case TypePing:
		pong := Frame{Type: TypePong, Payload: frame.Payload}
		if err := s.enqueueGlobal(pong); err != nil {
			return fmt.Errorf("reply PONG: %w", err)
		}

		return nil
	case TypeWelcome, TypePong, TypeHello, TypeOpen:
		return fmt.Errorf("unexpected frame %#02x: %w", frame.Type, ErrProtocolViolation)
	case TypeData, TypeWindow, TypeClose:
	}

	s.mu.Lock()
	stream, known := s.streams[frame.StreamID]
	tombstoned := s.isTombstoned(frame.StreamID)
	s.mu.Unlock()

	if tombstoned || !known {
		return nil
	}

	return stream.handleRelay(frame)
}

func (s *Session) enqueueGlobal(frame Frame) error {
	if s.writer == nil {
		return nil
	}

	return s.writer.enqueue(frame)
}

func (s *Session) isTombstoned(id uint32) bool {
	_, ok := s.tombstones[id]

	return ok
}

func (s *Session) tombstone(id uint32) {
	if _, ok := s.tombstones[id]; ok {
		return
	}

	if len(s.tombOrder) == tombstoneLimit {
		oldest := s.tombOrder[0]
		s.tombOrder = s.tombOrder[1:]
		delete(s.tombstones, oldest)
	}

	s.tombstones[id] = struct{}{}
	s.tombOrder = append(s.tombOrder, id)
}

func (s *Session) dropStream(id uint32) {
	s.mu.Lock()
	delete(s.streams, id)
	s.tombstone(id)
	s.mu.Unlock()
}

func (s *Session) fail(cause error) {
	s.onceFail.Do(func() {
		s.stop()

		s.mu.Lock()

		s.lost = cause
		streams := make([]*Stream, 0, len(s.streams))

		for _, stream := range s.streams {
			streams = append(streams, stream)
		}

		s.mu.Unlock()

		for _, stream := range streams {
			stream.abort(cause)
		}
	})
}

// carrierWriter serializes frame sends over one carrier, coalescing queued
// frames into protocol-bounded batches.
type carrierWriter struct {
	carrier Carrier
	queue   chan Frame
	done    <-chan struct{}
	onError func(error)
}

func newCarrierWriter(
	carrier Carrier,
	done <-chan struct{},
	onError func(error),
) *carrierWriter {
	return &carrierWriter{
		carrier: carrier,
		queue:   make(chan Frame, writerQueueSize),
		done:    done,
		onError: onError,
	}
}

func (w *carrierWriter) enqueue(frame Frame) error {
	select {
	case w.queue <- frame:
		return nil
	case <-w.done:
		return fmt.Errorf("enqueue %s: %w", frame.Type, ErrSessionLost)
	}
}

func (w *carrierWriter) run(ctx context.Context) {
	defer func() { _ = w.carrier.Close() }()

	for {
		var frame Frame

		select {
		case <-ctx.Done():
			return
		case frame = <-w.queue:
		}

		batch := Encode(frame)
		count := 1

	drain:
		for len(batch) < DefaultBatchBytes && count < MaxBatchFrames {
			select {
			case next := <-w.queue:
				batch = append(batch, Encode(next)...)
				count++
			default:
				break drain
			}
		}

		if err := w.carrier.Send(batch); err != nil {
			w.onError(fmt.Errorf("carrier send: %w: %w", err, ErrSessionLost))

			return
		}
	}
}

func runCarrierPump(
	carrier Carrier,
	handle func(Frame) error,
	fail func(error),
) {
	for {
		batch, err := carrier.Recv()
		if err != nil {
			fail(fmt.Errorf("carrier recv: %w: %w", err, ErrSessionLost))

			return
		}

		frames, err := DecodeAll(batch)
		if err != nil {
			fail(fmt.Errorf("decode batch: %w: %w", err, ErrProtocolViolation))

			return
		}

		for _, frame := range frames {
			if err := handle(frame); err != nil {
				fail(fmt.Errorf("%w: %w", ErrSessionLost, err))

				return
			}
		}
	}
}

// Stream is one logical MTProto connection multiplexed over the session.
type Stream struct {
	session *Session
	id      uint32
	sendWin *sendWindow
	recvWin *recvWindow

	mu       sync.Mutex
	cond     *sync.Cond
	buf      []byte
	recvErr  error
	closed   bool
	deadline time.Time

	laneMu   sync.Mutex
	lane     *carrierWriter
	laneStop context.CancelFunc
}

func newStream(session *Session, id uint32) *Stream {
	stream := &Stream{
		session: session,
		id:      id,
		sendWin: NewSendWindow(),
		recvWin: NewRecvWindow(),
	}
	stream.cond = sync.NewCond(&stream.mu)

	return stream
}

// ID returns the nonzero stream id.
func (st *Stream) ID() uint32 {
	return st.id
}

// Read drains relayed DATA payloads, granting WINDOW credit as the
// application consumes bytes.
func (st *Stream) Read(out []byte) (int, error) {
	st.mu.Lock()

	for len(st.buf) == 0 && st.recvErr == nil {
		if deadline := st.deadline; !deadline.IsZero() && !time.Now().Before(deadline) {
			st.mu.Unlock()

			return 0, os.ErrDeadlineExceeded
		}

		st.cond.Wait()
	}

	if len(st.buf) == 0 {
		err := st.recvErr
		st.mu.Unlock()

		return 0, err
	}

	size := min(len(out), len(st.buf))
	copy(out, st.buf[:size])
	st.buf = st.buf[size:]
	st.mu.Unlock()

	if delta := st.recvWin.Consume(size); delta > 0 {
		_ = st.send(Frame{
			Type:     TypeWindow,
			StreamID: st.id,
			Payload:  WindowPayload(delta),
		})
	}

	return size, nil
}

// Write splits data into protocol DATA chunks within the granted window.
func (st *Stream) Write(data []byte) (int, error) {
	total := 0

	for len(data) > 0 {
		if err := st.writeGate(); err != nil {
			return total, fmt.Errorf("write stream %d: %w", st.id, err)
		}

		chunk := DataChunkSize(len(data))

		got, err := st.sendWin.Reserve(context.Background(), chunk)
		if err != nil {
			return total, fmt.Errorf("write stream %d: %w", st.id, err)
		}

		if err := st.send(Frame{
			Type:     TypeData,
			StreamID: st.id,
			Payload:  data[:got],
		}); err != nil {
			return total, err
		}

		data = data[got:]
		total += got
	}

	return total, nil
}

// Close aborts the stream, emits its CLOSE frame, and tombstones the id.
func (st *Stream) Close() error {
	st.mu.Lock()

	if st.closed {
		st.mu.Unlock()

		return nil
	}

	st.closed = true

	if st.recvErr == nil {
		st.recvErr = ErrStreamClosed
	}

	st.cond.Broadcast()
	st.mu.Unlock()

	st.sendWin.Abort(fmt.Errorf("stream %d: %w", st.id, ErrStreamClosed))

	err := st.send(Frame{Type: TypeClose, StreamID: st.id})
	st.session.dropStream(st.id)
	st.stopLane()

	return err
}

// SetReadDeadline arms the read deadline; a zero value clears it.
func (st *Stream) SetReadDeadline(when time.Time) error {
	st.mu.Lock()

	st.deadline = when
	st.cond.Broadcast()
	st.mu.Unlock()

	return nil
}

// SetWriteDeadline is accepted for net.Conn compatibility; writes abort via
// stream closure rather than deadlines.
func (st *Stream) SetWriteDeadline(when time.Time) error {
	_ = when

	return nil
}

// SetDeadline arms the read deadline.
func (st *Stream) SetDeadline(when time.Time) error {
	return st.SetReadDeadline(when)
}

// start prepares the per-lane transport when the session runs in a lanes
// mode; lane establishment failure is a parent-carrier failure per protocol.
func (st *Stream) start(ctx context.Context) error {
	if st.session.mode != ModeWebSocketLanes && st.session.mode != ModeHTTPSLanes {
		return nil
	}

	if st.session.laneDial == nil {
		return fmt.Errorf("lane %d dial: %w", st.id, ErrCarrierClosed)
	}

	carrier, err := st.session.laneDial(ctx, st.id)
	if err != nil {
		cause := fmt.Errorf("lane %d dial: %w: %w", st.id, err, ErrSessionLost)
		st.session.fail(cause)

		return cause
	}

	laneCtx, cancel := context.WithCancel(context.Background())
	onLaneError := func(err error) { st.abort(fmt.Errorf("lane %d: %w", st.id, err)) }

	st.laneMu.Lock()
	st.lane = newCarrierWriter(carrier, st.session.done, onLaneError)
	st.laneStop = cancel
	writer := st.lane
	st.laneMu.Unlock()

	//nolint:contextcheck // lane lifetime is stream-scoped, not request-scoped
	go writer.run(laneCtx)
	go runCarrierPump(carrier, st.handleLaneFrame, st.session.fail)

	return nil
}

func (st *Stream) handleLaneFrame(frame Frame) error {
	if frame.StreamID != st.id {
		return fmt.Errorf("lane %d got stream %d: %w", st.id, frame.StreamID, ErrProtocolViolation)
	}

	return st.handleRelay(frame)
}

func (st *Stream) handleRelay(frame Frame) error {
	switch frame.Type {
	case TypeData:
		st.mu.Lock()
		st.buf = append(st.buf, frame.Payload...)
		st.cond.Broadcast()
		st.mu.Unlock()
	case TypeWindow:
		amount, err := WindowAmount(frame.Payload)
		if err != nil {
			return fmt.Errorf("stream %d: %w: %w", st.id, err, ErrProtocolViolation)
		}

		st.sendWin.Grant(amount)
	case TypeClose:
		st.mu.Lock()

		if st.recvErr == nil {
			st.recvErr = io.EOF
		}

		st.cond.Broadcast()
		st.mu.Unlock()

		st.sendWin.Abort(fmt.Errorf("stream %d: %w", st.id, ErrStreamClosed))

		st.session.dropStream(st.id)
		st.stopLane()
	default:
	}

	return nil
}

func (st *Stream) send(frame Frame) error {
	if err := ValidateClient(frame); err != nil {
		return fmt.Errorf("send %s: %w", frame.Type, err)
	}

	st.laneMu.Lock()
	lane := st.lane
	st.laneMu.Unlock()

	if lane != nil {
		return lane.enqueue(frame)
	}

	return st.session.enqueueGlobal(frame)
}

func (st *Stream) abort(cause error) {
	st.mu.Lock()

	if st.recvErr == nil {
		st.recvErr = fmt.Errorf("stream %d: %w", st.id, cause)
	}

	st.cond.Broadcast()
	st.mu.Unlock()

	st.sendWin.Abort(fmt.Errorf("stream %d: %w", st.id, cause))

	st.stopLane()
}

func (st *Stream) stopLane() {
	st.laneMu.Lock()
	defer st.laneMu.Unlock()

	if st.laneStop != nil {
		st.laneStop()
	}
}

// writeGate rejects writes once the stream's terminal state is observable.
// It reads the same mutex-guarded state Read reports, so the close transition
// is atomic across both directions: once a reader can observe EOF or a
// terminal error, no strictly later Write can slip between that publication
// and the send-window abort.
func (st *Stream) writeGate() error {
	st.mu.Lock()
	defer st.mu.Unlock()

	switch {
	case st.closed || errors.Is(st.recvErr, io.EOF):
		return ErrStreamClosed
	case st.recvErr != nil:
		return st.recvErr
	default:
		return nil
	}
}
