package port

import (
	"context"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/retention/core/model"
)

// ExecutionFilter narrows archive execution listings.
type ExecutionFilter struct {
	PolicyKey string
	Kind      model.ExecutionKind
}

// ArchiveStore persists the cold-storage tier's retention records (decision
// 0024): archive policies of both families and their executions. The
// archived items and the cold indexes have accessors of their own on Store.
type ArchiveStore interface {
	// CreatePolicy stores p; ErrConflict when the key exists.
	CreatePolicy(ctx context.Context, p model.ArchivePolicy) error
	// Policy returns the policy.
	Policy(ctx context.Context, key string) (model.ArchivePolicy, error)
	// UpdatePolicy replaces the policy.
	UpdatePolicy(ctx context.Context, p model.ArchivePolicy) error
	// DeletePolicy removes the policy; its executions and items are kept.
	DeletePolicy(ctx context.Context, key string) error
	// Policies returns a family's policies ("" for both) ordered by key.
	Policies(ctx context.Context, family model.ArchiveFamily) ([]model.ArchivePolicy, error)

	// CreateExecution stores e and returns it with its assigned ID.
	CreateExecution(ctx context.Context, e model.ArchiveExecution) (model.ArchiveExecution, error)
	// Execution returns the execution with id.
	Execution(ctx context.Context, id int64) (model.ArchiveExecution, error)
	// UpdateExecution replaces an execution.
	UpdateExecution(ctx context.Context, e model.ArchiveExecution) error
	// Executions returns matching executions newest first.
	Executions(
		ctx context.Context,
		f ExecutionFilter,
		p kernel.Page,
	) ([]model.ArchiveExecution, string, error)
}
