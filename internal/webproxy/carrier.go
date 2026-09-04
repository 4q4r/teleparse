package webproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Carrier wire constants from PROTOCOL.md and the reference relay.
const (
	// wsPath is the WebSocket carrier endpoint.
	wsPath = "/api/v1/ws"
	// upPath is the HTTPS uplink endpoint.
	upPath = "/api/v1/up"
	// downPath is the HTTPS downlink long-poll endpoint.
	downPath = "/api/v1/down"
	// wsProtocolPrefix is the multiplexed WebSocket subprotocol prefix.
	wsProtocolPrefix = "tproxy-v1."
	// wsLaneProtocolPrefix is the per-lane WebSocket subprotocol prefix.
	wsLaneProtocolPrefix = "tproxy-lane-v1."
	// octetStream is the mandatory binary body media type.
	octetStream = "application/octet-stream"
	// DefaultBatchBytes is the protocol default carrier body target.
	DefaultBatchBytes = 2 * 1024 * 1024
	// carrierIOTimeout bounds one carrier write or read.
	carrierIOTimeout = 30 * time.Second
	// carrierRequestTimeout bounds one HTTPS carrier request.
	carrierRequestTimeout = 90 * time.Second
	// emptyPollPause throttles re-polling after an immediate empty poll.
	emptyPollPause = 100 * time.Millisecond
	// deliveryBuffer bounds queued downlink batches per carrier.
	deliveryBuffer = 16
)

// Carrier header names.
const (
	hdrUpSeq      = "X-Up-Seq"
	hdrUpAck      = "X-Up-Ack"
	hdrDownCursor = "X-Down-Cursor"
	hdrLaneID     = "X-Lane-Id"
	hdrLaneClosed = "X-Lane-Closed"
)

// Carrier sentinels.
var (
	// ErrUnavailable reports an exhausted 503 retry budget.
	ErrUnavailable = errors.New("carrier temporarily unavailable")
	// ErrCarrierClosed reports a carrier torn down or never established.
	ErrCarrierClosed = errors.New("carrier closed")
	// ErrLaneClosed reports a fully drained and closed HTTPS lane.
	ErrLaneClosed = errors.New("lane closed")
	// ErrSessionClosed reports an explicitly closed session.
	ErrSessionClosed = errors.New("session closed")
)

// CarrierMode selects or observes the relay carrier transport.
type CarrierMode string

// Carrier modes as offered by the relay plus the client-side auto preference.
const (
	// ModeAuto accepts whatever mode the relay selects.
	ModeAuto CarrierMode = "auto"
	// ModeWebSocket is the multiplexed WebSocket carrier.
	ModeWebSocket CarrierMode = "websocket"
	// ModeWebSocketLanes is the stream-aware WebSocket lane carrier.
	ModeWebSocketLanes CarrierMode = "websocket-lanes"
	// ModeHTTPS is the serialized HTTPS carrier.
	ModeHTTPS CarrierMode = "https"
	// ModeHTTPSLanes is the stream-aware HTTPS lane carrier.
	ModeHTTPSLanes CarrierMode = "https-lanes"
)

// Offered reports whether the mode is a relay-offered transport.
func (m CarrierMode) Offered() bool {
	switch m {
	case ModeWebSocket, ModeWebSocketLanes, ModeHTTPS, ModeHTTPSLanes:
		return true
	default:
		return false
	}
}

// String returns the wire representation of the mode.
func (m CarrierMode) String() string {
	return string(m)
}

// Config configures a WEB-proxy client session.
type Config struct {
	// Host is the canonical lowercase ASCII or IDNA hostname.
	Host string
	// Secret is the decoded MTProxy secret, dd or ee prefix retained.
	Secret []byte
	// CarrierMode is the preferred carrier; ModeAuto by default.
	CarrierMode CarrierMode
	// BaseURL overrides the https://Host origin derivation when set.
	BaseURL string
	// Client performs carrier HTTP requests and WebSocket upgrades.
	Client *http.Client
}

