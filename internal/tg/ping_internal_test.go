package tg

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errFakePingDown simulates a failed timed operation.
var errFakePingDown = errors.New("ping link down")

// fakePingConn is a dial stand-in that can fail on close.
type fakePingConn struct {
	closeErr error
}

func (c *fakePingConn) Close() error { return c.closeErr }

// recordingMeasure captures the call order of the two timed operations.
type recordingMeasure struct {
	dialErr  error
	rpcErr   error
	rpcDelay time.Duration
	events   []string
}

func (m *recordingMeasure) dial(context.Context, int) (io.Closer, error) {
	m.events = append(m.events, "dial")

	return &fakePingConn{}, m.dialErr
}

func (m *recordingMeasure) rpc(context.Context, int) error {
	m.events = append(m.events, "rpc")

	if m.rpcDelay > 0 {
		time.Sleep(m.rpcDelay)
	}

	return m.rpcErr
}

func TestMeasurePingDCSplitsConnectAndRTT(t *testing.T) {
	t.Parallel()

	measure := &recordingMeasure{rpcDelay: 30 * time.Millisecond}

	connectMs, rttMs, err := measurePingDC(t.Context(), 2, measure.dial, measure.rpc)
	require.NoError(t, err)

	assert.GreaterOrEqual(t, connectMs, int64(0), "connect time is recorded")
	assert.GreaterOrEqual(t, rttMs, int64(20), "rpc delay must surface in the rtt split")
	assert.Equal(t, []string{"dial", "rpc"}, measure.events, "dial runs before the rpc probe")
}

func TestMeasurePingDCDialFailure(t *testing.T) {
	t.Parallel()

	measure := &recordingMeasure{dialErr: errFakePingDown}

	connectMs, rttMs, err := measurePingDC(t.Context(), 4, measure.dial, measure.rpc)
	require.ErrorIs(t, err, errFakePingDown)

	assert.Equal(t, int64(0), connectMs)
	assert.Equal(t, int64(0), rttMs)
	assert.NotContains(t, measure.events, "rpc", "a failed dial must not probe rpc")
}

func TestMeasurePingDCRPCFailureKeepsConnectTime(t *testing.T) {
	t.Parallel()

	measure := &recordingMeasure{rpcErr: errFakePingDown}

	connectMs, rttMs, err := measurePingDC(t.Context(), 2, measure.dial, measure.rpc)
	require.ErrorIs(t, err, errFakePingDown)

	assert.GreaterOrEqual(t, connectMs, int64(0), "connect time survives the rpc failure")
	assert.Equal(t, int64(0), rttMs)
}

func TestMeasurePingDCCloseFailureSurfaces(t *testing.T) {
	t.Parallel()

	dial := func(context.Context, int) (io.Closer, error) {
		return &fakePingConn{closeErr: errFakePingDown}, nil
	}

	_, _, err := measurePingDC(t.Context(), 2, dial, func(context.Context, int) error { return nil })
	require.ErrorIs(t, err, errFakePingDown)
}
