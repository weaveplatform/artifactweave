package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// Store keeps encoded sessions between requests. Get returns an error
// wrapping blob.ErrUploadUnknown for an unknown ID; Delete of a missing
// session succeeds.
type Store interface {
	Put(ctx context.Context, id string, state []byte, size int64) error
	Get(ctx context.Context, id string) ([]byte, error)
	Delete(ctx context.Context, id string) error
}

// Memory is a Store in process memory. Sessions do not survive a restart;
// it serves tests and embedders without a metadata store.
type Memory struct {
	mu sync.Mutex
	m  map[string][]byte
}

// NewMemory returns an empty Memory.
func NewMemory() *Memory { return &Memory{m: map[string][]byte{}} }

// Put stores a copy of state.
func (m *Memory) Put(_ context.Context, id string, state []byte, _ int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.m[id] = slices.Clone(state)
	return nil
}

// Get returns a copy of the session.
func (m *Memory) Get(_ context.Context, id string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.m[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", blob.ErrUploadUnknown, id)
	}
	return slices.Clone(st), nil
}

// Delete forgets a session.
func (m *Memory) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.m, id)
	return nil
}

// InUploads keeps sessions in the metadata store's uploads table (decision
// 0004) under backend, which names the blob store instance, for example
// "s3:hot" or "gcs:archive". A session stored under another backend name is
// unknown here, so the stale-upload reaper always knows which store aborts a
// session.
func InUploads(us store.UploadStore, backend string) Store {
	return uploads{us: us, backend: backend, now: time.Now}
}

type uploads struct {
	us      store.UploadStore
	backend string
	now     func() time.Time
}

// Put stores state, keeping the session's original start time.
func (u uploads) Put(ctx context.Context, id string, state []byte, size int64) error {
	now := u.now()
	started := now
	cur, err := u.us.Get(ctx, id)
	switch {
	case err == nil:
		started = cur.Started
	case !errors.Is(err, store.ErrNotFound):
		return fmt.Errorf("blob/session: load upload %s: %w", id, err)
	}
	if err := u.us.Put(ctx, store.Upload{
		ID: id, Backend: u.backend, State: state, Bytes: size, Started: started, Updated: now,
	}); err != nil {
		return fmt.Errorf("blob/session: store upload %s: %w", id, err)
	}
	return nil
}

// Get returns the session, or an error wrapping blob.ErrUploadUnknown when
// the table has none under id for this backend.
func (u uploads) Get(ctx context.Context, id string) ([]byte, error) {
	rec, err := u.us.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("%w: %s", blob.ErrUploadUnknown, id)
	}
	if err != nil {
		return nil, fmt.Errorf("blob/session: load upload %s: %w", id, err)
	}
	if rec.Backend != u.backend {
		return nil, fmt.Errorf("%w: %s belongs to %q, not %q",
			blob.ErrUploadUnknown, id, rec.Backend, u.backend)
	}
	return rec.State, nil
}

// Delete forgets a session; forgetting one that is already gone succeeds.
func (u uploads) Delete(ctx context.Context, id string) error {
	if err := u.us.Delete(ctx, id); err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("blob/session: forget upload %s: %w", id, err)
	}
	return nil
}
