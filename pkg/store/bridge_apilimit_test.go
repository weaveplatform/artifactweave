package store_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/apilimit"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// The bridge must alias the request budget port and its values, not declare
// its own (decision 0044, "Type-alias bridges"): the SQL and in-memory stores
// implement the port under the store names, and the metadata store's
// RequestBudgets returns it as store.RequestBudgetStore, while admission
// charges through apilimit.Store with the apilimit names, and neither side
// converts. A declared RequestBudgetStore would still compile here, because
// interfaces with the same methods are assignable both ways. A struct is
// assignable only to its own type and a pointer only to a pointer to the
// identical type, so these lines compile only while each pair is one type.
var (
	_ apilimit.RequestBudget       = store.RequestBudget{}
	_ apilimit.BudgetRequest       = store.BudgetRequest{}
	_ apilimit.BudgetBalance       = store.BudgetBalance{}
	_ apilimit.BudgetDecision      = store.BudgetDecision{}
	_ *apilimit.RequestBudgetStore = (*store.RequestBudgetStore)(nil)
)

// If the bridged sentinel were a copy, it would still compile and print the
// same text, but errors.Is would stop matching between the stores, which
// return it under the store name, and code that tests for it under the
// apilimit name. Only the same value matches in both directions, so that is
// what the test checks, also through a wrap.
func TestUnitBridgeAPILimitSentinel(t *testing.T) {
	legacy, target := store.ErrBudgetCapacity, apilimit.ErrBudgetCapacity
	if !errors.Is(legacy, target) || !errors.Is(target, legacy) {
		t.Error("the store and apilimit names hold different errors")
	}
	if !errors.Is(fmt.Errorf("op: %w", target), legacy) ||
		!errors.Is(fmt.Errorf("op: %w", legacy), target) {
		t.Error("a wrapped sentinel does not match under both names")
	}
	const text = "store: request budget capacity exhausted"
	for _, err := range []error{legacy, target} {
		if got := err.Error(); got != text {
			t.Errorf("Error() = %q, want %q", got, text)
		}
	}
}
