package store

import (
	"context"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// ReplicationStore persists replication targets and their run history. The
// replication key is assigned by the caller; the password is sealed.
type ReplicationStore interface {
	// Put creates or replaces the replication with r.Key; ErrInvalid when
	// the key is empty.
	Put(ctx context.Context, r types.Replication) error
	// Get returns the replication with key.
	Get(ctx context.Context, key string) (types.Replication, error)
	// ForRepo returns a repository's replications ordered by key.
	ForRepo(ctx context.Context, repoKey string) ([]types.Replication, error)
	// List returns every replication ordered by (repository, key).
	List(ctx context.Context) ([]types.Replication, error)
	// Delete removes a replication and its runs.
	Delete(ctx context.Context, key string) error
	// DeleteForRepo removes every replication of a repository.
	DeleteForRepo(ctx context.Context, repoKey string) error
	// PutRun creates or replaces the run (r.ReplicationKey, r.Started);
	// ErrNotFound when the replication does not exist.
	PutRun(ctx context.Context, r types.ReplicationRun) error
	// LastRun returns a replication's most recent run.
	LastRun(ctx context.Context, key string) (types.ReplicationRun, error)
}
