package tg

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/dcs"
	tgapi "github.com/gotd/td/tg"
)

// pingPoolConns is the single connection PingDC opens per probed DC.
const pingPoolConns = 1

// PingDC measures connectivity to one production DC through the given
// proxy URL ("" = direct): the first return is the transport dial time in
// milliseconds (proxy + TCP + MTProto channel setup), the second the
// round-trip of one updates.getState call in milliseconds on a
// single-connection pool dialed for that DC. client is the running
// telegram.Client (its per-DC media pools carry the exported
// authorization).
func PingDC(ctx context.Context, client poolClient, proxyURL string, dcID int) (int64, int64, error) {
	resolver, err := ParseProxyURL(proxyURL)
	if err != nil {
		return 0, 0, err
	}

	dial := func(ctx context.Context, dcID int) (io.Closer, error) {
		return resolver.Primary(ctx, dcID, dcs.Prod())
	}

	return measurePingDC(ctx, dcID, dial, pingRPC(client))
}

// pingRPC builds the DC round-trip probe: one pooled connection, one
// lightweight verified call, closed afterwards.
func pingRPC(client poolClient) func(ctx context.Context, dcID int) error {
	return func(ctx context.Context, dcID int) error {
		invoker, err := client.MediaOnly(ctx, dcID, pingPoolConns)
		if err != nil {
			return fmt.Errorf("pool dc %d: %w", dcID, err)
		}

		defer func() { _ = invoker.Close() }()

		if _, err := tgapi.NewClient(invoker).UpdatesGetState(ctx); err != nil {
			return fmt.Errorf("updates.getState dc %d: %w", dcID, err)
		}

		return nil
	}
}

// measurePingDC times one raw dial and one RPC round-trip against dcID,
// returning the connect and rtt halves in milliseconds.
func measurePingDC(ctx context.Context, dcID int,
	dial func(ctx context.Context, dcID int) (io.Closer, error),
	rpc func(ctx context.Context, dcID int) error,
) (int64, int64, error) {
	started := time.Now()

	conn, err := dial(ctx, dcID)
	if err != nil {
		return 0, 0, fmt.Errorf("dial dc %d: %w", dcID, err)
	}

	connectMs := time.Since(started).Milliseconds()

	if err := conn.Close(); err != nil {
		return connectMs, 0, fmt.Errorf("close ping connection dc %d: %w", dcID, err)
	}

	started = time.Now()

	if err := rpc(ctx, dcID); err != nil {
		return connectMs, 0, fmt.Errorf("ping rpc dc %d: %w", dcID, err)
	}

	return connectMs, time.Since(started).Milliseconds(), nil
}

// ProbeDC dials the given production DC through the proxy URL ("" = direct)
// and reports the transport connect latency; no RPC is issued, so it works
// without a session.
func ProbeDC(ctx context.Context, raw string, dcID int) (time.Duration, error) {
	resolver, err := ParseProxyURL(raw)
	if err != nil {
		return 0, err
	}

	started := time.Now()

	conn, err := resolver.Primary(ctx, dcID, dcs.Prod())
	if err != nil {
		return 0, fmt.Errorf("dial DC %d via %q: %w", dcID, raw, err)
	}

	if err := conn.Close(); err != nil {
		return 0, fmt.Errorf("close probe connection: %w", err)
	}

	return time.Since(started), nil
}

// ensure the production client satisfies the pool seam used by PingDC.
var _ poolClient = (*telegram.Client)(nil)
