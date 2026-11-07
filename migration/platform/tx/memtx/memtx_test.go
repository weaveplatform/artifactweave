package memtx_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
)

var errBoom = errors.New("boom")

// rec is a row with the kinds of value the store copies and normalises.
type rec struct {
	At    time.Time
	Took  time.Duration
	Tags  []string
	Attrs map[string]string
}

func items(s *memtx.Store) *memtx.Table[string, int] {
	return memtx.TableOf[string, int](s, "items")
}

// lateCancel reports no error to the first Err call and context.Canceled to
// every later one: the store has checked the context and found it live, and
// it is cancelled by the time the store looks again at the gate. done is
// what Done returns: nil never fires, a closed channel has fired.
type lateCancel struct {
	context.Context
	done  chan struct{}
	calls int
}

func newLateCancel(done chan struct{}) *lateCancel {
	return &lateCancel{Context: context.Background(), done: done}
}

func (c *lateCancel) Done() <-chan struct{} { return c.done }

func (c *lateCancel) Err() error {
	c.calls++
	if c.calls == 1 {
		return nil
	}
	return context.Canceled
}

func mustDo(t *testing.T, s *memtx.Store, fn func() error) {
	t.Helper()
	if err := s.Do(context.Background(), fn); err != nil {
		t.Fatalf("Do: %v", err)
	}
}

func TestUnitTxCommit(t *testing.T) {
	s := memtx.New()
	ctx := context.Background()
	var id int64
	err := s.Tx(ctx, func(ctx context.Context) error {
		return s.Do(ctx, func() error {
			items(s).Put("a", 1)
			id = s.NextID("items")
			return nil
		})
	})
	if err != nil {
		t.Fatalf("Tx: %v", err)
	}
	mustDo(t, s, func() error {
		if got, ok := items(s).Get("a"); !ok || got != 1 {
			t.Errorf("Get(a) after commit = %d, %t; want 1, true", got, ok)
		}
		if next := s.NextID("items"); id != 1 || next != 2 {
			t.Errorf("sequence = %d then %d; want 1 then 2", id, next)
		}
		return nil
	})
}

func TestUnitTxRollbackRestoresTablesAndSequences(t *testing.T) {
	s := memtx.New()
	ctx := context.Background()
	var before *memtx.Table[string, int]
	mustDo(t, s, func() error {
		before = items(s)
		before.Put("kept", 1)
		before.Put("deleted", 2)
		s.NextID("items")
		return nil
	})
	err := s.Tx(ctx, func(ctx context.Context) error {
		return s.Do(ctx, func() error {
			tab := items(s)
			tab.Put("kept", 10)
			tab.Del("deleted")
			tab.Put("added", 3)
			memtx.TableOf[string, string](s, "created").Put("x", "y")
			s.NextID("items")
			s.NextID("other")
			return errBoom
		})
	})
	if err != errBoom {
		t.Fatalf("Tx = %v; want the callback's error unchanged", err)
	}
	mustDo(t, s, func() error {
		got := items(s).All(nil, func(a, b int) int { return a - b })
		if !slices.Equal(got, []int{1, 2}) {
			t.Errorf("rows after rollback = %v; want [1 2]", got)
		}
		if items(s).Has("added") {
			t.Error("a row written inside the rolled-back Tx survived")
		}
		if n := len(memtx.TableOf[string, string](s, "created").Rows); n != 0 {
			t.Errorf("a table created inside the rolled-back Tx kept %d rows", n)
		}
		if next := s.NextID("items"); next != 2 {
			t.Errorf("NextID(items) after rollback = %d; want 2", next)
		}
		if next := s.NextID("other"); next != 1 {
			t.Errorf("NextID(other) after rollback = %d; want 1", next)
		}
		// Rollback swaps in the snapshot, so a table fetched before the Tx is
		// no longer the store's: callers look tables up with TableOf on every
		// use and never keep one across a transaction.
		if items(s) == before {
			t.Error("the table fetched before the rollback is still the store's")
		}
		return nil
	})
}

