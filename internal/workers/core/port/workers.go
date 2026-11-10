package port

import (
	"context"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/workers/core/model"
)

// WorkerFilter narrows worker listings.
type WorkerFilter struct {
	Action      model.WorkerAction
	ProjectKey  string
	EnabledOnly bool
}

// WorkerStore persists Workers (decision 0026) with their key-value state,
// sealed secrets and execution history.
type WorkerStore interface {
	// Create stores w; kernel.ErrConflict when the key exists.
	Create(ctx context.Context, w model.Worker) error
	// Get returns the worker.
	Get(ctx context.Context, key string) (model.Worker, error)
	// Update replaces the worker.
	Update(ctx context.Context, w model.Worker) error
	// Delete removes the worker with its state, secrets and executions.
	Delete(ctx context.Context, key string) error
	// List returns matching workers ordered by key.
	List(ctx context.Context, f WorkerFilter) ([]model.Worker, error)

	// State returns a worker's state entries; kernel.ErrNotFound when the
	// worker does not exist.
	State(ctx context.Context, worker string) (map[string]string, error)
	// SetState creates or replaces one entry; kernel.ErrNotFound when the
	// worker does not exist. The per-worker limits are the caller's to apply
	// (model.ValidateWorkerState).
	SetState(ctx context.Context, worker, key, value string) error
	// DeleteState removes one entry; removing a missing entry is not an
	// error.
	DeleteState(ctx context.Context, worker, key string) error

	// PutSecret creates or replaces a secret; kernel.ErrNotFound when the
	// worker does not exist.
	PutSecret(ctx context.Context, worker, name, value string) error
	// Secrets returns a worker's secrets by name; kernel.ErrNotFound when the
	// worker does not exist.
	Secrets(ctx context.Context, worker string) (map[string]string, error)
	// DeleteSecret removes a secret.
	DeleteSecret(ctx context.Context, worker, name string) error

	// AddExecution records an execution; kernel.ErrConflict when its ID exists,
	// kernel.ErrNotFound when its worker does not.
	AddExecution(ctx context.Context, e model.WorkerExecution) error
	// Execution returns one execution.
	Execution(ctx context.Context, id string) (model.WorkerExecution, error)
	// Executions returns a worker's executions ("" for every worker) newest
	// first, by (Started, ID).
	Executions(
		ctx context.Context,
		worker string,
		p kernel.Page,
	) ([]model.WorkerExecution, string, error)
	// PruneExecutions removes executions started before t and returns how
	// many.
	PruneExecutions(ctx context.Context, before time.Time) (int64, error)
}
