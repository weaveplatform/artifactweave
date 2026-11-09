package storetest

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/apilimit"
	"github.com/weaveplatform/artifactweave/migration/platform/testkit/storekit"
)

func requestBudget(key string, limit int64) apilimit.RequestBudget {
	return apilimit.RequestBudget{Key: replayDigest(key), Limit: limit, Reset: storekit.At(1)}
}

func runRequestBudgets(t *testing.T, factory func(*testing.T) Store) {
	t.Run("RequestBudgets", func(t *testing.T) {
		s := factory(t)
		ctx := t.Context()
		r := apilimit.BudgetRequest{
			Now:     storekit.Base,
			MaxKeys: 3,
			Budgets: []apilimit.RequestBudget{
				requestBudget("global", 10), requestBudget("alice", 2),
			},
		}
		var allowed atomic.Int64
		var wg sync.WaitGroup
		for range 16 {
			wg.Go(func() {
				out, err := s.RequestBudgets().Take(ctx, r)
				if err != nil {
					t.Error(err)
				} else if out.Allowed {
					allowed.Add(1)
				}
			})
		}
		wg.Wait()
		if allowed.Load() != 2 {
			t.Fatalf("admitted %d requests to a 2-request quota", allowed.Load())
		}
		r.Budgets[1] = requestBudget("bob", 2)
		out := storekit.Get[apilimit.BudgetDecision](t)(s.RequestBudgets().Take(ctx, r))
		if !out.Allowed || out.Balances[0].Remaining != 7 || out.Balances[1].Remaining != 1 {
			t.Fatalf("one principal consumed another's budget: %+v", out)
		}
		r.Budgets[1] = requestBudget("charlie", 2)
		out, err := s.RequestBudgets().Take(ctx, r)
		storekit.WantErr(t, err, apilimit.ErrBudgetCapacity)
		if out.Allowed {
			t.Fatal("capacity failure granted admission")
		}
		r.Budgets = r.Budgets[:1]
		out = storekit.Get[apilimit.BudgetDecision](t)(s.RequestBudgets().Take(ctx, r))
		if out.Balances[0].Remaining != 6 {
			t.Fatalf("capacity failure charged global budget: %+v", out)
		}
		storekit.Must(t, func() error {
			err := s.Tx(ctx, func(ctx context.Context) error {
				if _, err := s.RequestBudgets().Take(ctx, r); err != nil {
					return err //nolint:wrapcheck // WantErr reports any error but ErrConflict
				}
				return kernel.ErrConflict
			})
			storekit.WantErr(t, err, kernel.ErrConflict)
			return nil
		}())
		out = storekit.Get[apilimit.BudgetDecision](t)(s.RequestBudgets().Take(ctx, r))
		if out.Balances[0].Remaining != 5 {
			t.Fatalf("rolled-back admission survived: %+v", out)
		}
		r.Now, r.Budgets[0].Reset = storekit.At(1), storekit.At(2)
		out = storekit.Get[apilimit.BudgetDecision](t)(s.RequestBudgets().Take(ctx, r))
		if !out.Allowed || out.Balances[0].Remaining != 9 {
			t.Fatalf("window did not reset: %+v", out)
		}
		// A late request from the old window cannot reset a newer counter.
		r.Now, r.Budgets[0].Reset = storekit.Base, storekit.At(1)
		out = storekit.Get[apilimit.BudgetDecision](t)(s.RequestBudgets().Take(ctx, r))
		if out.Balances[0].Remaining != 8 ||
			!out.Balances[0].Reset.Equal(kernel.Normalise(storekit.At(2))) {
			t.Fatalf("older clock reset the window: %+v", out)
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		_, err = s.RequestBudgets().Take(canceled, r)
		storekit.WantErr(t, err, context.Canceled)
		for _, bad := range []apilimit.BudgetRequest{
			{},
			{Now: storekit.Base, MaxKeys: 1},
			{Now: storekit.Base, MaxKeys: 0, Budgets: r.Budgets},
			{Now: storekit.Base, MaxKeys: 65537, Budgets: r.Budgets},
			{Now: storekit.Base, MaxKeys: 2, Budgets: []apilimit.RequestBudget{r.Budgets[0], r.Budgets[0]}},
			{Now: storekit.Base, MaxKeys: 1, Budgets: []apilimit.RequestBudget{{Key: "invalid", Limit: 1, Reset: storekit.At(1)}}},
			{Now: storekit.Base, MaxKeys: 1, Budgets: []apilimit.RequestBudget{requestBudget("zero", 0)}},
			{Now: storekit.At(2), MaxKeys: 1, Budgets: r.Budgets},
		} {
			_, err := s.RequestBudgets().Take(ctx, bad)
			storekit.WantErr(t, err, kernel.ErrInvalid)
		}
	})
	t.Run("RequestBudgetBoundedPruning", func(t *testing.T) {
		s := factory(t)
		r := apilimit.BudgetRequest{Now: storekit.Base, MaxKeys: 256}
		storekit.Must(t, s.Tx(t.Context(), func(ctx context.Context) error {
			for i := range 129 {
				r.Budgets = []apilimit.RequestBudget{requestBudget(fmt.Sprintf("key-%d", i), 1)}
				if _, err := s.RequestBudgets().Take(ctx, r); err != nil {
					return err //nolint:wrapcheck // Must reports it
				}
			}
			return nil
		}))
		r.Now, r.MaxKeys = storekit.At(1), 1
		r.Budgets = []apilimit.RequestBudget{
			{Key: replayDigest("new"), Limit: 1, Reset: storekit.At(2)},
		}
		_, err := s.RequestBudgets().Take(t.Context(), r)
		storekit.WantErr(t, err, apilimit.ErrBudgetCapacity)
		out, err := s.RequestBudgets().Take(t.Context(), r)
		storekit.Must(t, err)
		if !out.Allowed {
			t.Fatal("capacity failure rolled back bounded cleanup")
		}
	})
}
