package inmem

import (
	"context"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
)

func (s *Store) RequestBudgets() store.RequestBudgetStore { return requestBudgets{s} }

type requestBudgets struct{ s *Store }
type budgetWindow struct {
	used  int64
	reset time.Time
}

func (b requestBudgets) rows() *table[string, budgetWindow] {
	return tableOf[string, budgetWindow](b.s, "request_budgets")
}

func (b requestBudgets) Take(ctx context.Context, r store.BudgetRequest) (store.BudgetDecision, error) {
	var out store.BudgetDecision
	if err := r.Validate(); err != nil {
		return out, err
	}
	err := b.s.do(ctx, func() error {
		rows := b.rows().rows
		pruned := 0
		for key, v := range rows {
			if v.reset.UnixMicro() <= r.Now.UnixMicro() {
				delete(rows, key)
				pruned++
				if pruned == 128 {
					break
				}
			}
		}
		out.Allowed = true
		out.Balances = make([]store.BudgetBalance, len(r.Budgets))
		windows := make([]budgetWindow, len(r.Budgets))
		newKeys := 0
		for i, budget := range r.Budgets {
			v, ok := rows[budget.Key]
			if !ok {
				newKeys++
			}
			if !ok || v.reset.UnixMicro() <= r.Now.UnixMicro() {
				v = budgetWindow{reset: store.Normalise(budget.Reset)}
			}
			windows[i] = v
			remaining := max(0, budget.Limit-v.used)
			out.Balances[i] = store.BudgetBalance{Remaining: remaining, Reset: v.reset}
			out.Allowed = out.Allowed && remaining > 0
		}
		if !out.Allowed {
			return nil
		}
		if len(rows)+newKeys > r.MaxKeys {
			return store.ErrBudgetCapacity
		}
		for i, budget := range r.Budgets {
			windows[i].used++
			rows[budget.Key] = windows[i]
			out.Balances[i].Remaining--
		}
		return nil
	})
	if err != nil {
		out.Allowed = false
	}
	return out, err
}
