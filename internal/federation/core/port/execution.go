package port

import (
	"context"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// ExecutionStore publishes durable dispatch independently of configuration and
// transfer progress. All operations join the shared metadata transaction.
type ExecutionStore interface {
	Executions() ExecutionProgressStore
}

// ExecutionProgressStore retains logical identity after queue/target removal.
// A stored record grants no current source, caller or transfer authority.
type ExecutionProgressStore interface {
	// PutOptions creates immutable, resolved one-shot options under the exact
	// execution lifetime. An enclosing admission transaction is required.
	PutOptions(context.Context, model.ExecutionReference, model.ExecutionOptions) error
	Options(context.Context, model.ExecutionReference) (model.ExecutionOptions, error)
	// ExplicitTargets pages secret-free manual destinations under the current
	// repository incarnation. It must not open options or credentials.
	ExplicitTargets(context.Context, string, string, kernel.Page) ([]model.ReplicationTargetState, string, error)
	// SelectedPaths counts distinct selected canonical paths, so reconciliation
	// of a previously fetched file does not consume the nonadministrator cap twice.
	SelectedPaths(context.Context, model.ExecutionReference, string) (int64, bool, error)
	// Create is idempotent only for the same immutable binding. A Quartz
	// occurrence may identify only one logical execution, across all retries.
	Create(context.Context, string, model.ExecutionBinding, time.Time) (model.ExecutionRun, error)
	Run(context.Context, model.ExecutionReference) (model.ExecutionRun, error)
	ForOccurrence(context.Context, string) (model.ExecutionRun, error)
	// Advance requires an enclosing transaction and exact prior version.
	// Callers fence work ownership after all progress/status/event writes.
	Advance(context.Context, int64, model.ExecutionRun) error
	// RecordEffect joins the transfer intent's mutation transaction. It updates
	// exact evidence, indexed paths and cumulative counters atomically. A replay
	// never counts twice; stale versions and changed fingerprints conflict.
	RecordEffect(context.Context, model.ExecutionEffect) error
	Effect(context.Context, model.ExecutionReference, string) (model.ExecutionEffect, error)
	// PathStatus selects actual selected/effect evidence for the current target
	// and repository lifetime, without opening credentials or private bindings.
	PathStatus(context.Context, string, string, string) (model.ExecutionPathState, error)
}
