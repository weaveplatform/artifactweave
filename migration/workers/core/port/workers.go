package port

import (
	"context"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// WorkerFilter narrows worker listings.
type WorkerFilter struct {
	Action      types.WorkerAction
	ProjectKey  string
	EnabledOnly bool
}

// WorkerStore persists Workers (decision 0026) with their key-value state,
// sealed secrets and execution history.
type WorkerStore interface {
	// Create stores w; ErrConflict when the key exists.
	Create(ctx context.Context, w types.Worker) error
	// Get returns the worker.
	Get(ctx context.Context, key string) (types.Worker, error)
	// Update replaces the worker.
	Update(ctx context.Context, w types.Worker) error
	// Delete removes the worker with its state, secrets and executions.
	Delete(ctx context.Context, key string) error
	// List returns matching workers ordered by key.
	List(ctx context.Context, f WorkerFilter) ([]types.Worker, error)

	// State returns a worker's state entries; ErrNotFound when the worker
	// does not exist.
	State(ctx context.Context, worker string) (map[string]string, error)
	// SetState creates or replaces one entry; ErrNotFound when the worker
	// does not exist. The per-worker limits are the caller's to apply
	// (types.ValidateWorkerState).
	SetState(ctx context.Context, worker, key, value string) error
	// DeleteState removes one entry; removing a missing entry is not an
	// error.
	DeleteState(ctx context.Context, worker, key string) error

	// PutSecret creates or replaces a secret; ErrNotFound when the worker
	// does not exist.
	PutSecret(ctx context.Context, worker, name, value string) error
	// Secrets returns a worker's secrets by name; ErrNotFound when the
	// worker does not exist.
	Secrets(ctx context.Context, worker string) (map[string]string, error)
	// DeleteSecret removes a secret.
	DeleteSecret(ctx context.Context, worker, name string) error

	// AddExecution records an execution; ErrConflict when its ID exists,
	// ErrNotFound when its worker does not.
	AddExecution(ctx context.Context, e types.WorkerExecution) error
	// Execution returns one execution.
	Execution(ctx context.Context, id string) (types.WorkerExecution, error)
	// Executions returns a worker's executions ("" for every worker) newest
	// first, by (Started, ID).
	Executions(ctx context.Context, worker string, p Page) ([]types.WorkerExecution, string, error)
	// PruneExecutions removes executions started before t and returns how
	// many.
	PruneExecutions(ctx context.Context, before time.Time) (int64, error)
}
