package fakeupstream

import (
	"context"
	"sync"
	"time"
)

// Clock supplies the same controllable time to the upstream and the cache under test.
// It does not replace timers: use barriers for network scheduling.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

func NewClock(now time.Time) *Clock { return &Clock{now: now.UTC()} }

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Barrier holds requests until Release. Reached closes when the first waiter arrives.
// Both releasing repeatedly and sharing one barrier across requests are safe.
type Barrier struct {
	init     sync.Once
	arrived  sync.Once
	released sync.Once
	reached  chan struct{}
	ready    chan struct{}
}

func (b *Barrier) initialize() {
	b.init.Do(func() {
		b.reached = make(chan struct{})
		b.ready = make(chan struct{})
	})
}

func (b *Barrier) Reached() <-chan struct{} {
	b.initialize()
	return b.reached
}

func (b *Barrier) Wait(ctx context.Context) error {
	b.initialize()
	b.arrived.Do(func() { close(b.reached) })
	select {
	case <-ctx.Done():
		return ctx.Err() //nolint:wrapcheck // Preserve the cancellation sentinel for callers.
	case <-b.ready:
		return nil
	}
}

func (b *Barrier) Release() {
	b.initialize()
	b.released.Do(func() { close(b.ready) })
}
