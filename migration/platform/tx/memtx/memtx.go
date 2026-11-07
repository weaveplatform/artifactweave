package memtx

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/weaveplatform/artifactweave/migration/kernel"
)

// Store is an in-memory store.Store. The zero value is not usable; call New.
type Store struct {
	gate   chan struct{}
	tables map[string]snapshotter
	seqs   map[string]int64
}

// New returns an empty store.
func New() *Store {
	return &Store{
		gate:   make(chan struct{}, 1),
		tables: map[string]snapshotter{},
		seqs:   map[string]int64{},
	}
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

// Do runs fn under the lock unless ctx is inside this store's Tx.
func (s *Store) Do(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("inmem: %w", err)
	}
	if ctx.Value(txKey{s}) != nil {
		return fn()
	}
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer func() { <-s.gate }()
	return fn()
}

// InTx reports whether ctx is inside this store's Tx.
func (s *Store) InTx(ctx context.Context) bool { return ctx.Value(txKey{s}) != nil }

func (s *Store) lock(ctx context.Context) error {
	select {
	case s.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-s.gate
			return fmt.Errorf("inmem: %w", err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("inmem: %w", ctx.Err())
	}
}

// Tx implements store.Store.
func (s *Store) Tx(ctx context.Context, fn func(context.Context) error) error {
	if ctx.Value(txKey{s}) != nil {
		return fn(ctx)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("inmem: %w", err)
	}
	if err := s.lock(ctx); err != nil {
		return err
	}
	tx := &transaction{}
	committed := false
	defer func() {
		<-s.gate
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

// NextID returns the next identifier of a sequence, starting at 1. Call it
// under the lock.
func (s *Store) NextID(seq string) int64 {
	s.seqs[seq]++
	return s.seqs[seq]
}

// snapshotter is a table that can copy itself for rollback.
type snapshotter interface {
	snapshot() snapshotter
}

// Table is one in-memory table. Stored values are private copies that are
// replaced, never mutated, so a snapshot only needs to copy the map.
type Table[K comparable, V any] struct {
	Rows map[K]V
}

func (t *Table[K, V]) snapshot() snapshotter {
	return &Table[K, V]{Rows: maps.Clone(t.Rows)}
}

// TableOf returns the named table, creating it on first use. Call it under
// the lock; a name must always be used with the same key and value types.
func TableOf[K comparable, V any](s *Store, name string) *Table[K, V] {
	if t, ok := s.tables[name]; ok {
		return t.(*Table[K, V]) //nolint:forcetypeassert // one type per name by construction
	}
	t := &Table[K, V]{Rows: map[K]V{}}
	s.tables[name] = t
	return t
}

// Get returns a copy of the row at k.
func (t *Table[K, V]) Get(k K) (V, bool) {
	v, ok := t.Rows[k]
	if !ok {
		return v, false
	}
	return kernel.Clone(v), true
}

// Has reports whether a row exists at k.
func (t *Table[K, V]) Has(k K) bool {
	_, ok := t.Rows[k]
	return ok
}

// Put stores a normalised copy of v at k.
func (t *Table[K, V]) Put(k K, v V) { t.Rows[k] = kernel.Normalise(v) }

// Del removes the row at k and reports whether there was one.
func (t *Table[K, V]) Del(k K) bool {
	_, ok := t.Rows[k]
	delete(t.Rows, k)
	return ok
}

// All returns copies of the rows matching keep (every row when nil),
// sorted by cmp.
func (t *Table[K, V]) All(keep func(K, V) bool, cmp func(a, b V) int) []V {
	out := make([]V, 0, len(t.Rows))
	for k, v := range t.Rows {
		if keep == nil || keep(k, v) {
			out = append(out, kernel.Clone(v))
		}
	}
	if cmp != nil {
		slices.SortFunc(out, cmp)
	}
	return out
}

// DeleteWhere removes the rows matching match and returns how many.
func (t *Table[K, V]) DeleteWhere(match func(K, V) bool) int64 {
	var n int64
	for k, v := range t.Rows {
		if match(k, v) {
			delete(t.Rows, k)
			n++
		}
	}
	return n
}

// NotFound wraps kernel.ErrNotFound with what was looked up.
func NotFound(what string, key any) error {
	return fmt.Errorf("%s %v: %w", what, key, kernel.ErrNotFound)
}

// Conflict wraps kernel.ErrConflict with what collided.
func Conflict(what string, key any) error {
	return fmt.Errorf("%s %v: %w", what, key, kernel.ErrConflict)
}