func TestUnitTxNestedJoinsOuter(t *testing.T) {
	s := memtx.New()
	ctx := context.Background()

	// An inner error the outer function returns rolls back both.
	err := s.Tx(ctx, func(ctx context.Context) error {
		mustPut(ctx, t, s, "outer")
		return s.Tx(ctx, func(ctx context.Context) error {
			mustPut(ctx, t, s, "inner")
			return errBoom
		})
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("nested Tx = %v; want %v", err, errBoom)
	}
	mustDo(t, s, func() error {
		if n := len(items(s).Rows); n != 0 {
			t.Errorf("%d rows survived a rolled-back nested Tx", n)
		}
		return nil
	})

	// An inner error the outer function drops rolls nothing back here: the
	// inner Tx is part of the outer one, which commits. The SQL unit of
	// work fails the outer transaction instead, on purpose, so the contract
	// suite checks only the case where the error is returned.
	ran := false
	err = s.Tx(ctx, func(ctx context.Context) error {
		mustPut(ctx, t, s, "outer")
		inner := s.Tx(ctx, func(ctx context.Context) error {
			if !s.InTx(ctx) {
				t.Error("the nested Tx did not run inside the outer one")
			}
			mustPut(ctx, t, s, "inner")
			s.AfterCommit(ctx, func() { ran = true })
			return errBoom
		})
		if !errors.Is(inner, errBoom) {
			t.Errorf("inner Tx = %v; want %v", inner, errBoom)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("outer Tx: %v", err)
	}
	mustDo(t, s, func() error {
		for _, k := range []string{"outer", "inner"} {
			if !items(s).Has(k) {
				t.Errorf("row %q did not commit with the outer Tx", k)
			}
		}
		return nil
	})
	if !ran {
		t.Error("AfterCommit from the nested Tx did not run after the outer commit")
	}
}

func mustPut(ctx context.Context, t *testing.T, s *memtx.Store, k string) {
	t.Helper()
	if err := s.Do(ctx, func() error { items(s).Put(k, 1); return nil }); err != nil {
		t.Fatalf("Do: %v", err)
	}
}

func TestUnitAfterCommit(t *testing.T) {
	s := memtx.New()
	ctx := context.Background()

	var order []int
	err := s.Tx(ctx, func(ctx context.Context) error {
		for i := 1; i <= 3; i++ {
			s.AfterCommit(ctx, func() { order = append(order, i) })
		}
		if len(order) != 0 {
			t.Errorf("AfterCommit ran %v before the commit", order)
		}
		// A deferred function may use the store: the gate is free by then.
		// The timeout turns a regression into a failure, not a hang.
		s.AfterCommit(ctx, func() {
			c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := s.Do(c, func() error { return nil }); err != nil {
				t.Errorf("Do from an AfterCommit function: %v", err)
			}
		})
		return nil
	})
	if err != nil {
		t.Fatalf("Tx: %v", err)
	}
	if !slices.Equal(order, []int{1, 2, 3}) {
		t.Errorf("AfterCommit order = %v; want [1 2 3]", order)
	}

	rolledBack := false
	_ = s.Tx(ctx, func(ctx context.Context) error {
		s.AfterCommit(ctx, func() { rolledBack = true })
		return errBoom
	})
	if rolledBack {
		t.Error("AfterCommit ran after a rollback")
	}

	now := false
	s.AfterCommit(ctx, func() { now = true })
	if !now {
		t.Error("AfterCommit outside a transaction did not run at once")
	}
}

func TestUnitCancelledContext(t *testing.T) {
	s := memtx.New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ran := false
	do := func() error { ran = true; return nil }
	tx := func(context.Context) error { ran = true; return nil }
	wantCancelled(t, "Tx", s.Tx(ctx, tx))
	wantCancelled(t, "Do", s.Do(ctx, do))

	// Cancelled after the first check: the gate is free, so the store takes
	// it, sees the cancellation and gives it back.
	wantCancelled(t, "Do, cancelled at the gate", s.Do(newLateCancel(nil), do))
	wantCancelled(t, "Tx, cancelled at the gate", s.Tx(newLateCancel(nil), tx))
	// Cancelled while waiting: another transaction holds the gate.
	closed := make(chan struct{})
	close(closed)
	err := s.Tx(context.Background(), func(context.Context) error {
		wantCancelled(t, "Do, waiting for the gate", s.Do(newLateCancel(closed), do))
		wantCancelled(t, "Tx, waiting for the gate", s.Tx(newLateCancel(closed), tx))
		return nil
	})
	if err != nil {
		t.Fatalf("Tx: %v", err)
	}
	if ran {
		t.Error("a function ran on a cancelled context")
	}
	mustDo(t, s, func() error { return nil }) // the gate is free again
}

func wantCancelled(t *testing.T, name string, err error) {
	t.Helper()
	if !errors.Is(err, context.Canceled) || err.Error() != "inmem: context canceled" {
		t.Errorf("%s = %v; want inmem: context canceled", name, err)
	}
}

func TestUnitDoInsideTx(t *testing.T) {
	s := memtx.New()
	done := make(chan error, 1)
	go func() {
		done <- s.Tx(context.Background(), func(ctx context.Context) error {
			return s.Do(ctx, func() error {
				items(s).Put("a", 1)
				return nil
			})
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Tx: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Do inside Tx waited for the gate its own transaction holds")
	}
}

func TestUnitInTx(t *testing.T) {
	a, b := memtx.New(), memtx.New()
	ctx := context.Background()
	if a.InTx(ctx) {
		t.Error("InTx is true outside a transaction")
	}
	err := a.Tx(ctx, func(ctx context.Context) error {
		if !a.InTx(ctx) {
			t.Error("InTx is false inside the store's Tx")
		}
		if b.InTx(ctx) {
			t.Error("another store's transaction counts as this store's")
		}
		return b.Tx(ctx, func(ctx context.Context) error {
			if !a.InTx(ctx) || !b.InTx(ctx) {
				t.Error("a context inside both transactions does not report both")
			}
			return nil
		})
	})
	if err != nil {
		t.Fatalf("Tx: %v", err)
	}
}

func TestUnitClose(t *testing.T) {
	s := memtx.New()
	mustDo(t, s, func() error { items(s).Put("a", 1); return nil })
	if err := s.Close(); err != nil {
		t.Errorf("Close = %v; want nil", err)
	}
	mustDo(t, s, func() error {
		if !items(s).Has("a") {
			t.Error("Close dropped the rows")
		}
		return nil
	})
}

func TestUnitTable(t *testing.T) {
	s := memtx.New()
	at := time.Date(2026, 11, 7, 10, 11, 12, 123456789, time.FixedZone("plus2", 2*3600))
	mustDo(t, s, func() error {
		tab := memtx.TableOf[string, rec](s, "recs")
		if memtx.TableOf[string, rec](s, "recs") != tab {
			t.Error("TableOf returned a new table for a name in use")
		}
		in := rec{
			At:    at,
			Took:  1500 * time.Microsecond,
			Tags:  []string{"a"},
			Attrs: map[string]string{"k": "v"},
		}
		tab.Put("r", in)
		in.Tags[0], in.Attrs["k"] = "caller", "caller"

		got, ok := tab.Get("r")
		switch {
		case !ok:
			t.Fatal("Get(r) found nothing")
		case !got.At.Equal(at.Truncate(time.Microsecond)) || got.At.Location() != time.UTC:
			t.Errorf("At = %v; want %v in UTC", got.At, at.Truncate(time.Microsecond))
		case got.Took != time.Millisecond:
			t.Errorf("Took = %v; want 1ms", got.Took)
		case got.Tags[0] != "a" || got.Attrs["k"] != "v":
			t.Errorf("Put kept the caller's memory: %v %v", got.Tags, got.Attrs)
		}
		got.Tags[0] = "mutated"
		if again, _ := tab.Get("r"); again.Tags[0] != "a" {
			t.Error("Get returned the stored memory, not a copy")
		}
		if _, ok := tab.Get("missing"); ok {
			t.Error("Get(missing) found a row")
		}

		tab.Put("s", rec{Tags: []string{"b"}})
		tab.Put("t", rec{Tags: []string{"c"}})
		byTag := func(x, y rec) int { return strings.Compare(x.Tags[0], y.Tags[0]) }
		if got := tags(tab.All(nil, byTag)); !slices.Equal(got, []string{"a", "b", "c"}) {
			t.Errorf("All(nil) = %v; want [a b c]", got)
		}
		notS := func(k string, _ rec) bool { return k != "s" }
		if got := tags(tab.All(notS, byTag)); !slices.Equal(got, []string{"a", "c"}) {
			t.Errorf("All(keep) = %v; want [a c]", got)
		}
		if n := len(tab.All(nil, nil)); n != 3 {
			t.Errorf("All(nil, nil) returned %d rows; want 3", n)
		}
		onlyR := func(k string, _ rec) bool { return k == "r" }
		tab.All(onlyR, nil)[0].Tags[0] = "mutated"
		if listed := tab.All(onlyR, nil); listed[0].Tags[0] != "a" {
			t.Error("All returned the stored memory, not a copy")
		}

		if !tab.Has("s") || tab.Has("missing") {
			t.Error("Has disagrees with the rows")
		}
		if !tab.Del("s") || tab.Del("s") {
			t.Error("Del did not report whether there was a row")
		}
		if n := tab.DeleteWhere(func(_ string, v rec) bool { return v.Tags[0] != "a" }); n != 1 {
			t.Errorf("DeleteWhere removed %d rows; want 1", n)
		}
		if n := len(tab.Rows); n != 1 {
			t.Errorf("%d rows left; want 1", n)
		}
		return nil
	})
}

func tags(rs []rec) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Tags[0]
	}
	return out
}

func TestUnitNextID(t *testing.T) {
	s := memtx.New()
	mustDo(t, s, func() error {
		got := []int64{s.NextID("a"), s.NextID("a"), s.NextID("b"), s.NextID("a")}
		if !slices.Equal(got, []int64{1, 2, 1, 3}) {
			t.Errorf("NextID = %v; want [1 2 1 3]", got)
		}
		return nil
	})
}

func TestUnitNotFoundAndConflict(t *testing.T) {
	for _, c := range []struct {
		err      error
		sentinel error
		text     string
	}{
		{memtx.NotFound("repository", "libs"), kernel.ErrNotFound, "repository libs: store: not found"},
		{memtx.Conflict("build run", int64(7)), kernel.ErrConflict, "build run 7: store: conflict"},
	} {
		if !errors.Is(c.err, c.sentinel) || c.err.Error() != c.text {
			t.Errorf("error = %q; want %q wrapping %v", c.err, c.text, c.sentinel)
		}
	}
}

// row is sorted by (name, id), with id compared as a number.
type row struct {
	name string
	id   int64
}

func rowKey(r row) []any { return []any{r.name, r.id} }

func TestUnitPaginateRoundTrip(t *testing.T) {
	// a/2 before a/10 checks that integer parts compare as numbers: as
	// strings "10" sorts before "2".
	asc := []row{{"a", 2}, {"a", 10}, {"b", 1}, {"b", 9}, {"c", 3}}
	desc := slices.Clone(asc)
	slices.Reverse(desc)
	for _, c := range []struct {
		name string
		rows []row
		desc bool
	}{{"ascending", asc, false}, {"descending", desc, true}} {
		for _, limit := range []int{1, 2, 5, 6} {
			var got []row
			p := kernel.Page{Limit: limit}
			for pages := 1; ; pages++ {
				page, next, err := memtx.Paginate(c.rows, p, c.desc, rowKey)
				if err != nil {
					t.Fatalf("%s, limit %d: Paginate: %v", c.name, limit, err)
				}
				if pages > len(c.rows)+1 {
					t.Fatalf("%s, limit %d: the cursors never end", c.name, limit)
				}
				got = append(got, page...)
				if next == "" {
					break
				}
				p.After = next
			}
			if !slices.Equal(got, c.rows) {
				t.Errorf("%s, limit %d: pages = %v; want %v", c.name, limit, got, c.rows)
			}
		}
	}

	page, next, err := memtx.Paginate([]row(nil), kernel.Page{}, false, rowKey)
	if page != nil || next != "" || err != nil {
		t.Errorf("Paginate(no rows) = %v, %q, %v; want nil, \"\", nil", page, next, err)
	}
	afterLast := kernel.Page{After: kernel.Cursor("c", "3")}
	page, next, err = memtx.Paginate(asc, afterLast, false, rowKey)
	if len(page) != 0 || next != "" || err != nil {
		t.Errorf("Paginate(after the last row) = %v, %q, %v; want an empty page", page, next, err)
	}

	for name, after := range map[string]string{
		"undecodable":         "%%%",
		"wrong part count":    kernel.Cursor("a"),
		"non-integer id part": kernel.Cursor("a", "x"),
	} {
		_, _, err := memtx.Paginate(asc, kernel.Page{After: after}, false, rowKey)
		if !errors.Is(err, kernel.ErrInvalidCursor) {
			t.Errorf("%s cursor: Paginate = %v; want %v", name, err, kernel.ErrInvalidCursor)
		}
	}
}

func TestUnitKeyStringsAndCompareKey(t *testing.T) {
	got := memtx.KeyStrings([]any{"a", int64(-3), 7})
	if !slices.Equal(got, []string{"a", "-3", "7"}) {
		t.Errorf("KeyStrings = %v; want [a -3 7]", got)
	}
	for _, c := range []struct {
		row    []any
		cursor []string
		want   int
	}{
		{[]any{"b", int64(10)}, []string{"b", "9"}, 1},
		{[]any{"a", int64(10)}, []string{"b", "1"}, -1},
		{[]any{"a", int64(2)}, []string{"a", "2"}, 0},
	} {
		got, err := memtx.CompareKey(c.row, c.cursor)
		if err != nil || got != c.want {
			t.Errorf("CompareKey(%v, %v) = %d, %v; want %d", c.row, c.cursor, got, err, c.want)
		}
	}
	const notInteger = `store: invalid cursor: "one" is not an integer`
	_, err := memtx.CompareKey([]any{int64(1)}, []string{"one"})
	if !errors.Is(err, kernel.ErrInvalidCursor) || err.Error() != notInteger {
		t.Errorf("CompareKey(non-integer) = %v; want the invalid cursor error", err)
	}
}
