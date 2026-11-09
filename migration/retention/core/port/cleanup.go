package port

import (
	"context"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/retention/api"
	"github.com/weaveplatform/artifactweave/migration/retention/core/model"
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
	// CreatePolicy stores p; ErrConflict when the key exists.
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
}
