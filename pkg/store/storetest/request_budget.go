package storetest

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/store"
)

func requestBudget(key string, limit int64) store.RequestBudget {
	return store.RequestBudget{Key: replayDigest(key), Limit: limit, Reset: at(1)}
}

func runRequestBudgets(t *testing.T, factory Factory) {
	t.Run("RequestBudgets", func(t *testing.T) {
		s := factory(t)
		ctx := t.Context()
		r := store.BudgetRequest{Now: base, MaxKeys: 3, Budgets: []store.RequestBudget{
			requestBudget("global", 10), requestBudget("alice", 2),
		}}
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
		out := get[store.BudgetDecision](t)(s.RequestBudgets().Take(ctx, r))
		if !out.Allowed || out.Balances[0].Remaining != 7 || out.Balances[1].Remaining != 1 {
			t.Fatalf("one principal consumed another's budget: %+v", out)
		}
		r.Budgets[1] = requestBudget("charlie", 2)
		out, err := s.RequestBudgets().Take(ctx, r)
		wantErr(t, err, store.ErrBudgetCapacity)
		if out.Allowed {
			t.Fatal("capacity failure granted admission")
		}
		r.Budgets = r.Budgets[:1]
		out = get[store.BudgetDecision](t)(s.RequestBudgets().Take(ctx, r))
		if out.Balances[0].Remaining != 6 {
			t.Fatalf("capacity failure charged global budget: %+v", out)
		}
		must(t, func() error {
			err := s.Tx(ctx, func(ctx context.Context) error {
				if _, err := s.RequestBudgets().Take(ctx, r); err != nil {
					return err
				}
				return store.ErrConflict
			})
			wantErr(t, err, store.ErrConflict)
			return nil
		}())
		out = get[store.BudgetDecision](t)(s.RequestBudgets().Take(ctx, r))
		if out.Balances[0].Remaining != 5 {
			t.Fatalf("rolled-back admission survived: %+v", out)
		}
		r.Now, r.Budgets[0].Reset = at(1), at(2)
		out = get[store.BudgetDecision](t)(s.RequestBudgets().Take(ctx, r))
		if !out.Allowed || out.Balances[0].Remaining != 9 {
			t.Fatalf("window did not reset: %+v", out)
		}
		// A late request from the old window cannot reset a newer counter.
		r.Now, r.Budgets[0].Reset = base, at(1)
		out = get[store.BudgetDecision](t)(s.RequestBudgets().Take(ctx, r))
		if out.Balances[0].Remaining != 8 || !out.Balances[0].Reset.Equal(store.Normalise(at(2))) {
			t.Fatalf("older clock reset the window: %+v", out)
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		_, err = s.RequestBudgets().Take(canceled, r)
		wantErr(t, err, context.Canceled)
		for _, bad := range []store.BudgetRequest{
			{},
			{Now: base, MaxKeys: 1},
			{Now: base, MaxKeys: 0, Budgets: r.Budgets},
			{Now: base, MaxKeys: 65537, Budgets: r.Budgets},
			{Now: base, MaxKeys: 2, Budgets: []store.RequestBudget{r.Budgets[0], r.Budgets[0]}},
			{Now: base, MaxKeys: 1, Budgets: []store.RequestBudget{{Key: "invalid", Limit: 1, Reset: at(1)}}},
			{Now: base, MaxKeys: 1, Budgets: []store.RequestBudget{requestBudget("zero", 0)}},
			{Now: at(2), MaxKeys: 1, Budgets: r.Budgets},
		} {
			_, err := s.RequestBudgets().Take(ctx, bad)
			wantErr(t, err, store.ErrInvalid)
		}
	})
	t.Run("RequestBudgetBoundedPruning", func(t *testing.T) {
		s := factory(t)
		r := store.BudgetRequest{Now: base, MaxKeys: 256}
		must(t, s.Tx(t.Context(), func(ctx context.Context) error {
			for i := range 129 {
				r.Budgets = []store.RequestBudget{requestBudget(fmt.Sprintf("key-%d", i), 1)}
				if _, err := s.RequestBudgets().Take(ctx, r); err != nil {
					return err
				}
			}
			return nil
		}))
		r.Now, r.MaxKeys = at(1), 1
		r.Budgets = []store.RequestBudget{{Key: replayDigest("new"), Limit: 1, Reset: at(2)}}
		_, err := s.RequestBudgets().Take(t.Context(), r)
		wantErr(t, err, store.ErrBudgetCapacity)
		out, err := s.RequestBudgets().Take(t.Context(), r)
		must(t, err)
		if !out.Allowed {
			t.Fatal("capacity failure rolled back bounded cleanup")
		}
	})
}
