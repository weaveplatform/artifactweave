package memtx_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	platformtx "github.com/weaveplatform/artifactweave/internal/platform/tx"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

func TestUnitMiddlewareWrapsRootBeforeFinalizers(t *testing.T) {
	var order []string
	var s *memtx.Store
	s = memtx.NewWithMiddleware(func(ctx context.Context, next func(context.Context) error) error {
		if !s.InTx(ctx) {
			t.Fatal("middleware ran outside rollback protection")
		}
		order = append(order, "guard")
		items(s).Put("guarded", 1)
		if err := platformtx.SetPublicationCheck(ctx, func(context.Context) error { order = append(order, "settle"); return nil }); err != nil {
			return err
		}
		if err := next(ctx); err != nil {
			return err
		}
		return platformtx.CheckPublication(ctx)
	})
	err := s.Tx(t.Context(), func(ctx context.Context) error {
		order = append(order, "business")
		if err := s.Tx(ctx, func(context.Context) error { order = append(order, "joined"); return nil }); err != nil {
			return err
		}
		s.AfterCommit(ctx, func() { order = append(order, "committed") })
		return platformtx.BeforeCommit(ctx, func(context.Context) error { order = append(order, "event"); return nil })
	})
	if err != nil || !slices.Equal(order, []string{"guard", "business", "joined", "settle", "event", "committed"}) {
		t.Fatal("root middleware order or nested multiplicity changed", order, err)
	}
}

func TestUnitMiddlewareRollbackIncludesItsOwnWrites(t *testing.T) {
	for _, failure := range []string{"guard", "business", "settlement", "finalizer", "panic"} {
		t.Run(failure, func(t *testing.T) {
			var s *memtx.Store
			committed := false
			s = memtx.NewWithMiddleware(func(ctx context.Context, next func(context.Context) error) error {
				items(s).Put("middleware", 1)
				if failure == "guard" {
					return errBoom
				}
				if err := next(ctx); err != nil {
					return err
				}
				if failure == "panic" {
					panic(errBoom)
				}
				if failure == "settlement" {
					return errBoom
				}
				return nil
			})
			var err error
			var panicked any
			func() {
				defer func() { panicked = recover() }()
				err = s.Tx(t.Context(), func(ctx context.Context) error {
					items(s).Put("business", 2)
					s.AfterCommit(ctx, func() { committed = true })
					if failure == "business" {
						return errBoom
					}
					return platformtx.BeforeCommit(ctx, func(context.Context) error {
						if failure == "finalizer" {
							return errBoom
						}
						return nil
					})
				})
			}()
			if failure == "panic" && panicked != errBoom || failure != "panic" && !errors.Is(err, errBoom) || committed {
				t.Fatal("middleware failure did not propagate/rollback", panicked, err, committed)
			}
			mustDo(t, s, func() error {
				if len(items(s).Rows) != 0 {
					t.Fatal("middleware or business writes survived rollback")
				}
				return nil
			})
		})
	}
}

func TestUnitPublicationChecksRejoinTheirOwningTransaction(t *testing.T) {
	var calls []string
	newStore := func(name string) *memtx.Store {
		return memtx.NewWithMiddleware(func(ctx context.Context, next func(context.Context) error) error {
			if err := platformtx.SetPublicationCheck(ctx, func(context.Context) error { calls = append(calls, name); return nil }); err != nil {
				return err
			}
			if err := next(ctx); err != nil {
				return err
			}
			return platformtx.CheckPublication(ctx)
		})
	}
	a, b := newStore("a"), newStore("b")
	err := a.Tx(t.Context(), func(ctx context.Context) error {
		return b.Tx(ctx, func(ctx context.Context) error {
			return a.Tx(ctx, platformtx.CheckPublication)
		})
	})
	if err != nil || !slices.Equal(calls, []string{"a", "b", "a"}) {
		t.Fatal("a nested store shadowed publication settlement", calls, err)
	}
}
