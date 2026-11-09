package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
)

// RequestBudgets implements store.Store.
func (s *Store) RequestBudgets() store.RequestBudgetStore { return requestBudgets{s} }

type (
	requestBudgets struct{ s *Store }
	budgetWindow   struct {
		used  int64
		reset int64
	}
)

func (b requestBudgets) Take(
	ctx context.Context,
	r store.BudgetRequest,
) (store.BudgetDecision, error) {
	var out store.BudgetDecision
	var full bool
	if err := r.Validate(); err != nil {
		return out, fmt.Errorf("request budget: %w", err)
	}
	err := b.s.Tx(ctx, func(ctx context.Context) error {
		// One permanent guard serializes multi-counter admission and the table
		// capacity check. No user-controlled lock rows or pending claims exist.
		var guard int
		if err := b.s.Q(ctx).
			QueryRowContext(ctx, "SELECT id FROM request_budget_guard WHERE id = 1 "+b.s.Dialect().ForUpdate).
			Scan(&guard); err != nil {
			return b.s.Wrap("lock request budgets", err)
		}
		if _, err := b.s.Exec(
			ctx,
			"prune request budgets",
			"DELETE FROM request_budgets WHERE key_hash IN (SELECT key_hash FROM request_budgets WHERE resets_at <= ? ORDER BY resets_at, key_hash LIMIT 128)",
			r.Now.UnixMicro(),
		); err != nil {
			return err
		}
		out.Allowed = true
		out.Balances = make([]store.BudgetBalance, len(r.Budgets))
		windows := make([]budgetWindow, len(r.Budgets))
		newKeys := 0
		for i, budget := range r.Budgets {
			v, exists, err := b.get(ctx, budget.Key)
			if err != nil {
				return err
			}
			if !exists {
				newKeys++
			}
			if !exists || v.reset <= r.Now.UnixMicro() {
				v = budgetWindow{reset: budget.Reset.UnixMicro()}
			}
			windows[i] = v
			remaining := max(0, budget.Limit-v.used)
			out.Balances[i] = store.BudgetBalance{
				Remaining: remaining,
				Reset:     time.UnixMicro(v.reset).UTC(),
			}
			out.Allowed = out.Allowed && remaining > 0
		}
		if !out.Allowed {
			return nil
		}
		if err := b.capacity(ctx, newKeys, r.MaxKeys); err != nil {
			if errors.Is(err, store.ErrBudgetCapacity) {
				full = true
				return nil
			}
			return err
		}
		for i, budget := range r.Budgets {
			if _, err := b.s.Exec(
				ctx,
				"charge request budget",
				Upsert(
					"request_budgets",
					[]string{"key_hash", "used", "resets_at"},
					[]string{"key_hash"},
				),
				budget.Key,
				windows[i].used+1,
				windows[i].reset,
			); err != nil {
				return err
			}
			out.Balances[i].Remaining--
		}
		return nil
	})
	if err == nil && full {
		err = store.ErrBudgetCapacity
	}
	if err != nil {
		out.Allowed = false
	}
	return out, err
}

func (b requestBudgets) get(ctx context.Context, key string) (budgetWindow, bool, error) {
	var v budgetWindow
	err := b.s.Q(ctx).
		QueryRowContext(ctx, b.s.Dialect().Rebind("SELECT used, resets_at FROM request_budgets WHERE key_hash = ?"), key).
		Scan(&v.used, &v.reset)
	if errors.Is(err, sql.ErrNoRows) {
		return v, false, nil
	}
	return v, err == nil, b.s.Wrap("read request budget", err)
}

func (b requestBudgets) capacity(ctx context.Context, newKeys, maxKeys int) error {
	if newKeys == 0 {
		return nil
	}
	var count int
	if err := b.s.Q(ctx).
		QueryRowContext(ctx, "SELECT COUNT(*) FROM request_budgets").
		Scan(&count); err != nil {
		return b.s.Wrap("count request budgets", err)
	}
	if count+newKeys > maxKeys {
		return store.ErrBudgetCapacity
	}
	return nil
}
