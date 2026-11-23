package system

import (
	"context"
	"sync"
	"time"
)

// PublicURLRefresh bounds how long a urlBase written by another process,
// such as `config apply` against a live store, takes to reach this one's
// links. Writes through this process invalidate at once, so the interval
// only trades one single-row read per interval against that staleness.
const PublicURLRefresh = 5 * time.Second

// publicURLReadTimeout keeps a slow store from holding every request that
// waits on the refresh; the previous value stays in force meanwhile.
const publicURLReadTimeout = 2 * time.Second

// PublicURL is the effective configured base URL that links are built on:
// the stored urlBase while one is set, else Fallback, the installation's
// base_url. It is read on every request, so it caches the stored value for
// PublicURLRefresh and Invalidate drops the cache after a local commit.
type PublicURL struct {
	// Read returns the stored urlBase; ok is false when none is set.
	Read func(ctx context.Context) (value string, ok bool, err error)
	// Fallback is the installation's base_url, possibly "".
	Fallback string
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	mu      sync.Mutex
	value   string
	expires time.Time
}

// Get returns the effective configured base URL. Concurrent callers wait
// for one refresh rather than each reading the store. A failed read keeps
// the previous value, or Fallback before the first success, and is retried
// after PublicURLRefresh: links then name the last known base rather than
// failing the request that needed them.
func (p *PublicURL) Get(ctx context.Context) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if now.Before(p.expires) {
		return p.value
	}
	if p.expires.IsZero() && p.value == "" {
		p.value = p.Fallback
	}
	// The request may be cancelled while it waits; the shared value must
	// not depend on one caller's deadline.
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), publicURLReadTimeout)
	defer cancel()
	value, ok, err := p.Read(readCtx)
	switch {
	case err != nil:
	case ok:
		p.value = value
	default:
		p.value = p.Fallback
	}
	p.expires = now.Add(PublicURLRefresh)
	return p.value
}

// Invalidate makes the next Get read the store, after a write committed.
func (p *PublicURL) Invalidate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.expires.IsZero() {
		return
	}
	p.expires = time.Unix(0, 1)
}

func (p *PublicURL) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}
