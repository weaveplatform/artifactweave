package dialect_test

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	platformtx "github.com/weaveplatform/artifactweave/internal/platform/tx"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

const itemsTable = "CREATE TABLE items (name TEXT PRIMARY KEY)"

func TestAccBeforeCommitRechecksDroppedInnerFailure(t *testing.T) {
	db := openSQLite(t)
	mustExec(t, db, itemsTable)
	s := dialect.New(db, sqliteDialect, nil)
	marker := errors.New("nested finalizer failure")
	var ran bool
	err := s.Tx(t.Context(), func(ctx context.Context) error {
		insert(ctx, t, s, "business")
		s.AfterCommit(ctx, func() { ran = true })
		return platformtx.BeforeCommit(ctx, func(ctx context.Context) error {
			inner := s.Tx(ctx, func(ctx context.Context) error {
				insert(ctx, t, s, "finalizer")
				return marker
			})
			if !errors.Is(inner, marker) {
				t.Errorf("inner transaction = %v; want marker", inner)
			}
			return nil
		})
	})
	if !errors.Is(err, marker) || ran || count(t, db, "items") != 0 {
		t.Fatalf("dropped finalizer error committed: error=%v, AfterCommit=%t", err, ran)
	}
}

func TestAccBeforeCommitShadowsAnotherPool(t *testing.T) {
	aDB, bDB := openSQLite(t), openSQLite(t)
	a := dialect.New(aDB, sqliteDialect, nil)
	b := dialect.New(bDB, sqliteDialect, nil)
	marker := errors.New("outer rollback")
	var aRan, bRan bool
	err := a.Tx(t.Context(), func(ctx context.Context) error {
		if err := platformtx.BeforeCommit(ctx, func(context.Context) error { aRan = true; return nil }); err != nil {
			return err
		}
		if err := b.Tx(ctx, func(ctx context.Context) error {
			return platformtx.BeforeCommit(ctx, func(context.Context) error { bRan = true; return nil })
		}); err != nil {
			return err
		}
		if !bRan || aRan {
			t.Error("independent pool did not finalize its own queue")
		}
		return marker
	})
	if !errors.Is(err, marker) || aRan || !bRan {
		t.Fatalf("pool finalization/rollback: error=%v, A=%t, B=%t", err, aRan, bRan)
	}
}

