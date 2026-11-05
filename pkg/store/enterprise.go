package store

import (
	"context"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// ExecutionFilter narrows archive execution listings.
type ExecutionFilter struct {
	PolicyKey string
	Kind      types.ExecutionKind
}

// ArchivedFilter narrows archived item listings.
type ArchivedFilter struct {
	RepoKey     string
	PolicyKey   string
	ExecutionID int64
}

// ArchiveStore persists the cold-storage tier's records (decision 0024):
// archive policies of both families, executions, archived items and the
// cold blob index.
type ArchiveStore interface {
	// CreatePolicy stores p; ErrConflict when the key exists.
	CreatePolicy(ctx context.Context, p types.ArchivePolicy) error
	// Policy returns the policy.
	Policy(ctx context.Context, key string) (types.ArchivePolicy, error)
	// UpdatePolicy replaces the policy.
	UpdatePolicy(ctx context.Context, p types.ArchivePolicy) error
	// DeletePolicy removes the policy; its executions and items are kept.
	DeletePolicy(ctx context.Context, key string) error
	// Policies returns a family's policies ("" for both) ordered by key.
	Policies(ctx context.Context, family types.ArchiveFamily) ([]types.ArchivePolicy, error)

	// CreateExecution stores e and returns it with its assigned ID.
	CreateExecution(ctx context.Context, e types.ArchiveExecution) (types.ArchiveExecution, error)
	// Execution returns the execution with id.
	Execution(ctx context.Context, id int64) (types.ArchiveExecution, error)
	// UpdateExecution replaces an execution.
	UpdateExecution(ctx context.Context, e types.ArchiveExecution) error
	// Executions returns matching executions newest first.
	Executions(
		ctx context.Context,
		f ExecutionFilter,
		p Page,
	) ([]types.ArchiveExecution, string, error)

	// AddItem stores an archived item and returns it with its assigned ID;
	// ErrInvalid when the item's full path does not split
	// (types.SplitItemPath).
	AddItem(ctx context.Context, a types.ArchivedItem) (types.ArchivedItem, error)
	// Item returns the archived item with id.
	Item(ctx context.Context, id int64) (types.ArchivedItem, error)
	// FindItems returns the archived copies of an original repository and
	// full path, newest first by ID; ErrInvalid for a path that does not
	// split.
	FindItems(ctx context.Context, repoKey, fullPath string) ([]types.ArchivedItem, error)
	// Items returns matching archived items ordered by ID; a zero
	// ExecutionID matches every execution.
	Items(ctx context.Context, f ArchivedFilter, p Page) ([]types.ArchivedItem, string, error)
	// DeleteItem removes an archived item.
	DeleteItem(ctx context.Context, id int64) error
	// ExpiredItems returns items whose ExpiresAt is set and before t,
	// oldest first, at most limit (DefaultLimit when zero).
	ExpiredItems(ctx context.Context, before time.Time, limit int) ([]types.ArchivedItem, error)

	// ColdBlobs is the cold tier's blob index.
	ColdBlobs() BlobIndex
	// ColdReplicas is the cold tier's replica index, for a mirrored cold
	// tier.
	ColdReplicas() ReplicaIndex
}
