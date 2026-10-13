// Package inmem is the reference in-memory implementation of pkg/store.
//
// # Design
//
// Each table is a map of Go values. Values are deep-copied on the way in and
// on the way out, so callers never share memory with the store, and on the
// way in every time is normalised to UTC microseconds and every duration to
// milliseconds, exactly what the SQL columns keep. One mutex serialises
// access; Tx holds it for the whole callback (calls made with the
// transaction's context skip re-locking) and restores a snapshot of every
// table when the callback fails, giving the all-or-nothing behaviour of the
// SQL backends. It serves unit tests and embedders that need no
// durability.
package inmem

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/weaveplatform/weaveplatform-artifact-registry/pkg/store"
)

// Store is an in-memory store.Store. The zero value is not usable; call New.
type Store struct {
	mu     sync.Mutex
	tables map[string]snapshotter
	seqs   map[string]int64
}

var _ store.Store = (*Store)(nil)

// New returns an empty store.
func New() *Store {
	return &Store{tables: map[string]snapshotter{}, seqs: map[string]int64{}}
}

type (
	txKey       struct{ s *Store }
	transaction struct{ after []func() }
)

// AfterCommit implements store.Store.
func (s *Store) AfterCommit(ctx context.Context, fn func()) {
	if tx, ok := ctx.Value(txKey{s}).(*transaction); ok {
		tx.after = append(tx.after, fn)
		return
	}
	fn()
}

// do runs fn under the lock unless ctx is inside this store's Tx.
func (s *Store) do(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("inmem: %w", err)
	}
	if ctx.Value(txKey{s}) != nil {
		return fn()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn()
}

// Tx implements store.Store.
func (s *Store) Tx(ctx context.Context, fn func(context.Context) error) error {
	if ctx.Value(txKey{s}) != nil {
		return fn(ctx)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("inmem: %w", err)
	}
	s.mu.Lock()
	tx := &transaction{}
	committed := false
	defer func() {
		s.mu.Unlock()
		if committed {
			for _, f := range tx.after {
				f()
			}
		}
	}()
	tables := make(map[string]snapshotter, len(s.tables))
	for name, t := range s.tables {
		tables[name] = t.snapshot()
	}
	seqs := maps.Clone(s.seqs)
	if err := fn(context.WithValue(ctx, txKey{s}, tx)); err != nil {
		s.tables, s.seqs = tables, seqs
		return err
	}
	committed = true
	return nil
}

// Close implements store.Store.
func (*Store) Close() error { return nil }

// nextID returns the next identifier of a sequence, starting at 1. Call it
// under the lock.
func (s *Store) nextID(seq string) int64 {
	s.seqs[seq]++
	return s.seqs[seq]
}

// snapshotter is a table that can copy itself for rollback.
type snapshotter interface {
	snapshot() snapshotter
}

// table is one in-memory table. Stored values are private copies that are
// replaced, never mutated, so a snapshot only needs to copy the map.
type table[K comparable, V any] struct {
	rows map[K]V
}

func (t *table[K, V]) snapshot() snapshotter {
	return &table[K, V]{rows: maps.Clone(t.rows)}
}

// tableOf returns the named table, creating it on first use. Call it under
// the lock; a name must always be used with the same key and value types.
func tableOf[K comparable, V any](s *Store, name string) *table[K, V] {
	if t, ok := s.tables[name]; ok {
		return t.(*table[K, V]) //nolint:forcetypeassert // one type per name by construction
	}
	t := &table[K, V]{rows: map[K]V{}}
	s.tables[name] = t
	return t
}

// get returns a copy of the row at k.
func (t *table[K, V]) get(k K) (V, bool) {
	v, ok := t.rows[k]
	if !ok {
		return v, false
	}
	return store.Clone(v), true
}

// has reports whether a row exists at k.
func (t *table[K, V]) has(k K) bool {
	_, ok := t.rows[k]
	return ok
}

// put stores a normalised copy of v at k.
func (t *table[K, V]) put(k K, v V) { t.rows[k] = store.Normalise(v) }

// del removes the row at k and reports whether there was one.
func (t *table[K, V]) del(k K) bool {
	_, ok := t.rows[k]
	delete(t.rows, k)
	return ok
}

// all returns copies of the rows matching keep (every row when nil),
// sorted by cmp.
func (t *table[K, V]) all(keep func(K, V) bool, cmp func(a, b V) int) []V {
	out := make([]V, 0, len(t.rows))
	for k, v := range t.rows {
		if keep == nil || keep(k, v) {
			out = append(out, store.Clone(v))
		}
	}
	if cmp != nil {
		slices.SortFunc(out, cmp)
	}
	return out
}

// deleteWhere removes the rows matching match and returns how many.
func (t *table[K, V]) deleteWhere(match func(K, V) bool) int64 {
	var n int64
	for k, v := range t.rows {
		if match(k, v) {
			delete(t.rows, k)
			n++
		}
	}
	return n
}

// notFound wraps store.ErrNotFound with what was looked up.
func notFound(what string, key any) error {
	return fmt.Errorf("%s %v: %w", what, key, store.ErrNotFound)
}

// conflict wraps store.ErrConflict with what collided.
func conflict(what string, key any) error {
	return fmt.Errorf("%s %v: %w", what, key, store.ErrConflict)
}
