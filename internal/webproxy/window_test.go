package webproxy_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/webproxy"

	"github.com/stretchr/testify/require"
)

func TestSendWindowStartsAtInitialCredit(t *testing.T) {
	t.Parallel()

	w := webproxy.NewSendWindow()
	require.Equal(t, uint32(webproxy.InitialWindow), w.Available())
}

func TestSendWindowReserveDecrements(t *testing.T) {
	t.Parallel()

	w := webproxy.NewSendWindow()
	ctx := context.Background()

	got, err := w.Reserve(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, 100, got)
	require.Equal(t, uint32(webproxy.InitialWindow-100), w.Available())
}

func TestSendWindowReservePartialWhenShort(t *testing.T) {
	t.Parallel()

	w := webproxy.NewSendWindow()
	ctx := context.Background()

	got, err := w.Reserve(ctx, webproxy.InitialWindow+128)
	require.NoError(t, err)
	require.Equal(t, webproxy.InitialWindow, got)
	require.Equal(t, uint32(0), w.Available())
}

func TestSendWindowReserveRejectsNonPositiveWant(t *testing.T) {
	t.Parallel()

	w := webproxy.NewSendWindow()

	_, err := w.Reserve(context.Background(), 0)
	require.ErrorIs(t, err, webproxy.ErrWindowAmount)
}

func TestSendWindowReserveBlocksUntilGrant(t *testing.T) {
	t.Parallel()

	w := webproxy.NewSendWindow()
	ctx := context.Background()

	got, err := w.Reserve(ctx, webproxy.InitialWindow)
	require.NoError(t, err)
	require.Equal(t, webproxy.InitialWindow, got)

	time.AfterFunc(30*time.Millisecond, func() { w.Grant(64) })

	started := time.Now()
	got, err = w.Reserve(ctx, 1024)
	require.NoError(t, err)
	require.Equal(t, 64, got)
	require.GreaterOrEqual(t, time.Since(started), 20*time.Millisecond)
}

func TestSendWindowReserveHonorsContext(t *testing.T) {
	t.Parallel()

	w := webproxy.NewSendWindow()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	_, err := w.Reserve(ctx, webproxy.InitialWindow)
	require.NoError(t, err)

	_, err = w.Reserve(ctx, 1)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestSendWindowGrantSaturates(t *testing.T) {
	t.Parallel()

	w := webproxy.NewSendWindow()

	w.Grant(0xFFFFFFFF)
	w.Grant(0xFFFFFFFF)
	require.Equal(t, uint32(0xFFFFFFFF), w.Available())
}

func TestSendWindowGrantZeroIsNoop(t *testing.T) {
	t.Parallel()

	w := webproxy.NewSendWindow()
	w.Grant(0)
	require.Equal(t, uint32(webproxy.InitialWindow), w.Available())
}

func TestSendWindowAbortUnblocksReserve(t *testing.T) {
	t.Parallel()

	w := webproxy.NewSendWindow()
	boom := errors.New("boom")

	_, err := w.Reserve(context.Background(), webproxy.InitialWindow)
	require.NoError(t, err)

	w.Abort(boom)
	require.Equal(t, uint32(0), w.Available())

	_, err = w.Reserve(context.Background(), 1)
	require.ErrorIs(t, err, boom)
}

func TestRecvWindowGrantsAfterHalfWindowConsumed(t *testing.T) {
	t.Parallel()

	w := webproxy.NewRecvWindow()
	half := webproxy.InitialWindow / 2

	require.Equal(t, uint32(0), w.Consume(half-1))
	require.Equal(t, uint32(half), w.Consume(1))
	require.Equal(t, uint32(0), w.Consume(1))
}

func TestRecvWindowGrantAccumulatesAcrossBoundary(t *testing.T) {
	t.Parallel()

	w := webproxy.NewRecvWindow()
	half := webproxy.InitialWindow / 2

	require.Equal(t, uint32(0), w.Consume(half/2))
	require.Equal(t, uint32(0), w.Consume(half/2-1))
	require.Equal(t, uint32(half), w.Consume(1))
	require.Equal(t, uint32(0), w.Consume(half/3))
}

func TestRecvWindowConsumeIgnoresNonPositive(t *testing.T) {
	t.Parallel()

	w := webproxy.NewRecvWindow()
	require.Equal(t, uint32(0), w.Consume(0))
	require.Equal(t, uint32(0), w.Consume(-5))
	require.Equal(t, uint32(0), w.Consume(1))
}

func TestDataChunkSizeCapsToProtocolChunk(t *testing.T) {
	t.Parallel()

	require.Equal(t, webproxy.DataChunk, webproxy.DataChunkSize(webproxy.DataChunk*3))
	require.Equal(t, webproxy.DataChunk, webproxy.DataChunkSize(webproxy.DataChunk+1))
	require.Equal(t, 12345, webproxy.DataChunkSize(12345))
}
