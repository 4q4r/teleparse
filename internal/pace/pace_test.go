package pace_test

import (
	"context"
	"sync"
	"teleparse/internal/pace"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeTimer struct {
	when time.Time
	fn   func()
	off  bool
}

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
	timers []*fakeTimer
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.now
}

func (f *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.sleeps = append(f.sleeps, d)
	f.now = f.now.Add(d)

	return nil
}

func (f *fakeClock) AfterFunc(d time.Duration, fn func()) func() {
	f.mu.Lock()
	defer f.mu.Unlock()

	timer := &fakeTimer{when: f.now.Add(d), fn: fn}
	f.timers = append(f.timers, timer)

	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()

		timer.off = true
	}
}

func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)

	due := make([]*fakeTimer, 0, len(f.timers))
	keep := make([]*fakeTimer, 0, len(f.timers))

	for _, timer := range f.timers {
		switch {
		case timer.off:
		case !timer.when.After(f.now):
			due = append(due, timer)
		default:
			keep = append(keep, timer)
		}
	}

	f.timers = keep
	f.mu.Unlock()

	for _, timer := range due {
		timer.fn()
	}
}

func (f *fakeClock) recorded() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]time.Duration(nil), f.sleeps...)
}

func testConfig() pace.Config {
	return pace.Config{
		Concurrency:         2,
		DelayMin:            time.Second,
		DelayMax:            4 * time.Second,
		FloodSleepThreshold: time.Minute,
	}
}

func TestWaitSleepsWithinJitterBounds(t *testing.T) {
	t.Parallel()

	clk := newFakeClock()
	p := pace.NewWithClock(testConfig(), clk)

	const samples = 60

	for range samples {
		require.NoError(t, p.Wait(t.Context()))
	}

	delays := clk.recorded()
	require.Len(t, delays, samples)

	distinct := map[time.Duration]struct{}{}

	for _, d := range delays {
		assert.GreaterOrEqual(t, d, time.Second)
		assert.LessOrEqual(t, d, 4*time.Second)
		distinct[d] = struct{}{}
	}

	assert.Greater(t, len(distinct), 1, "delays must jitter across the configured range")
}

func TestWaitHonorsContextCancel(t *testing.T) {
	t.Parallel()

	p := pace.NewWithClock(testConfig(), newFakeClock())

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := p.Wait(ctx)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
}

func TestAcquireBlocksAtCapacity(t *testing.T) {
	t.Parallel()

	p := pace.New(testConfig())
	ctx := t.Context()

	require.NoError(t, p.Acquire(ctx))
	require.NoError(t, p.Acquire(ctx))

	done := make(chan error, 1)
	go func() { done <- p.Acquire(ctx) }()

	select {
	case err := <-done:
		t.Fatalf("third acquire must block while both slots are held, got %v", err)
	case <-time.After(80 * time.Millisecond):
	}

	p.Release()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("release must unblock the pending acquire")
	}

	p.Release()
}

func TestAcquireHonorsContextCancel(t *testing.T) {
	t.Parallel()

	p := pace.New(testConfig())
	ctx := t.Context()

	require.NoError(t, p.Acquire(ctx))
	require.NoError(t, p.Acquire(ctx))

	blocked, cancel := context.WithCancel(ctx)
	cancel()

	err := p.Acquire(blocked)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)

	p.Release()
	p.Release()
}

func TestReportFloodPausesAllWaiters(t *testing.T) {
	t.Parallel()

	clk := newFakeClock()
	p := pace.NewWithClock(testConfig(), clk)

	p.ReportFlood(30)

	ctx := t.Context()
	first := make(chan error, 1)
	second := make(chan error, 1)

	go func() { first <- p.Wait(ctx) }()
	go func() { second <- p.Wait(ctx) }()

	for _, waiter := range []chan error{first, second} {
		select {
		case err := <-waiter:
			t.Fatalf("waiter must stay paused during flood, got %v", err)
		case <-time.After(80 * time.Millisecond):
		}
	}

	clk.advance(31 * time.Second)

	select {
	case err := <-first:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("waiter must resume after the flood deadline passes")
	}

	select {
	case err := <-second:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("waiter must resume after the flood deadline passes")
	}
}

func TestReportFloodExtendsDeadline(t *testing.T) {
	t.Parallel()

	clk := newFakeClock()
	p := pace.NewWithClock(testConfig(), clk)

	p.ReportFlood(10)
	p.ReportFlood(30)

	ctx := t.Context()
	done := make(chan error, 1)
	go func() { done <- p.Wait(ctx) }()

	clk.advance(11 * time.Second)

	select {
	case err := <-done:
		t.Fatalf("extended flood deadline must still pause waiters, got %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	clk.advance(20 * time.Second)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("waiter must resume once the extended deadline passes")
	}
}

func TestParkUntilBlocksWaiters(t *testing.T) {
	t.Parallel()

	clk := newFakeClock()
	p := pace.NewWithClock(testConfig(), clk)

	deadline := clk.Now().Add(time.Hour)
	p.ParkUntil(deadline)

	ctx := t.Context()
	done := make(chan error, 1)
	go func() { done <- p.Wait(ctx) }()

	clk.advance(59 * time.Minute)

	select {
	case err := <-done:
		t.Fatalf("parked waiter must not proceed before the deadline, got %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	clk.advance(2 * time.Minute)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("parked waiter must resume after the deadline")
	}
}

func TestShouldParkThreshold(t *testing.T) {
	t.Parallel()

	p := pace.New(testConfig())

	assert.False(t, p.ShouldPark(59), "below threshold the middleware sleeps inline")
	assert.False(t, p.ShouldPark(60), "at threshold the middleware still sleeps inline")
	assert.True(t, p.ShouldPark(61), "above threshold the run must park")
}

func TestZeroFloodIsIgnored(t *testing.T) {
	t.Parallel()

	clk := newFakeClock()
	p := pace.NewWithClock(testConfig(), clk)

	p.ReportFlood(0)

	require.NoError(t, p.Wait(t.Context()))
	require.Len(t, clk.recorded(), 1, "zero-length floods must not pause waiters")
}
