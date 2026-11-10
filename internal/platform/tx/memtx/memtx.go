package memtx

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Store is one in-memory database: named tables and sequences behind one
// gate, and the transactions that every in-memory store built over it
// shares. It implements tx.Runner and is safe for concurrent use. The zero
// value is not usable; call New.
type Store struct {
	gate   chan struct{}
	tables map[string]snapshotter
	seqs   map[string]int64
}

// New returns an empty database.
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

// AfterCommit implements tx.Runner. It defers fn until the Tx that ctx is
// inside commits, or runs fn at once when ctx is outside a transaction of
// this store. Deferred functions run in order once the transaction has
// committed and given up the gate, so they may use the store; none runs
// after a rollback.
func (s *Store) AfterCommit(ctx context.Context, fn func()) {
	if tx, ok := ctx.Value(txKey{s}).(*transaction); ok {
		tx.after = append(tx.after, fn)
		return
	}
	fn()
}

// Do runs fn with the gate held and returns fn's error, so fn may read and
// write tables and sequences. When ctx is inside this store's Tx, fn runs at
// once under the transaction's hold, and its writes commit or roll back with
// the transaction. Outside one, Do is not a transaction: it waits for the
// gate, and what fn wrote stays even when fn returns an error, so fn checks
// before it writes. Do fails without running fn when ctx has ended, or ends
// while Do waits, with an error that wraps ctx.Err() and reads
// "inmem: <error>".
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

// InTx reports whether ctx is inside this store's Tx, that is whether Do
// and Tx called with it join that transaction. A context inside another
// Store's Tx is not inside this one's. Stores use it to refuse a call that is
// only valid in a transaction, such as a read for update.
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

// Tx implements tx.Runner. It takes the gate for the whole of fn, saves a
// snapshot of every table and sequence, and passes fn a context that Do,
// Tx, AfterCommit and InTx recognise. When fn returns an error, Tx restores
// the snapshot and returns that error unchanged; otherwise fn's writes stand
// and the deferred functions run. Outside a transaction, Tx fails without
// running fn when ctx has ended, or ends while Tx waits for the gate, with an
// error that wraps ctx.Err() and reads "inmem: <error>". Called with a
// context already inside this store's Tx, it runs fn in that transaction at
// once: the outer Tx commits or rolls back both, and an inner error that the
// outer fn drops rolls nothing back, unlike the SQL unit of work (see
// tx.Runner).
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

// Close does nothing and returns nil: the database lives as long as the
// Store. It gives in-memory stores the Close of the metadata store's
// lifetime contract.
func (*Store) Close() error { return nil }

// NextID returns the next number of the named sequence, starting at 1. Call
// it inside Do or Tx; a Tx that rolls back gives its numbers back.
func (s *Store) NextID(seq string) int64 {
	s.seqs[seq]++
	return s.seqs[seq]
}

// snapshotter is a table that can copy itself for rollback.
type snapshotter interface {
	snapshot() snapshotter
}

// Table is one in-memory table of rows of type V keyed by K. Get, All and
// Put copy rows on the way out and in, so callers never share memory with
// the store, and Put normalises times and durations to what the SQL columns
// keep. Stored values are private copies that are replaced, never mutated,
// so a snapshot only needs to copy the map. Call its methods inside Do or
// Tx, on a table that TableOf has just returned.
type Table[K comparable, V any] struct {
	// Rows holds the table's rows. Code that reads or writes it directly
	// holds the gate, gets no copies and no normalisation, and replaces a
	// stored value rather than changing it in place: a snapshot shares the
	// values, so a change in place would survive a rollback. Stores use it
	// to range over rows, to store a value that kernel.Normalise cannot copy
	// (one with unexported fields), and to delete while they range.
	Rows map[K]V
}

func (t *Table[K, V]) snapshot() snapshotter {
	return &Table[K, V]{Rows: maps.Clone(t.Rows)}
}

// TableOf returns the table name of s, creating it empty on first use. Call
// it inside Do or Tx, and again for each use: a Tx that rolls back replaces
// every table with its snapshot, so a *Table kept from before is stale. A
// name must always be used with the same key and value types; TableOf
// panics on another pair.
func TableOf[K comparable, V any](s *Store, name string) *Table[K, V] {
	if t, ok := s.tables[name]; ok {
		return t.(*Table[K, V]) //nolint:forcetypeassert // one type per name by construction
	}
	t := &Table[K, V]{Rows: map[K]V{}}
	s.tables[name] = t
	return t
}

// Get returns a copy of the row at k and true, or the zero value and false
// when there is none.
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

// Put stores a normalised copy of v at k, replacing any row there.
func (t *Table[K, V]) Put(k K, v V) { t.Rows[k] = kernel.Normalise(v) }

// Del removes the row at k and reports whether there was one.
func (t *Table[K, V]) Del(k K) bool {
	_, ok := t.Rows[k]
	delete(t.Rows, k)
	return ok
}

// All returns copies of the rows matching keep (every row when nil),
// sorted by cmp (in no set order when cmp is nil).
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

// NotFound returns the error for a missing record, "<what> <key>: " and
// kernel.ErrNotFound's text, wrapping kernel.ErrNotFound.
func NotFound(what string, key any) error {
	return fmt.Errorf("%s %v: %w", what, key, kernel.ErrNotFound)
}

// Conflict returns the error for a record that collides with one already
// stored, "<what> <key>: " and kernel.ErrConflict's text, wrapping
// kernel.ErrConflict.
func Conflict(what string, key any) error {
	return fmt.Errorf("%s %v: %w", what, key, kernel.ErrConflict)
}