func (c Config) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}

	return "https://" + c.Host
}

func (c Config) httpClient() *http.Client {
	if c.Client != nil {
		return c.Client
	}

	return &http.Client{}
}

// Carrier is one ordered bidirectional transport of complete frame batches.
type Carrier interface {
	// Send blocks until one complete uplink batch is delivered.
	Send(batch []byte) error
	// Recv blocks until one complete downlink batch arrives.
	Recv() ([]byte, error)
	// Close tears the carrier down and releases its resources.
	Close() error
}

// NewCarrier establishes the session-level carrier for the offered mode.
//
//nolint:ireturn // carrier polymorphism is the transport seam
func NewCarrier(ctx context.Context, cfg Config, mode CarrierMode, token string) (Carrier, error) {
	switch mode {
	case ModeWebSocket:
		return dialWebSocket(ctx, cfg, token, 0)
	case ModeHTTPS, ModeHTTPSLanes:
		return newHTTPSCarrier(ctx, cfg, token, 0), nil
	default:
		return nil, fmt.Errorf("mode %q: %w", mode, ErrCarrierClosed)
	}
}

// NewLaneCarrier establishes the per-stream lane carrier for a nonzero
// stream id in a lanes mode.
//
//nolint:ireturn // carrier polymorphism is the transport seam
func NewLaneCarrier(
	ctx context.Context,
	cfg Config,
	mode CarrierMode,
	token string,
	streamID uint32,
) (Carrier, error) {
	if streamID == 0 || streamID > MaxStreamID {
		return nil, fmt.Errorf("lane stream id %d: %w", streamID, ErrCarrierClosed)
	}

	switch mode {
	case ModeWebSocketLanes:
		return dialWebSocket(ctx, cfg, token, streamID)
	case ModeHTTPSLanes:
		return newHTTPSCarrier(ctx, cfg, token, streamID), nil
	default:
		return nil, fmt.Errorf("mode %q: %w", mode, ErrCarrierClosed)
	}
}

// wsCarrier carries frame batches over one WebSocket connection.
type wsCarrier struct {
	conn   *websocket.Conn
	cancel context.CancelFunc
	once   sync.Once
}

func dialWebSocket(ctx context.Context, cfg Config, token string, streamID uint32) (*wsCarrier, error) {
	protocol := wsProtocolPrefix + token
	if streamID != 0 {
		protocol = wsLaneProtocolPrefix + token + "." + strconv.FormatUint(uint64(streamID), 10)
	}

	wsCtx, cancel := context.WithCancel(ctx)

	target := strings.Replace(cfg.baseURL(), "https://", "wss://", 1)
	if strings.HasPrefix(target, "http://") {
		target = "ws" + strings.TrimPrefix(target, "http")
	}

	conn, response, err := websocket.Dial(wsCtx, target+wsPath, &websocket.DialOptions{
		Subprotocols: []string{protocol},
		HTTPClient:   cfg.httpClient(),
		Host:         cfg.Host,
	})

	if response != nil && response.Body != nil {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}

	if err != nil {
		cancel()

		return nil, fmt.Errorf("dial websocket: %w: %w", err, ErrCarrierClosed)
	}

	if conn.Subprotocol() != protocol {
		_ = conn.Close(websocket.StatusProtocolError, "subprotocol mismatch")

		cancel()

		return nil, fmt.Errorf("subprotocol %q: %w", conn.Subprotocol(), ErrCarrierClosed)
	}

	return &wsCarrier{conn: conn, cancel: cancel}, nil
}

// Send writes one complete binary frame batch.
func (c *wsCarrier) Send(batch []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), carrierIOTimeout)
	defer cancel()

	writer, err := c.conn.Writer(ctx, websocket.MessageBinary)
	if err != nil {
		return fmt.Errorf("ws writer: %w: %w", err, ErrCarrierClosed)
	}

	if _, err := writer.Write(batch); err != nil {
		return fmt.Errorf("ws write: %w: %w", err, ErrCarrierClosed)
	}

	if err := writer.Close(); err != nil {
		return fmt.Errorf("ws close message: %w: %w", err, ErrCarrierClosed)
	}

	return nil
}

