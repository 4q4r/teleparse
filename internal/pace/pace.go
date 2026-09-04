// Package pace spaces out downloads to stay under Telegram rate limits:
// jittered inter-download delays, a concurrency semaphore and account-wide
// flood-wait pausing broadcast to every worker.
package pace

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"
)

// Config tunes a Pacer; the fields map one-to-one onto config.Pacing.
type Config struct {
	Concurrency         int
	DelayMin            time.Duration
	DelayMax            time.Duration
	FloodSleepThreshold time.Duration
}

// Clock abstracts the time facilities Pacer depends on. StandardClock is the
// production implementation; tests inject an instant fake.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
	AfterFunc(d time.Duration, fn func()) func()
}

// StandardClock is the wall-clock Clock implementation.
type StandardClock struct{}

// Now returns the current wall-clock time.
func (StandardClock) Now() time.Time { return time.Now() }

// Sleep suspends the caller for d or until ctx is cancelled.
func (StandardClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("pace sleep: %w", err)
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return fmt.Errorf("pace sleep: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

// AfterFunc runs fn after d has elapsed and returns its stop function.
func (StandardClock) AfterFunc(d time.Duration, fn func()) func() {
	timer := time.AfterFunc(d, fn)

	return func() { timer.Stop() }
}

// Pacer coordinates download pacing across all worker goroutines. Wait
// applies the jittered delay and blocks during flood pauses; Acquire gates
// the number of concurrent downloads.
type Pacer struct {
	sem       chan struct{}
	delayMin  time.Duration
	spread    time.Duration
	threshold time.Duration
	clock     Clock

	mu         sync.Mutex
	floodUntil time.Time
	floodWake  chan struct{}
	floodGen   uint64
	floodStop  func()
}

// New returns a Pacer running on the wall clock. A concurrency below one and
// inverted delay bounds are clamped to sane values.
func New(cfg Config) *Pacer {
	return NewWithClock(cfg, StandardClock{})
}

// NewWithClock returns a Pacer driven by clock.
func NewWithClock(cfg Config, clock Clock) *Pacer {
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}

	if cfg.DelayMin < 0 {
		cfg.DelayMin = 0
	}

	if cfg.DelayMax < cfg.DelayMin {
		cfg.DelayMax = cfg.DelayMin
	}

	return &Pacer{
		sem:       make(chan struct{}, cfg.Concurrency),
		delayMin:  cfg.DelayMin,
		spread:    cfg.DelayMax - cfg.DelayMin,
		threshold: cfg.FloodSleepThreshold,
		clock:     clock,
		floodWake: make(chan struct{}),
	}
}

// Wait blocks until any flood pause has lifted and then for a random delay
// in [DelayMin, DelayMax]; it returns early when ctx is cancelled.
func (p *Pacer) Wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("pace wait: %w", err)
	}

	if err := p.awaitFlood(ctx); err != nil {
		return err
	}

	if err := p.clock.Sleep(ctx, p.jitter()); err != nil {
		return fmt.Errorf("pace sleep: %w", err)
	}

	return nil
}

// Acquire takes one concurrency slot, blocking while every slot is held.
func (p *Pacer) Acquire(ctx context.Context) error {
	select {
	case p.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("pace acquire: %w", ctx.Err())
	}
}

// Release returns a previously acquired concurrency slot.
func (p *Pacer) Release() {
	<-p.sem
}

// ReportFlood pauses all waiters for seconds, extending any pause already
// in effect.
func (p *Pacer) ReportFlood(seconds int) {
	if seconds <= 0 {
		return
	}

	p.parkFor(time.Duration(seconds) * time.Second)
}

// ParkUntil pauses all waiters until the deadline; the run manager uses it
// for flood waits longer than FloodSleepThreshold.
func (p *Pacer) ParkUntil(deadline time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.setDeadlineLocked(deadline)
}

// ShouldPark reports whether a flood wait of seconds exceeds the sleep
// threshold and must park the run instead of sleeping inline.
func (p *Pacer) ShouldPark(seconds int) bool {
	return time.Duration(seconds)*time.Second > p.threshold
}

func (p *Pacer) parkFor(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.setDeadlineLocked(p.clock.Now().Add(d))
}

// setDeadlineLocked lifts the pause no earlier than deadline, replacing any
// closer active deadline. Callers hold p.mu.
func (p *Pacer) setDeadlineLocked(deadline time.Time) {
	now := p.clock.Now()
	if !deadline.After(now) || !deadline.After(p.floodUntil) {
		return
	}

	if p.floodStop != nil {
		p.floodStop()
	}

	p.floodGen++

	gen := p.floodGen
	p.floodUntil = deadline
	p.floodStop = p.clock.AfterFunc(deadline.Sub(now), func() {
		p.mu.Lock()
		defer p.mu.Unlock()

		if gen != p.floodGen {
			return
		}

		p.floodUntil = time.Time{}
		close(p.floodWake)
		p.floodWake = make(chan struct{})
	})
}

func (p *Pacer) awaitFlood(ctx context.Context) error {
	for {
		p.mu.Lock()

		if !p.floodUntil.After(p.clock.Now()) {
			p.mu.Unlock()

			return nil
		}

		wake := p.floodWake
		p.mu.Unlock()

		select {
		case <-ctx.Done():
			return fmt.Errorf("pace flood wait: %w", ctx.Err())
		case <-wake:
		}
	}
}

func (p *Pacer) jitter() time.Duration {
	if p.spread <= 0 {
		return p.delayMin
	}

	return p.delayMin + time.Duration(rand.Int64N(int64(p.spread)+1)) //nolint:gosec // jitter is not security-sensitive
}
