package tg

import (
	"context"
	"fmt"
	"sync"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/downloader"
	tgapi "github.com/gotd/td/tg"
)

// poolClient is the narrow seam DownloadPools dials pools through; the
// production telegram.Client satisfies it and tests inject fakes.
type poolClient interface {
	Pool(maxConns int64) (telegram.CloseInvoker, error)
	MediaOnly(ctx context.Context, dc int, maxConns int64) (telegram.CloseInvoker, error)
}

// minPoolConns is the defensive floor for the per-DC connection count;
// config validation already enforces the real 1..8 range.
const minPoolConns = 1

// DownloadPools lazily builds and caches one RPC client per data center:
// media-only connection pools for known file DCs (the transport official
// clients use for downloads) and a single home-DC pool for dc 0 (unknown).
// Every pool holds up to maxConns MTProto connections, reused across files,
// so parallel ranged downloads no longer multiplex over one connection.
// It implements download.InvokerSource structurally.
type DownloadPools struct {
	client  poolClient
	maxConn int64

	mu      sync.Mutex
	home    downloader.Client
	perDC   map[int]downloader.Client
	closers []telegram.CloseInvoker
}

// NewDownloadPools prepares the lazy pool cache; no connection is dialed
// until the first InvokerFor call.
func NewDownloadPools(client poolClient, maxConns int64) *DownloadPools {
	if maxConns < minPoolConns {
		maxConns = minPoolConns
	}

	return &DownloadPools{
		client:  client,
		maxConn: maxConns,
		perDC:   map[int]downloader.Client{},
	}
}

// InvokerFor returns the download RPC target for dc: the cached media-only
// pool, the home pool for dc < 1, or a freshly dialed pool. Creation errors
// are returned un-cached so callers can fall back and later calls can retry.
//
//nolint:ireturn // the downloader.Client union is the gotd download contract
func (p *DownloadPools) InvokerFor(ctx context.Context, dcID int) (downloader.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if dcID < minPoolConns {
		return p.homeLocked()
	}

	if cached, ok := p.perDC[dcID]; ok {
		return cached, nil
	}

	invoker, err := p.client.MediaOnly(ctx, dcID, p.maxConn)
	if err != nil {
		return nil, fmt.Errorf("media pool dc %d: %w", dcID, err)
	}

	return p.publish(invoker, dcID)
}

// Close releases every dialed pool. It must be called after the last
// InvokerFor user finishes; download clients borrowed earlier keep working
// only until this point.
func (p *DownloadPools) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var closeErr error

	for _, invoker := range p.closers {
		if err := invoker.Close(); err != nil {
			closeErr = fmt.Errorf("close pool: %w: %w", err, closeErr)
		}
	}

	p.closers = nil
	p.perDC = map[int]downloader.Client{}
	p.home = nil

	return closeErr
}

// homeLocked lazily builds the home-DC pool; caller holds the mutex.
//
//nolint:ireturn // the gotd download contract
func (p *DownloadPools) homeLocked() (downloader.Client, error) {
	if p.home != nil {
		return p.home, nil
	}

	invoker, err := p.client.Pool(p.maxConn)
	if err != nil {
		return nil, fmt.Errorf("home pool: %w", err)
	}

	client := tgapi.NewClient(invoker)

	p.home = client
	p.closers = append(p.closers, invoker)

	return client, nil
}

// publish wraps a fresh invoker and caches it; caller holds the mutex.
//
//nolint:ireturn // the gotd download contract
func (p *DownloadPools) publish(invoker telegram.CloseInvoker, dcID int) (downloader.Client, error) {
	client := tgapi.NewClient(invoker)

	p.perDC[dcID] = client
	p.closers = append(p.closers, invoker)

	return client, nil
}
