package pace_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/pace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStandardClockWaitSleepsAndJitterCollapses(t *testing.T) {
	t.Parallel()

	// DelayMin == DelayMax leaves no spread, so the jitter floor applies
	// and the wall-clock Sleep path runs for a bounded millisecond.
	p := pace.New(pace.Config{Concurrency: 1, DelayMin: time.Millisecond, DelayMax: time.Millisecond})

	start := time.Now()
	require.NoError(t, p.Wait(t.Context()))
	elapsed := time.Since(start)

	assert.GreaterOrEqual(t, elapsed, time.Millisecond, "the zero-spread delay still sleeps once")
	assert.Less(t, elapsed, time.Second, "and stays bounded")
}

func TestStandardClockSleepRejectsCancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := pace.StandardClock{}.Sleep(ctx, time.Nanosecond)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
}

func TestStandardClockSleepAbortsOnCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := pace.StandardClock{}.Sleep(ctx, time.Minute)
	elapsed := time.Since(start)

	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	assert.Less(t, elapsed, 5*time.Second, "cancellation must cut the sleep short")
}

func TestStandardClockNowAdvances(t *testing.T) {
	t.Parallel()

	clock := pace.StandardClock{}

	first := clock.Now()
	require.NoError(t, clock.Sleep(t.Context(), time.Millisecond))

	assert.True(t, clock.Now().After(first), "the wall clock moves with sleeps")
}

func TestStandardClockAfterFuncFiresAndStops(t *testing.T) {
	t.Parallel()

	clock := pace.StandardClock{}

	var fired atomic.Int64

	clock.AfterFunc(30*time.Millisecond, func() { fired.Add(1) })
	require.NoError(t, clock.Sleep(t.Context(), 100*time.Millisecond))

	assert.Equal(t, int64(1), fired.Load(), "an unstopped timer fires exactly once")

	stopped := clock.AfterFunc(time.Hour, func() { fired.Add(1) })
	stopped()

	require.NoError(t, clock.Sleep(t.Context(), 10*time.Millisecond))
	assert.Equal(t, int64(1), fired.Load(), "a stopped timer never fires")
}

func TestNewWithClockClampsInvertedConfig(t *testing.T) {
	t.Parallel()

	clk := newFakeClock()
	p := pace.NewWithClock(pace.Config{
		Concurrency:         -3,
		DelayMin:            -5 * time.Second,
		DelayMax:            -10 * time.Second,
		FloodSleepThreshold: time.Minute,
	}, clk)

	require.NoError(t, p.Acquire(t.Context()), "concurrency below one clamps to one slot")
	p.Release()

	require.NoError(t, p.Acquire(t.Context()), "the released slot is immediately reusable")
	p.Release()

	require.NoError(t, p.Wait(t.Context()))

	delays := clk.recorded()
	require.Len(t, delays, 1)
	assert.Zero(t, delays[0], "inverted negative delay bounds clamp to a zero floor")

	require.NoError(t, p.Wait(t.Context()))
	require.Len(t, clk.recorded(), 2, "every wait sleeps exactly once")
}
