package memtx

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync/atomic"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	platformtx "github.com/weaveplatform/artifactweave/internal/platform/tx"
)

// Store is one in-memory database: named tables and sequences behind one
// gate, and the transactions that every in-memory store built over it
// shares. It implements tx.Runner and is safe for concurrent use. The zero
// value is not usable; call New.
type Store struct {
	gate       chan struct{}
	tables     map[string]snapshotter
	seqs       map[string]int64
	middleware platformtx.Middleware
}

// New returns an empty database.
func New() *Store {
	return &Store{
		gate:   make(chan struct{}, 1),
		tables: map[string]snapshotter{},
		seqs:   map[string]int64{},
	}
}

// NewWithMiddleware constructs a database whose new root transactions surround
// ordinary work with middleware. It runs after snapshot/binding and before
// finalizers, so its own writes roll back with business work. A nil middleware
// preserves New's behavior; every module adapter shares the returned backend.
func NewWithMiddleware(middleware platformtx.Middleware) *Store {
	s := New()
	s.middleware = middleware
	return s
}

type (
	txKey       struct{ s *Store }
	transaction struct {
		after  []func()
		before platformtx.Finalizers
	}
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
// Tx, AfterCommit and InTx recognise. When fn returns an error or panics,
// Tx restores the snapshot before releasing the gate, preserving the error
// or panic unchanged; otherwise fn's writes stand and the deferred functions
// run. A panic recovered inside fn does not roll back the transaction.
// Outside a transaction, Tx fails without running fn when ctx has ended,
// or ends while Tx waits for the gate, with an error that wraps ctx.Err()
// and reads "inmem: <error>". Called with a
// context already inside this store's Tx, it runs fn in that transaction at
// once: the outer Tx commits or rolls back both, and an inner error that the
// outer fn drops rolls nothing back, unlike the SQL unit of work (see
// tx.Runner).
func (s *Store) Tx(ctx context.Context, fn func(context.Context) error) error {
	if current, ok := ctx.Value(txKey{s}).(*transaction); ok {
		return fn(current.before.Bind(ctx))
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
	defer tx.before.Close()
	tables := make(map[string]snapshotter, len(s.tables))
	for name, t := range s.tables {
		tables[name] = t.snapshot()
	}
	seqs := maps.Clone(s.seqs)
	// Restore before the earlier gate-release defer lets another caller in.
	defer func() {
		if !committed {
			s.tables, s.seqs = tables, seqs
		}
	}()
	ctx = tx.before.Bind(context.WithValue(ctx, txKey{s}, tx))
	var err error
	if s.middleware != nil {
		err = s.middleware(ctx, fn)
	} else {
		err = fn(ctx)
	}
	if err == nil {
		if err := tx.before.Run(ctx); err != nil {
			return fmt.Errorf("inmem: finalize transaction: %w", err)
		}
	}
	if err != nil {
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

// Table is one in-memory table of rows of type V keyed by K. Get, All,
// First, Page and Put copy rows on the way out and in, so callers never share memory with
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
	return copyRow(v), true
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
// sorted by cmp (in no set order when cmp is nil). It copies every match;
// a list that returns only some of them uses Page or First instead.
func (t *Table[K, V]) All(keep func(K, V) bool, cmp func(a, b V) int) []V {
	return copyRows(t.Sorted(keep, cmp))
}

// Sorted returns the rows matching keep (every row when nil), sorted by cmp
// (in no set order when cmp is nil). They are the stored values, not
// copies, like Rows: read them without changing them, and copy whatever
// part of them a caller keeps. keep and cmp see the stored values too and
// must not change them. A store uses Sorted when it returns something
// derived from the rows, or only some of them in a way Page and First do not
// cover, so that it copies only what it returns.
func (t *Table[K, V]) Sorted(keep func(K, V) bool, cmp func(a, b V) int) []V {
	out := make([]V, 0, len(t.Rows))
	for k, v := range t.Rows {
		if keep == nil || keep(k, v) {
			out = append(out, v)
		}
	}
	if cmp != nil {
		slices.SortFunc(out, cmp)
	}
	return out
}

// First returns copies of at most n of the rows matching keep, the first in
// cmp's order, as All would list them. It copies only those it returns.
func (t *Table[K, V]) First(keep func(K, V) bool, cmp func(a, b V) int, n int) []V {
	rows := t.Sorted(keep, cmp)
	return copyRows(rows[:max(0, min(n, len(rows)))])
}

// Page returns copies of one page of the rows matching keep, sorted by cmp
// and paged by key as Paginate pages them (cmp must order rows as key does,
// ascending or, when desc, descending), with the next page's cursor and
// Paginate's errors. It is All and Paginate in one, but it copies only the
// rows of the page, not every match: a caller reading a long list a small
// page at a time would otherwise copy the whole list for every page.
func (t *Table[K, V]) Page(
	keep func(K, V) bool,
	cmp func(a, b V) int,
	p kernel.Page,
	desc bool,
	key func(V) []any,
) ([]V, string, error) {
	rows, next, err := Paginate(t.Sorted(keep, cmp), p, desc, key)
	if err != nil {
		return nil, "", err
	}
	return copyRows(rows), next, nil
}

// copied counts the rows Get, All, First and Page have copied out, so that
// a test can check a page copies no more than its own rows.
var copied atomic.Int64

func copyRow[V any](v V) V {
	copied.Add(1)
	return kernel.Clone(v)
}

// copyRows returns copies of rows in a new slice, nil for nil and empty
// for empty as All and Paginate have always answered, so the result shares
// neither the stored values nor the backing array of the sorted matches.
func copyRows[V any](rows []V) []V {
	if rows == nil {
		return nil
	}
	out := make([]V, len(rows))
	for i, v := range rows {
		out[i] = copyRow(v)
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
