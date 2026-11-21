package port

import (
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

// RunScope is one already-authorized historical ownership constraint. An empty
// key means a global policy; an empty incarnation admits every lifetime only
// when the caller has current instance-wide administration authority.
type RunScope struct {
	ProjectKey, Incarnation string
}

// RunOrder names supported descriptive run fields, never a SQL expression.
type RunOrder string

// Run ordering fields retain a numeric run-ID tie breaker in the same direction.
const (
	RunOrderID           RunOrder = "runId"
	RunOrderItemType     RunOrder = "itemType"
	RunOrderPolicy       RunOrder = "policyKey"
	RunOrderProject      RunOrder = "displayProject"
	RunOrderStarted      RunOrder = "startTime"
	RunOrderEnded        RunOrder = "endTime"
	RunOrderTotal        RunOrder = "totalItems"
	RunOrderCleanedSize  RunOrder = "cleanedSize"
	RunOrderCleanedItems RunOrder = "cleanedItems"
	RunOrderStatus       RunOrder = "status"
	RunOrderActor        RunOrder = "triggeredBy"
	RunOrderManual       RunOrder = "manual"
	RunOrderDry          RunOrder = "dryRun"
)

// Valid reports whether the field has a bounded store ordering implementation.
func (o RunOrder) Valid() bool {
	switch o {
	case "", RunOrderID, RunOrderItemType, RunOrderPolicy, RunOrderProject, RunOrderStarted, RunOrderEnded,
		RunOrderTotal, RunOrderCleanedSize, RunOrderCleanedItems, RunOrderStatus, RunOrderActor, RunOrderManual, RunOrderDry:
		return true
	default:
		return false
	}
}

// RunListing filters known package executions before counting or paging.
// Scopes is mandatory visibility: an empty set admits no runs. ItemTypes and
// Statuses are optional narrowing filters; unsupported execution families and
// archive warming states are never substituted for package work. Start bounds
// use stored UTC microseconds, inclusive From and exclusive To. Missing times
// sort before real times in ascending order, and after them in descending order.
type RunListing struct {
	Scopes       []RunScope
	PolicyKey    string
	ItemTypes    []model.CleanupItemType
	Statuses     []api.RunStatus
	From, To     *int64
	ExcludeBlank bool
	Order        RunOrder
	Descending   bool
	Offset       int64
	Limit        int
}

// Validate rejects unsafe windows before an adapter reads or allocates results.
func (q RunListing) Validate() error {
	if q.Limit < 1 || q.Limit > 500 || q.Offset < 0 || !q.Order.Valid() || len(q.Scopes) > 1000 {
		return fmt.Errorf("invalid cleanup run window: %w", kernel.ErrInvalid)
	}
	if q.From != nil && q.To != nil && *q.From > *q.To {
		return fmt.Errorf("invalid cleanup run time bounds: %w", kernel.ErrInvalid)
	}
	return nil
}

// RunPage contains at most Limit snapshots and a total over the same visibility
// and filters. Policy documents are frozen and contain no execution credentials.
type RunPage struct {
	Runs  []model.CleanupRunSnapshot
	Total int64
}
