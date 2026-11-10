package inmem

import (
	"context"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/apilimit"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// RequestBudgets returns the request budget store (decision 0043).
func (s *Store) RequestBudgets() apilimit.RequestBudgetStore { return requestBudgets{s} }

type (
	requestBudgets struct{ s *Store }
	budgetWindow   struct {
		used  int64
		reset time.Time
	}
)

func (b requestBudgets) rows() *memtx.Table[string, budgetWindow] {
	return memtx.TableOf[string, budgetWindow](b.s.Store, "request_budgets")
}

func (b requestBudgets) Take(
	ctx context.Context,
	r apilimit.BudgetRequest,
) (apilimit.BudgetDecision, error) {
	var out apilimit.BudgetDecision
	if err := r.Validate(); err != nil {
		return out, err //nolint:wrapcheck // Validate's error names the request budget
	}
	err := b.s.Do(ctx, func() error {
		rows := b.rows().Rows
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
		out.Balances = make([]apilimit.BudgetBalance, len(r.Budgets))
		windows := make([]budgetWindow, len(r.Budgets))
		newKeys := 0
		for i, budget := range r.Budgets {
			v, ok := rows[budget.Key]
			if !ok {
				newKeys++
			}
			if !ok || v.reset.UnixMicro() <= r.Now.UnixMicro() {
				v = budgetWindow{reset: kernel.Normalise(budget.Reset)}
			}
			windows[i] = v
			remaining := max(0, budget.Limit-v.used)
			out.Balances[i] = apilimit.BudgetBalance{Remaining: remaining, Reset: v.reset}
			out.Allowed = out.Allowed && remaining > 0
		}
		if !out.Allowed {
			return nil
		}
		if len(rows)+newKeys > r.MaxKeys {
			return apilimit.ErrBudgetCapacity
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
