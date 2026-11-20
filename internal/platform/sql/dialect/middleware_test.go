package dialect_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	platformtx "github.com/weaveplatform/artifactweave/internal/platform/tx"
)

func TestAccMiddlewareWrapsRootAndRollsBack(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[failure], func(t *testing.T) {
			db := openSQLite(t)
			mustExec(t, db, itemsTable)
			marker := errors.New("quota settlement refused")
			var order []string
			var s *dialect.Store
			s = dialect.NewWithMiddleware(db, sqliteDialect, nil, func(ctx context.Context, next func(context.Context) error) error {
				if !s.InTx(ctx) {
					t.Fatal("middleware lacks the actual transaction")
				}
				order = append(order, "guard")
				insert(ctx, t, s, "middleware")
				if err := next(ctx); err != nil {
					return err
				}
				order = append(order, "settle")
				if failure {
					return marker
				}
				return nil
			})
			err := s.Tx(t.Context(), func(ctx context.Context) error {
				order = append(order, "business")
				insert(ctx, t, s, "business")
				if err := s.Tx(ctx, func(context.Context) error { order = append(order, "joined"); return nil }); err != nil {
					return err
				}
				s.AfterCommit(ctx, func() { order = append(order, "committed") })
				return platformtx.BeforeCommit(ctx, func(context.Context) error { order = append(order, "event"); return nil })
			})
			want := []string{"guard", "business", "joined", "settle"}
			if failure {
				if !errors.Is(err, marker) || count(t, db, "items") != 0 {
					t.Fatal("settlement failure left transaction effects", err)
				}
			} else {
				want = append(want, "event", "committed")
				if err != nil || count(t, db, "items") != 2 {
					t.Fatal("middleware/business transaction did not commit", err)
				}
			}
			if !slices.Equal(order, want) {
				t.Fatal("root/join/finalizer order changed", order, want)
			}
		})
	}
}

func TestAccMiddlewareRejectsUninstrumentedRoot(t *testing.T) {
	db := openSQLite(t)
	mustExec(t, db, itemsTable)
	raw := dialect.New(db, sqliteDialect, nil)
	calls := 0
	s := dialect.NewWithMiddleware(db, sqliteDialect, nil, func(ctx context.Context, next func(context.Context) error) error { calls++; return next(ctx) })
	err := raw.Tx(t.Context(), func(ctx context.Context) error {
		insert(ctx, t, raw, "raw")
		inner := s.Tx(ctx, func(ctx context.Context) error { insert(ctx, t, s, "bypassed"); return nil })
		if !errors.Is(inner, platformtx.ErrMiddleware) {
			t.Fatal("a separate pool wrapper bypassed root middleware", inner)
		}
		return nil // SQL must poison the outer transaction even when dropped.
	})
	if !errors.Is(err, platformtx.ErrMiddleware) || calls != 0 || count(t, db, "items") != 0 {
		t.Fatal("uninstrumented publication committed", err, calls)
	}
}
