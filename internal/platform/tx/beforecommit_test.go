package tx_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

func TestUnitBeforeCommitRequiresOpenTransaction(t *testing.T) {
	ran := false
	callback := func(context.Context) error { ran = true; return nil }
	if err := tx.BeforeCommit(t.Context(), callback); !errors.Is(err, tx.ErrBeforeCommit) {
		t.Fatalf("unbound registration = %v", err)
	}
	var f tx.Finalizers
	ctx := f.Bind(t.Context())
	f.Close()
	f.Close()
	if err := tx.BeforeCommit(ctx, callback); !errors.Is(err, tx.ErrBeforeCommit) {
		t.Fatalf("closed registration = %v", err)
	}
	if err := f.Run(ctx); !errors.Is(err, tx.ErrBeforeCommit) {
		t.Fatalf("closed finalization = %v", err)
	}
	if ran {
		t.Error("unbound or closed registration ran immediately")
	}
}

func TestUnitBeforeCommitOrderAndOwningContext(t *testing.T) {
	var f, other tx.Finalizers
	ctx := f.Bind(t.Context())
	var order []int
	for i := range 3 {
		if err := tx.BeforeCommit(ctx, func(ctx context.Context) error {
			order = append(order, i)
			// Run must bind f even when a nested store shadowed the registrar.
			if err := tx.BeforeCommit(ctx, func(context.Context) error { return nil }); !errors.Is(err, tx.ErrBeforeCommit) {
				t.Errorf("reentrant registration = %v", err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(order) != 0 {
		t.Fatal("registration executed a callback")
	}
	if err := f.Run(other.Bind(ctx)); !errors.Is(err, tx.ErrBeforeCommit) {
		t.Fatalf("dropped reentrant error did not fail finalization: %v", err)
	}
	if !slices.Equal(order, []int{0}) {
		t.Errorf("callbacks after failure = %v; want only first", order)
	}
	if err := other.Run(ctx); err != nil {
		t.Fatalf("another store's queue was poisoned: %v", err)
	}
}

func TestUnitBeforeCommitStopsOnFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			var f tx.Finalizers
			ctx := f.Bind(t.Context())
			marker := errors.New("append failed")
			var order []int
			for i := range 3 {
				if err := tx.BeforeCommit(ctx, func(context.Context) error {
					order = append(order, i)
					if fail && i == 1 {
						return marker
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			err := f.Run(ctx)
			want := []int{0, 1, 2}
			if fail {
				want = []int{0, 1}
				if !errors.Is(err, marker) {
					t.Fatalf("finalization = %v; want callback error", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(order, want) {
				t.Errorf("callback order = %v; want %v", order, want)
			}
			if err := f.Run(ctx); !errors.Is(err, tx.ErrBeforeCommit) {
				t.Errorf("second finalization = %v", err)
			}
			if err := tx.BeforeCommit(ctx, func(context.Context) error { return nil }); !errors.Is(err, tx.ErrBeforeCommit) {
				t.Errorf("stale context registration = %v", err)
			}
		})
	}
}

func TestUnitBeforeCommitRejectsNilCallback(t *testing.T) {
	var f tx.Finalizers
	ctx := f.Bind(t.Context())
	if err := tx.BeforeCommit(ctx, nil); !errors.Is(err, tx.ErrBeforeCommit) {
		t.Fatalf("nil callback registration = %v", err)
	}
	if err := f.Run(ctx); !errors.Is(err, tx.ErrBeforeCommit) {
		t.Fatalf("dropped nil callback error = %v", err)
	}
}

func TestUnitBeforeCommitPanicClosesQueue(t *testing.T) {
	var f tx.Finalizers
	ctx := f.Bind(t.Context())
	marker := errors.New("callback panic")
	if err := tx.BeforeCommit(ctx, func(context.Context) error { panic(marker) }); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if got := recover(); got != marker {
				t.Errorf("panic = %v; want original marker", got)
			}
		}()
		if err := f.Run(ctx); err != nil {
			t.Error(err)
		}
	}()
	if err := tx.BeforeCommit(ctx, func(context.Context) error { return nil }); !errors.Is(err, tx.ErrBeforeCommit) {
		t.Fatalf("registration after panic = %v", err)
	}
}
