package tg

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errFakePoolDown simulates a pool-creation failure.
var errFakePoolDown = errors.New("pool down")

// fakeInvoker is a closeable invoker stand-in recording its lifetime.
type fakeInvoker struct {
	dc     int
	mu     sync.Mutex
	closed bool
}

func (f *fakeInvoker) Invoke(context.Context, bin.Encoder, bin.Decoder) error { return nil }

func (f *fakeInvoker) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.closed = true

	return nil
}

func (f *fakeInvoker) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.closed
}

// fakePoolClient records pool-creation traffic.
type fakePoolClient struct {
	mu        sync.Mutex
	mediaDCs  []int
	poolMaxes []int64
	answers   map[int]*fakeInvoker
	failDC    map[int]error
}

func (c *fakePoolClient) Pool(maxConns int64) (telegram.CloseInvoker, error) { //nolint:ireturn // mirrors the gotd seam
	c.mu.Lock()
	defer c.mu.Unlock()

	c.poolMaxes = append(c.poolMaxes, maxConns)

	return c.answers[0], nil
}

func (c *fakePoolClient) MediaOnly(_ context.Context, dc int, maxConns int64) (telegram.CloseInvoker, error) { //nolint:ireturn // mirrors the gotd seam
	c.mu.Lock()
	defer c.mu.Unlock()

	c.mediaDCs = append(c.mediaDCs, dc)
	c.poolMaxes = append(c.poolMaxes, maxConns)

	if err, ok := c.failDC[dc]; ok {
		return nil, err
	}

	return c.answers[dc], nil
}

func (c *fakePoolClient) mediaCalls(dc int) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	count := 0

	for _, seen := range c.mediaDCs {
		if seen == dc {
			count++
		}
	}

	return count
}

func newFakePoolClient() *fakePoolClient {
	return &fakePoolClient{
		answers: map[int]*fakeInvoker{
			0: {dc: 0},
			2: {dc: 2},
			4: {dc: 4},
		},
		failDC: map[int]error{},
	}
}

func TestDownloadPoolsCachesPerDC(t *testing.T) {
	t.Parallel()

	client := newFakePoolClient()

	pools := NewDownloadPools(client, 3)

	first, err := pools.InvokerFor(t.Context(), 2)
	require.NoError(t, err)

	second, err := pools.InvokerFor(t.Context(), 2)
	require.NoError(t, err)

	assert.Same(t, first, second, "one pool per DC must be reused")
	assert.Equal(t, 1, client.mediaCalls(2), "MediaOnly must be called once per DC")
}

func TestDownloadPoolsRoutesUnknownDCToHomePool(t *testing.T) {
	t.Parallel()

	client := newFakePoolClient()

	pools := NewDownloadPools(client, 3)

	home, err := pools.InvokerFor(t.Context(), 0)
	require.NoError(t, err)

	again, err := pools.InvokerFor(t.Context(), 0)
	require.NoError(t, err)

	assert.Same(t, home, again)

	require.Len(t, client.poolMaxes, 1)
	assert.EqualValues(t, 3, client.poolMaxes[0], "home pool must use the configured max")
	assert.Empty(t, client.mediaDCs, "unknown DCs must never dial a media pool")
}

func TestDownloadPoolsSeparatesDCs(t *testing.T) {
	t.Parallel()

	client := newFakePoolClient()

	pools := NewDownloadPools(client, 2)

	dc2, err := pools.InvokerFor(t.Context(), 2)
	require.NoError(t, err)

	dc4, err := pools.InvokerFor(t.Context(), 4)
	require.NoError(t, err)

	assert.NotSame(t, dc2, dc4)
}

func TestDownloadPoolsCloseClosesEverything(t *testing.T) {
	t.Parallel()

	client := newFakePoolClient()

	pools := NewDownloadPools(client, 3)

	_, err := pools.InvokerFor(t.Context(), 0)
	require.NoError(t, err)

	_, err = pools.InvokerFor(t.Context(), 2)
	require.NoError(t, err)

	_, err = pools.InvokerFor(t.Context(), 4)
	require.NoError(t, err)

	require.NoError(t, pools.Close())

	for dc, invoker := range client.answers {
		assert.True(t, invoker.isClosed(), "pool for DC %d must be closed", dc)
	}
}

func TestDownloadPoolsSurfacesCreationErrors(t *testing.T) {
	t.Parallel()

	client := newFakePoolClient()
	client.failDC[4] = errFakePoolDown

	pools := NewDownloadPools(client, 3)

	_, err := pools.InvokerFor(t.Context(), 4)
	require.Error(t, err)
	require.ErrorIs(t, err, errFakePoolDown)

	// The failed DC must not poison the cache: a later good DC still works.
	good, err := pools.InvokerFor(t.Context(), 2)
	require.NoError(t, err)
	require.NotNil(t, good)
}
