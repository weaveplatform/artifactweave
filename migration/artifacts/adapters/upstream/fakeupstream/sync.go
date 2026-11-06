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

// NewClock returns a Clock set to now, in UTC.
func NewClock(now time.Time) *Clock { return &Clock{now: now.UTC()} }

// Now returns the clock's time. Pass the method value as Options.Now and to the
// cache under test. It is safe to call while Advance runs.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock on by d. It is safe to call while handlers read Now:
// a request already in flight may see either time, so advance before sending
// the request that must see the new one.
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

// Reached returns a channel that closes when the first request arrives at the
// barrier. Wait on it before inspecting counts, replacing a resource or
// cancelling a request, so the test acts while that request is held.
func (b *Barrier) Reached() <-chan struct{} {
	b.initialize()
	return b.reached
}

// Wait holds the caller until Release or until ctx is done, and the first call
// closes Reached. It returns nil once released, or ctx.Err() if ctx ends
// first. Any number of goroutines may wait at once. The server waits on a
// Fault's barriers with the request's context, so cancelling the request or
// closing the server frees the handler.
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

// Release opens the barrier for good: every waiter returns, and later calls to
// Wait return at once.
func (b *Barrier) Release() {
	b.initialize()
	b.released.Do(func() { close(b.ready) })
}
