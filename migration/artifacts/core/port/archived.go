package port

import (
	"context"
	"time"

	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/kernel"
)

// ArchivedFilter narrows archived item listings.
type ArchivedFilter struct {
	RepoKey     string
	PolicyKey   string
	ExecutionID int64
}

// ArchivedItemStore persists the records archived items leave behind
// (decision 0024): each item's full metadata, so a restore recreates it and
// AQL over archives can search it. The records belong to artifacts, while
// the archive policies and executions that made them are retention's
// (ArchiveStore).
type ArchivedItemStore interface {
	// AddItem stores an archived item and returns it with its assigned ID;
	// kernel.ErrInvalid when the item's full path does not split
	// (kernel.SplitItemPath).
	AddItem(ctx context.Context, a model.ArchivedItem) (model.ArchivedItem, error)
	// Item returns the archived item with id.
	Item(ctx context.Context, id int64) (model.ArchivedItem, error)
	// FindItems returns the archived copies of an original repository and
	// full path, newest first by ID; kernel.ErrInvalid for a path that does not
	// split.
	FindItems(ctx context.Context, repoKey, fullPath string) ([]model.ArchivedItem, error)
	// Items returns matching archived items ordered by ID; a zero
	// ExecutionID matches every execution.
	Items(
		ctx context.Context,
		f ArchivedFilter,
		p kernel.Page,
	) ([]model.ArchivedItem, string, error)
	// DeleteItem removes an archived item.
	DeleteItem(ctx context.Context, id int64) error
	// ExpiredItems returns items whose ExpiresAt is set and before t,
	// oldest first, at most limit (kernel.DefaultLimit when zero).
	ExpiredItems(ctx context.Context, before time.Time, limit int) ([]model.ArchivedItem, error)
}