func TestAccBeforeCommitRejoinsSQLThroughMemory(t *testing.T) {
	s := dialect.New(openSQLite(t), sqliteDialect, nil)
	m := memtx.New()
	var memoryFinished, sqlRan bool
	if err := s.Tx(t.Context(), func(ctx context.Context) error {
		if err := m.Tx(ctx, func(ctx context.Context) error {
			return s.Tx(ctx, func(ctx context.Context) error {
				return platformtx.BeforeCommit(ctx, func(context.Context) error {
					sqlRan = true
					if !memoryFinished {
						t.Error("rejoined SQL registered its callback in the memory transaction")
					}
					return nil
				})
			})
		}); err != nil {
			return err
		}
		memoryFinished = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !sqlRan {
		t.Fatal("SQL finalizer did not run")
	}
}

// insert writes an item through s, inside the transaction ctx carries for
// s's pool, if any.
func insert(ctx context.Context, t *testing.T, s *dialect.Store, name string) {
	t.Helper()
	if _, err := s.Exec(ctx, "insert", "INSERT INTO items (name) VALUES (?)", name); err != nil {
		t.Fatal(err)
	}
}

// The SQL unit of work differs from memtx here on purpose (memtx's
// TestUnitTxNestedJoinsOuter, and tx.Runner): a nested Tx joins the outer
// transaction, and an error it returns fails the outer one even when the
// outer function drops it, so a caller that ignores an error cannot commit
// half of a change. Nothing either queued with AfterCommit runs.
func TestAccTxDroppedInnerErrorFailsOuter(t *testing.T) {
	ctx := context.Background()
	db := openSQLite(t)
	mustExec(t, db, itemsTable)
	s := dialect.New(db, sqliteDialect, nil)
	errInner := errors.New("inner")
	ran := false
	err := s.Tx(ctx, func(ctx context.Context) error {
		insert(ctx, t, s, "outer")
		s.AfterCommit(ctx, func() { ran = true })
		inner := s.Tx(ctx, func(ctx context.Context) error {
			if !s.InTx(ctx) {
				t.Error("the nested Tx did not run inside the outer one")
			}
			insert(ctx, t, s, "inner")
			s.AfterCommit(ctx, func() { ran = true })
			return errInner
		})
		if !errors.Is(inner, errInner) {
			t.Errorf("inner Tx = %v; want %v", inner, errInner)
		}
		return nil
	})
	if !errors.Is(err, errInner) {
		t.Fatalf("Tx = %v; want the inner error the outer function dropped", err)
	}
	if ran {
		t.Error("AfterCommit ran for a transaction that rolled back")
	}
	if n := count(t, db, "items"); n != 0 {
		t.Errorf("%d rows survived the failed transaction", n)
	}
}

// The transaction a context carries belongs to one *sql.DB. Every store
// built over that pool joins it, which is what lets several stores share one
// unit of work, and a store over another pool runs outside it.
func TestAccTxJoinsTheTransactionOfItsPool(t *testing.T) {
	ctx := context.Background()
	db, otherDB := openSQLite(t), openSQLite(t)
	mustExec(t, db, itemsTable)
	mustExec(t, otherDB, itemsTable)
	s, same := dialect.New(db, sqliteDialect, nil), dialect.New(db, sqliteDialect, nil)
	other := dialect.New(otherDB, sqliteDialect, nil)
	errRollBack := errors.New("roll back")
	err := s.Tx(ctx, func(ctx context.Context) error {
		if !s.InTx(ctx) || !same.InTx(ctx) {
			t.Error("a store over the transaction's pool is outside it")
		}
		if other.InTx(ctx) {
			t.Error("a store over another pool is inside the transaction")
		}
		insert(ctx, t, same, "joined")
		if err := same.Tx(ctx, func(ctx context.Context) error {
			insert(ctx, t, same, "nested")
			return nil
		}); err != nil {
			t.Error(err)
		}
		insert(ctx, t, other, "outside")
		return errRollBack
	})
	if !errors.Is(err, errRollBack) {
		t.Fatalf("Tx = %v; want %v", err, errRollBack)
	}
	if n := count(t, db, "items"); n != 0 {
		t.Errorf("%d rows written over the transaction's pool survived its rollback", n)
	}
	if n := count(t, otherDB, "items"); n != 1 {
		t.Errorf("another pool holds %d rows; want the 1 written outside the transaction", n)
	}
}

// A commit that fails fails Tx with ErrTransaction, and nothing queued with
// AfterCommit runs, since nothing was stored. SQLite cannot be made to fail a
// commit on demand, so the fault driver does.
func TestUnitTxReportsCommitFailure(t *testing.T) {
	s := dialect.New(faultDB(t, faults{commit: true}), dialect.Dialect{Name: "fake"}, nil)
	ran, finalized := false, false
	var stale context.Context
	err := s.Tx(context.Background(), func(ctx context.Context) error {
		stale = ctx
		s.AfterCommit(ctx, func() { ran = true })
		return platformtx.BeforeCommit(ctx, func(context.Context) error { finalized = true; return nil })
	})
	if !errors.Is(err, dialect.ErrTransaction) || !errors.Is(err, errFault) {
		t.Errorf("Tx = %v; want ErrTransaction and the commit's error", err)
	}
	if want := "sqlcommon: transaction failed: commit: fault"; err == nil || err.Error() != want {
		t.Errorf("Tx = %v; want %q", err, want)
	}
	if ran {
		t.Error("AfterCommit ran for a transaction that did not commit")
	}
	if !finalized {
		t.Error("commit was attempted without finalizing its required work")
	}
	if err := platformtx.BeforeCommit(stale, func(context.Context) error { return nil }); !errors.Is(err, platformtx.ErrBeforeCommit) {
		t.Errorf("registration after failed commit = %v", err)
	}
}
