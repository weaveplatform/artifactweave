package remote

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var ErrBusy = errors.New("remote: remote retrieval capacity exhausted")

type flight[T any] struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	result  T
	err     error
}

// flightGroup gives the shared operation its own context. A departing waiter
// cannot cancel its peers; the last departure cancels otherwise abandoned I/O.
// Only persisted results may be shared: an owned stream is never multicast.
type flightGroup[T any] struct {
	mu     sync.Mutex
	active map[string]*flight[T]
}

func (g *flightGroup[T]) do(
	ctx context.Context,
	key string,
	work func(context.Context) (T, error),
) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, fmt.Errorf("join remote retrieval: %w", err)
	}
	g.mu.Lock()
	if g.active == nil {
		g.active = make(map[string]*flight[T])
	}
	f := g.active[key]
	if f == nil {
		shared, cancel := context.WithCancel(context.WithoutCancel(ctx))
		f = &flight[T]{done: make(chan struct{}), cancel: cancel}
		g.active[key] = f
		go func() {
			result, err := work(shared)
			g.mu.Lock()
			f.result, f.err = result, err
			if g.active[key] == f {
				delete(g.active, key)
			}
			close(f.done)
			g.mu.Unlock()
			cancel()
		}()
	}
	f.waiters++
	g.mu.Unlock()
	select {
	case <-ctx.Done():
		g.mu.Lock()
		f.waiters--
		if f.waiters == 0 {
			f.cancel()
			if g.active[key] == f {
				delete(g.active, key)
			}
		}
		g.mu.Unlock()
		return zero, fmt.Errorf("wait for remote retrieval: %w", ctx.Err())
	case <-f.done:
		return f.result, f.err
	}
}

type admissionState struct{ active, queued int }

// admission bounds active work and queued callers without creating a goroutine
// for each blocked request. A single notification wakes contenders to recheck
// under the mutex; no repository reserves a global slot while it is full.
type admission struct {
	mu                     sync.Mutex
	changed                chan struct{}
	repos                  map[string]*admissionState
	active                 int
	perRepo, global, queue int
}

func (a *admission) acquire(ctx context.Context, repoKey string) (func(), error) {
	a.mu.Lock()
	if a.changed == nil {
		a.changed = make(chan struct{})
		a.repos = make(map[string]*admissionState)
		if a.perRepo <= 0 {
			a.perRepo = 8
		}
		if a.global <= 0 {
			a.global = 64
		}
		if a.queue <= 0 {
			a.queue = 256
		}
	}
	r := a.repos[repoKey]
	if r == nil {
		r = new(admissionState)
		a.repos[repoKey] = r
	}
	if r.queued >= a.queue {
		a.mu.Unlock()
		return nil, ErrBusy
	}
	r.queued++
	for {
		if err := ctx.Err(); err != nil {
			a.dequeue(repoKey, r)
			a.mu.Unlock()
			return nil, fmt.Errorf("wait for remote capacity: %w", err)
		}
		if a.active < a.global && r.active < a.perRepo {
			r.queued--
			r.active++
			a.active++
			a.mu.Unlock()
			var once sync.Once
			return func() { once.Do(func() { a.release(repoKey, r) }) }, nil
		}
		changed := a.changed
		a.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		}
		a.mu.Lock()
	}
}

func (a *admission) dequeue(key string, r *admissionState) {
	r.queued--
	if r.queued == 0 && r.active == 0 {
		delete(a.repos, key)
	}
}

func (a *admission) release(key string, r *admissionState) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.active--
	r.active--
	if r.queued == 0 && r.active == 0 {
		delete(a.repos, key)
	}
	close(a.changed)
	a.changed = make(chan struct{})
}