// Recv blocks until the next complete binary frame batch arrives.
func (c *wsCarrier) Recv() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), carrierIOTimeout)
	defer cancel()

	kind, batch, err := c.conn.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("ws read: %w: %w", err, ErrCarrierClosed)
	}

	if kind != websocket.MessageBinary {
		return nil, fmt.Errorf("ws message kind %d: %w", kind, ErrCarrierClosed)
	}

	return batch, nil
}

// Close cancels the carrier context and closes the WebSocket.
func (c *wsCarrier) Close() error {
	var err error

	c.once.Do(func() {
		c.cancel()
		err = c.conn.Close(websocket.StatusNormalClosure, "client close")
	})

	if err != nil {
		return fmt.Errorf("ws close: %w", err)
	}

	return nil
}

// httpsDelivery is one downlink result delivered to Recv.
type httpsDelivery struct {
	batch []byte
	err   error
}

// httpsCarrier carries frame batches over serialized or lane-scoped HTTPS.
type httpsCarrier struct {
	client  *http.Client
	baseURL string
	host    string
	token   string
	laneID  uint32
	done    <-chan struct{}
	stop    context.CancelFunc

	writeMu sync.Mutex
	seq     uint64

	stateMu     sync.Mutex
	cursor      uint64
	laneDone    bool
	terminal    error
	pollStarted bool

	stopOnce sync.Once
	inbox    chan httpsDelivery
}

func newHTTPSCarrier(ctx context.Context, cfg Config, token string, laneID uint32) *httpsCarrier {
	pollCtx, cancel := context.WithCancel(ctx)

	return &httpsCarrier{
		client:  cfg.httpClient(),
		baseURL: cfg.baseURL(),
		host:    cfg.Host,
		token:   token,
		laneID:  laneID,
		done:    pollCtx.Done(),
		stop:    cancel,
		seq:     1,
		inbox:   make(chan httpsDelivery, deliveryBuffer),
	}
}

// Send posts one uplink batch under the next sequence number, retrying 503
// responses byte-identically via exchange.
func (c *httpsCarrier) Send(batch []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if c.seq == 0 {
		return fmt.Errorf("sequence exhausted: %w", ErrCarrierClosed)
	}

	seq := c.seq
	sequence := strconv.FormatUint(seq, 10)
	ctx, cancel := c.requestCtx()

	defer cancel()

	response, err := exchange(ctx, c.client, func() (*http.Request, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost,
			c.baseURL+upPath, strings.NewReader(string(batch)))
		if err != nil {
			return nil, fmt.Errorf("build up request: %w", err)
		}

		request.Host = c.host
		request.Header.Set("Authorization", "Bearer "+c.token)
		request.Header.Set("Content-Type", octetStream)
		request.Header.Set(hdrUpSeq, sequence)
		request.ContentLength = int64(len(batch))

		if c.laneID != 0 {
			request.Header.Set(hdrLaneID, strconv.FormatUint(uint64(c.laneID), 10))
		}

		return request, nil
	})
	if err != nil {
		return fmt.Errorf("uplink seq %s: %w", sequence, err)
	}

	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)

	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("uplink seq %s: status %d: %w", sequence, response.StatusCode, ErrCarrierClosed)
	}

	if response.Header.Get(hdrUpAck) != sequence {
		return fmt.Errorf("uplink ack %q for seq %s: %w",
			response.Header.Get(hdrUpAck), sequence, ErrCarrierClosed)
	}

	c.seq = seq + 1

	return nil
}

