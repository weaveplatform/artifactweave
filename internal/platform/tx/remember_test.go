package tx_test

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

type counter struct{ calls int }

func (c *counter) next() (int, error) { c.calls++; return c.calls, nil }

func TestUnitRememberNeedsASession(t *testing.T) {
	var c counter
	for range 2 {
		if _, err := tx.Remember(t.Context(), false, "key", c.next); err != nil {
			t.Fatal(err)
		}
	}
	var f tx.Finalizers
	for range 2 {
		if _, err := tx.Remember(f.Bind(t.Context()), true, "key", c.next); err != nil {
			t.Fatal(err)
		}
	}
	if c.calls != 4 {
		t.Fatalf("remembered without a session: %d calls", c.calls)
	}
}

func TestUnitRememberKeepsSessionResultsOutsideTransactions(t *testing.T) {
	ctx := tx.WithSession(t.Context())
	if tx.WithSession(ctx) != ctx {
		t.Fatal("a nested session replaced the outer one")
	}
	var c counter
	first, _ := tx.Remember(ctx, false, "key", c.next)
	again, _ := tx.Remember(context.WithoutCancel(ctx), false, "key", c.next)
	other, _ := tx.Remember(ctx, false, "other", c.next)
	if first != 1 || again != 1 || other != 2 {
		t.Fatalf("session memo = %d %d %d", first, again, other)
	}
	if fresh, _ := tx.Remember(tx.WithSession(t.Context()), false, "key", c.next); fresh != 3 {
		t.Fatal("a separate session shared a result")
	}
}

func TestUnitRememberKeepsTransactionResultsUntilClose(t *testing.T) {
	session := tx.WithSession(t.Context())
	var f, other tx.Finalizers
	ctx := f.Bind(session)
	var c counter
	first, _ := tx.Remember(ctx, true, "key", c.next)
	again, _ := tx.Remember(ctx, true, "key", c.next)
	if first != 1 || again != 1 {
		t.Fatalf("transaction memo = %d %d", first, again)
	}
	if outside, _ := tx.Remember(session, false, "key", c.next); outside != 2 {
		t.Fatal("a transaction result leaked into the session")
	}
	if next, _ := tx.Remember(other.Bind(session), true, "key", c.next); next != 3 {
		t.Fatal("another transaction shared a result")
	}
	if missing, _ := tx.Remember(session, true, "key", c.next); missing != 4 {
		t.Fatal("a transaction memo without its transaction kept a result")
	}
	f.Close()
	if closed, _ := tx.Remember(ctx, true, "key", c.next); closed != 5 {
		t.Fatal("a closed transaction kept a result")
	}
	if closed, _ := tx.Remember(ctx, true, "key", c.next); closed != 6 {
		t.Fatal("a closed transaction kept a later result")
	}
}

func TestUnitRememberNeverKeepsFailures(t *testing.T) {
	ctx := tx.WithSession(t.Context())
	failure := errors.New("unavailable")
	calls := 0
	fail := func() (int, error) { calls++; return 0, failure }
	for range 2 {
		if _, err := tx.Remember(ctx, false, "key", fail); !errors.Is(err, failure) {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatal("a failure was kept")
	}
	var c counter
	if value, _ := tx.Remember(ctx, false, "key", c.next); value != 1 {
		t.Fatal("a failure shadowed a later success")
	}
}
