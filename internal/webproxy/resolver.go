package webproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/gotd/td/crypto"
	"github.com/gotd/td/mtproxy"
	"github.com/gotd/td/mtproxy/obfuscator"
	"github.com/gotd/td/proto/codec"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/transport"
)

var _ dcs.Resolver = (*Resolver)(nil)

// Resolver implements gotd's dcs.Resolver over a WEB-proxy carrier session.
// Every resolved connection is one multiplexed stream; the client-side
// MTProxy transform is applied by gotd's obfuscator over that stream, so
// DATA payloads stay opaque to the relay.
type Resolver struct {
	cfg      Config
	protocol transport.Protocol
	secret   mtproxy.Secret
	tag      [4]byte
	rand     io.Reader

	mu      sync.Mutex
	session *Session
}

// NewResolver validates the configuration and returns a resolver. The secret
// is the decoded MTProxy secret, dd or ee prefix retained.
func NewResolver(cfg Config) (*Resolver, error) {
	secret, err := mtproxy.ParseSecret(cfg.Secret)
	if err != nil {
		return nil, fmt.Errorf("web proxy secret: %w", err)
	}

	var (
		connCodec codec.Codec = codec.PaddedIntermediate{}
		tag                   = codec.PaddedIntermediateClientStart
	)

	// FIXME from upstream: some proxies force Padded (Secure) Intermediate
	// even when the secret denotes another transport type.
	if secret.Type != mtproxy.TLS {
		if expected, ok := secret.ExpectedCodec(); ok {
			connCodec = expected
			tag = [4]byte{secret.Tag, secret.Tag, secret.Tag, secret.Tag}
		}
	}

	rand := crypto.DefaultRand()

	return &Resolver{
		cfg:      cfg,
		protocol: transport.NewProtocol(func() transport.Codec { return codec.NoHeader{Codec: connCodec} }),
		secret:   secret,
		tag:      tag,
		rand:     rand,
	}, nil
}

// Primary resolves a primary DC connection through the carrier session.
//
//nolint:ireturn // transport.Conn is mandated by the gotd dcs.Resolver contract
func (r *Resolver) Primary(ctx context.Context, dc int, _ dcs.List) (transport.Conn, error) {
	return r.connect(ctx, dc)
}

// MediaOnly resolves a media-only DC connection, mirroring dcs.MTProxy.
//
//nolint:ireturn // transport.Conn is mandated by the gotd dcs.Resolver contract
func (r *Resolver) MediaOnly(ctx context.Context, dc int, _ dcs.List) (transport.Conn, error) {
	if dc > 0 {
		dc = -dc
	}

	return r.connect(ctx, dc)
}

// CDN resolves a CDN DC connection through the same carrier session.
//
//nolint:ireturn // transport.Conn is mandated by the gotd dcs.Resolver contract
func (r *Resolver) CDN(ctx context.Context, dc int, _ dcs.List) (transport.Conn, error) {
	return r.connect(ctx, dc)
}

//nolint:ireturn // transport.Conn is mandated by the gotd dcs.Resolver contract
func (r *Resolver) connect(ctx context.Context, dcID int) (transport.Conn, error) {
	stream, err := r.acquireStream(ctx)
	if err != nil {
		return nil, err
	}

	conn, err := r.handshake(streamConn{stream: stream}, dcID)
	if err != nil {
		_ = stream.Close()

		return nil, err
	}

	return conn, nil
}

func (r *Resolver) acquireStream(ctx context.Context) (*Stream, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.session != nil {
		stream, err := r.session.Open(ctx)
		if err == nil {
			return stream, nil
		}

		if !errors.Is(err, ErrSessionLost) && !errors.Is(err, ErrSessionClosed) {
			return nil, err
		}
	}

	session, err := r.establish(ctx)
	if err != nil {
		return nil, err
	}

	r.session = session

	return session.Open(ctx)
}

func (r *Resolver) establish(ctx context.Context) (*Session, error) {
	result, err := RunBootstrap(ctx, BootstrapOptions{
		Client:  r.cfg.httpClient(),
		BaseURL: r.cfg.baseURL(),
		Host:    r.cfg.Host,
		Secret:  r.cfg.Secret,
		Mode:    r.cfg.CarrierMode,
	})
	if err != nil {
		return nil, fmt.Errorf("web proxy bootstrap: %w", err)
	}

	var carrier Carrier

	if result.CarrierMode != ModeWebSocketLanes {
		carrier, err = NewCarrier(ctx, r.cfg, result.CarrierMode, result.SessionToken)
		if err != nil {
			return nil, fmt.Errorf("web proxy carrier: %w", err)
		}
	}

	//nolint:contextcheck // the session owns a lifecycle independent of one dial
	return NewSession(r.cfg, result.CarrierMode, result.SessionToken, carrier), nil
}

//nolint:ireturn // transport.Conn is mandated by the gotd transport stack
func (r *Resolver) handshake(conn net.Conn, dcID int) (transport.Conn, error) {
	var obsConn *obfuscator.Conn

	switch r.secret.Type {
	case mtproxy.Simple, mtproxy.Secured:
		obsConn = obfuscator.Obfuscated2(r.rand, conn)
	case mtproxy.TLS:
		obsConn = obfuscator.FakeTLS(r.rand, conn)
	default:
		return nil, fmt.Errorf("secret type %d: %w", r.secret.Type, ErrCarrierClosed)
	}

	if err := obsConn.Handshake(r.tag, dcID, r.secret); err != nil {
		return nil, fmt.Errorf("mtproxy handshake: %w", err)
	}

	transportConn, err := r.protocol.Handshake(obsConn)
	if err != nil {
		return nil, fmt.Errorf("transport handshake: %w", err)
	}

	return transportConn, nil
}

// streamConn adapts one multiplexed Stream to net.Conn for the gotd
// obfuscator stack.
type streamConn struct {
	stream *Stream
}

func (c streamConn) Read(out []byte) (int, error) {
	return c.stream.Read(out)
}

func (c streamConn) Write(data []byte) (int, error) {
	return c.stream.Write(data)
}

func (c streamConn) Close() error {
	return c.stream.Close()
}

func (c streamConn) LocalAddr() net.Addr {
	return streamAddr{}
}

func (c streamConn) RemoteAddr() net.Addr {
	return streamAddr{}
}

func (c streamConn) SetDeadline(when time.Time) error {
	return c.stream.SetDeadline(when)
}

func (c streamConn) SetReadDeadline(when time.Time) error {
	return c.stream.SetReadDeadline(when)
}

func (c streamConn) SetWriteDeadline(when time.Time) error {
	return c.stream.SetWriteDeadline(when)
}

type streamAddr struct{}

func (streamAddr) Network() string {
	return "webproxy"
}

func (streamAddr) String() string {
	return "webproxy"
}
