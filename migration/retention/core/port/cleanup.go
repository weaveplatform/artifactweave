package port

import (
	"context"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// PolicyFilter narrows cleanup policy listings.
type PolicyFilter struct {
	ItemType   types.CleanupItemType
	ProjectKey string
}

// RunFilter narrows cleanup run listings.
type RunFilter struct {
	PolicyKey string
	ItemType  types.CleanupItemType
	Statuses  []types.RunStatus
}

// CleanupStore persists package and build cleanup policies and their runs.
type CleanupStore interface {
	// CreatePolicy stores p; ErrConflict when the key exists.
	CreatePolicy(ctx context.Context, p types.CleanupPolicy) error
	// Policy returns the policy.
	Policy(ctx context.Context, key string) (types.CleanupPolicy, error)
	// UpdatePolicy replaces the policy.
	UpdatePolicy(ctx context.Context, p types.CleanupPolicy) error
	// DeletePolicy removes the policy; its runs are kept for the run views.
	DeletePolicy(ctx context.Context, key string) error
	// Policies returns matching policies ordered by key.
	Policies(ctx context.Context, f PolicyFilter) ([]types.CleanupPolicy, error)

	// CreateRun stores r and returns it with its assigned ID.
	CreateRun(ctx context.Context, r types.CleanupRun) (types.CleanupRun, error)
	// Run returns the run with id.
	Run(ctx context.Context, id int64) (types.CleanupRun, error)
	// UpdateRun replaces a run.
	UpdateRun(ctx context.Context, r types.CleanupRun) error
	// Runs returns matching runs newest first.
	Runs(ctx context.Context, f RunFilter, p Page) ([]types.CleanupRun, string, error)
	// LastRun returns a policy's most recent run.
	LastRun(ctx context.Context, policyKey string) (types.CleanupRun, error)
}
