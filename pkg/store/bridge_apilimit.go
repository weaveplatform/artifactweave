// Bridge (P9d): declared in migration/platform/apilimit; PRO-02 deletes it.

package store

import "github.com/weaveplatform/artifactweave/migration/platform/apilimit"

var ErrBudgetCapacity = apilimit.ErrBudgetCapacity

type (
	RequestBudget      = apilimit.RequestBudget
	BudgetRequest      = apilimit.BudgetRequest
	BudgetBalance      = apilimit.BudgetBalance
	BudgetDecision     = apilimit.BudgetDecision
	RequestBudgetStore = apilimit.RequestBudgetStore
)
