package port

import (
	"context"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
)

// ExecutionStore publishes durable dispatch independently of configuration and
// transfer progress. All operations join the shared metadata transaction.
type ExecutionStore interface {
	Executions() ExecutionProgressStore
}

// ExecutionProgressStore retains logical identity after queue/target removal.
// A stored record grants no current source, caller or transfer authority.
type ExecutionProgressStore interface {
	// Create is idempotent only for the same immutable binding. A Quartz
	// occurrence may identify only one logical execution, across all retries.
	Create(context.Context, string, model.ExecutionBinding, time.Time) (model.ExecutionRun, error)
	Run(context.Context, model.ExecutionReference) (model.ExecutionRun, error)
	ForOccurrence(context.Context, string) (model.ExecutionRun, error)
	// Advance requires an enclosing transaction and exact prior version.
	// Callers fence work ownership after all progress/status/event writes.
	Advance(context.Context, int64, model.ExecutionRun) error
}
