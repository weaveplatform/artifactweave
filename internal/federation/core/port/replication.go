package port

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
)

// ReplicationStore persists replication targets and their run history. The
// replication key is assigned by the caller; the password is sealed.
type ReplicationStore interface {
	// Put creates or replaces the replication with r.Key; kernel.ErrInvalid
	// when the key is empty.
	Put(ctx context.Context, r model.Replication) error
	// Get returns the replication with key.
	Get(ctx context.Context, key string) (model.Replication, error)
	// ForRepo returns a repository's replications ordered by key.
	ForRepo(ctx context.Context, repoKey string) ([]model.Replication, error)
	// List returns every replication ordered by (repository, key).
	List(ctx context.Context) ([]model.Replication, error)
	// Delete removes a replication and its runs.
	Delete(ctx context.Context, key string) error
	// DeleteForRepo removes every replication of a repository.
	DeleteForRepo(ctx context.Context, repoKey string) error
	// PutRun creates or replaces the run (r.ReplicationKey, r.Started);
	// kernel.ErrNotFound when the replication does not exist.
	PutRun(ctx context.Context, r model.ReplicationRun) error
	// LastRun returns a replication's most recent run.
	LastRun(ctx context.Context, key string) (model.ReplicationRun, error)
}