// Recv blocks until the next complete downlink batch arrives.
func (c *httpsCarrier) Recv() ([]byte, error) {
	c.stateMu.Lock()
	started := c.pollStarted
	terminal := c.terminal
	c.pollStarted = true
	c.stateMu.Unlock()

	if terminal != nil {
		return nil, fmt.Errorf("downlink: %w", terminal)
	}

	if !started {
		pollCtx := c.pollContext()
		go c.pollLoop(pollCtx)
	}

	select {
	case delivery := <-c.inbox:
		if delivery.err != nil {
			c.recordTerminal(delivery.err)
		}

		if delivery.err != nil {
			return nil, fmt.Errorf("downlink: %w", delivery.err)
		}

		return delivery.batch, nil
	case <-c.done:
		c.recordTerminal(ErrCarrierClosed)

		return nil, fmt.Errorf("downlink: %w", ErrCarrierClosed)
	}
}

// Close stops the poll loop and fails pending and future Recv calls.
func (c *httpsCarrier) Close() error {
	c.stopOnce.Do(func() {
		c.stop()
	})

	return nil
}

func (c *httpsCarrier) pollContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		select {
		case <-c.done:
			cancel()
		case <-ctx.Done():
		}
	}()

	return ctx
}

func (c *httpsCarrier) pollLoop(ctx context.Context) {
	for {
		err := c.poll(ctx)
		if err == nil {
			continue
		}

		c.recordTerminal(err)
		c.deliver(httpsDelivery{err: err})

		return
	}
}

func (c *httpsCarrier) poll(ctx context.Context) error {
	c.stateMu.Lock()
	closed := c.laneDone
	cursor := c.cursor
	c.stateMu.Unlock()

	if closed {
		return ErrLaneClosed
	}

	cursorText := strconv.FormatUint(cursor, 10)
	requestCtx, cancel := context.WithTimeout(ctx, carrierRequestTimeout)

	defer cancel()

	response, err := exchange(requestCtx, c.client, func() (*http.Request, error) {
		request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, c.baseURL+downPath, nil)
		if err != nil {
			return nil, fmt.Errorf("build down request: %w", err)
		}

		request.Host = c.host
		request.Header.Set("Authorization", "Bearer "+c.token)
		request.Header.Set(hdrDownCursor, cursorText)

		if c.laneID != 0 {
			request.Header.Set(hdrLaneID, strconv.FormatUint(uint64(c.laneID), 10))
		}

		return request, nil
	})
	if err != nil {
		return fmt.Errorf("poll cursor %s: %w", cursorText, err)
	}

	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, DefaultBatchBytes+int64(maxSessionBodySize)))
	if err != nil {
		return fmt.Errorf("read poll body: %w", err)
	}

	next, err := strconv.ParseUint(response.Header.Get(hdrDownCursor), 10, 64)
	if err != nil {
		return fmt.Errorf("parse %s: %w", hdrDownCursor, err)
	}

	switch response.StatusCode {
	case http.StatusOK:
		if len(body) == 0 {
			return fmt.Errorf("%w: empty 200 body", ErrCarrierClosed)
		}

		c.stateMu.Lock()
		c.cursor = next
		c.stateMu.Unlock()
		c.deliver(httpsDelivery{batch: body})

		return nil
	case http.StatusNoContent:
		if response.Header.Get(hdrLaneClosed) == "1" {
			c.stateMu.Lock()
			c.laneDone = true
			c.stateMu.Unlock()

			return ErrLaneClosed
		}

		if next != cursor {
			c.stateMu.Lock()
			c.cursor = next
			c.stateMu.Unlock()
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("re-poll wait: %w", ctx.Err())
		case <-time.After(emptyPollPause):
		}

		return nil
	default:
		return fmt.Errorf("%w: status %d", ErrCarrierClosed, response.StatusCode)
	}
}

func (c *httpsCarrier) requestCtx() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), carrierRequestTimeout)

	go func() {
		select {
		case <-c.done:
			cancel()
		case <-ctx.Done():
		}
	}()

	return ctx, cancel
}

func (c *httpsCarrier) recordTerminal(err error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()

	if c.terminal == nil {
		c.terminal = err
	}
}

func (c *httpsCarrier) deliver(delivery httpsDelivery) {
	select {
	case c.inbox <- delivery:
	case <-c.done:
	}
}
