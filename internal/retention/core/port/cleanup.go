package port

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

// PolicyFilter narrows cleanup policy listings.
type PolicyFilter struct {
	ItemType   model.CleanupItemType
	ProjectKey string
}

// RunFilter narrows cleanup run listings.
type RunFilter struct {
	PolicyKey string
	ItemType  model.CleanupItemType
	Statuses  []api.RunStatus
}

// CleanupStore persists package and build cleanup policies and their runs.
type CleanupStore interface {
	CleanupProgressStore
	// CreatePolicy stores p; kernel.ErrConflict when the key exists.
	CreatePolicy(ctx context.Context, p model.CleanupPolicy) error
	// Policy returns the policy.
	Policy(ctx context.Context, key string) (model.CleanupPolicy, error)
	// UpdatePolicy replaces the policy.
	UpdatePolicy(ctx context.Context, p model.CleanupPolicy) error
	// DeletePolicy removes the policy; its runs are kept for the run views.
	DeletePolicy(ctx context.Context, key string) error
	// Policies returns matching policies ordered by key.
	Policies(ctx context.Context, f PolicyFilter) ([]model.CleanupPolicy, error)

	// CreateRun stores r and returns it with its assigned ID.
	CreateRun(ctx context.Context, r model.CleanupRun) (model.CleanupRun, error)
	// Run returns the run with id.
	Run(ctx context.Context, id int64) (model.CleanupRun, error)
	// UpdateRun replaces a run.
	UpdateRun(ctx context.Context, r model.CleanupRun) error
	// Runs returns matching runs newest first.
	Runs(ctx context.Context, f RunFilter, p kernel.Page) ([]model.CleanupRun, string, error)
	// LastRun returns a policy's most recent run.
	LastRun(ctx context.Context, policyKey string) (model.CleanupRun, error)
	// RunPage applies immutable ownership before filters, count and a bounded
	// offset window. Unbound legacy rows do not acquire guessed global scope.
	RunPage(context.Context, RunListing) (RunPage, error)

	// CreateExecution persists the private snapshot of an existing run. A
	// nonempty occurrence is unique across scheduler retries, including after
	// the policy is deleted; an empty one never coalesces manual runs.
	// The frozen policy omits its private configuration bindings: Credential
	// and ProjectIncarnation retain this run's own binding. Times use the
	// shared UTC microsecond precision.
	CreateExecution(context.Context, model.CleanupExecution) error
	// Execution locks the snapshot inside a transaction. Acquire current
	// project authority before this run lock, and the queue lease after it.
	Execution(context.Context, int64) (model.CleanupExecution, error)
	// ExecutionForOccurrence resolves a nonempty logical scheduler fire.
	ExecutionForOccurrence(context.Context, string) (model.CleanupExecution, error)
}
