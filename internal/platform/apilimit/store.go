package apilimit

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// ErrBudgetCapacity means the bounded budget table cannot admit another key.
// The budget stores return it, so its text starts "store:", as their other
// errors do.
var ErrBudgetCapacity = errors.New("store: request budget capacity exhausted")

// RequestBudget selects a fixed-window request counter. Key is a lowercase
// SHA-256 digest of a server-owned policy name and its authenticated scope.
// Reset is the end of the current window; an active stored window is never
// shortened by another replica with an older clock or policy configuration.
type RequestBudget struct {
	Key   string
	Limit int64
	Reset time.Time
}

// BudgetRequest atomically charges one request against all its budgets.
// MaxKeys caps retained counters across the shared store (at most 65536).
// Replicas must use the same capacity/policies and synchronized clocks.
type BudgetRequest struct {
	Budgets []RequestBudget
	Now     time.Time
	MaxKeys int
}

// Validate bounds both the persisted state and the work in one admission. A
// request out of bounds gives an error wrapping kernel.ErrInvalid, the
// metadata store's invalid record, not this package's ErrInvalid, which
// reports an invalid policy.
func (r BudgetRequest) Validate() error {
	if len(r.Budgets) < 1 || len(r.Budgets) > 8 || r.Now.IsZero() || r.MaxKeys < 1 ||
		r.MaxKeys > 65536 {
		return fmt.Errorf("request budget bounds: %w", kernel.ErrInvalid)
	}
	seen := make(map[string]bool, len(r.Budgets))
	for _, b := range r.Budgets {
		raw, err := hex.DecodeString(b.Key)
		if err != nil || len(raw) != 32 || hex.EncodeToString(raw) != b.Key || seen[b.Key] ||
			b.Limit < 1 || b.Limit > 1_000_000_000 || b.Reset.UnixMicro() <= r.Now.UnixMicro() || b.Reset.Sub(r.Now) > time.Hour {
			return fmt.Errorf("invalid request budget: %w", kernel.ErrInvalid)
		}
		seen[b.Key] = true
	}
	return nil
}

// BudgetBalance is one request budget's remaining requests and reset instant.
type BudgetBalance struct {
	Remaining int64
	Reset     time.Time
}

// BudgetDecision reports balances in request order. A refusal charges none of
// the budgets; balances then describe their current, unmodified usage.
type BudgetDecision struct {
	Allowed  bool
	Balances []BudgetBalance
}

// RequestBudgetStore enforces request rates across instances sharing metadata.
// It does not count simultaneous work; Service holds the concurrency permits,
// per process.
type RequestBudgetStore interface {
	// Take atomically tests every budget and increments all or none. Expired
	// counters are reclaimed in bounded batches. Backend errors never grant
	// admission; callers must not replace this with a per-process fallback.
	Take(ctx context.Context, request BudgetRequest) (BudgetDecision, error)
}
