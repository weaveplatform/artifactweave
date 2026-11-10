package inmem

import (
	"cmp"
	"context"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// ArchivedItems returns the archived item store: the records archived
// items leave behind (decision 0024).
func (s *Store) ArchivedItems() port.ArchivedItemStore { return archivedItemStore{s} }

// ColdBlobs returns the cold tier's blob index: the blob index code over
// the cold_blobs table (decision 0024).
func (s *Store) ColdBlobs() port.BlobIndex { return blobIndex{s: s, table: "cold_blobs"} }

// ColdReplicas returns the cold tier's replica index: the replica index
// code over the cold_blob_replicas table (decision 0024).
func (s *Store) ColdReplicas() port.ReplicaIndex {
	return replicaIndex{s: s, table: "cold_blob_replicas"}
}

type archivedItemStore struct{ s *Store }

func (a archivedItemStore) items() *memtx.Table[int64, model.ArchivedItem] {
	return memtx.TableOf[int64, model.ArchivedItem](a.s.Store, "archived_items")
}

// archivedLocation is the canonical (parent, name) of an archived item's
// original path.
func archivedLocation(it model.Item) (parent, name string, err error) {
	parent, name, _, err = kernel.SplitItemPath(it.FullPath())
	if err != nil {
		return "", "", fmt.Errorf("archived item: %w: %w", kernel.ErrInvalid, err)
	}
	return parent, name, nil
}

func (a archivedItemStore) AddItem(
	ctx context.Context,
	it model.ArchivedItem,
) (model.ArchivedItem, error) {
	var out model.ArchivedItem
	err := a.s.Do(ctx, func() error {
		if _, _, err := archivedLocation(it.Item); err != nil {
			return err
		}
		it.ID = a.s.NextID("archived_items")
		a.items().Put(it.ID, it)
		out, _ = a.items().Get(it.ID)
		return nil
	})
	return out, err
}

func (a archivedItemStore) Item(ctx context.Context, id int64) (model.ArchivedItem, error) {
	var out model.ArchivedItem
	err := a.s.Do(ctx, func() error {
		var ok bool
		if out, ok = a.items().Get(id); !ok {
			return memtx.NotFound("archived item", id)
		}
		return nil
	})
	return out, err
}

func (a archivedItemStore) FindItems(
	ctx context.Context,
	repoKey, fullPath string,
) ([]model.ArchivedItem, error) {
	parent, name, _, err := kernel.SplitItemPath(fullPath)
	if err != nil {
		return nil, fmt.Errorf("find archived items: %w: %w", kernel.ErrInvalid, err)
	}
	var out []model.ArchivedItem
	err = a.s.Do(ctx, func() error {
		out = a.items().All(func(_ int64, v model.ArchivedItem) bool {
			p, n, err := archivedLocation(v.Item)
			return err == nil && v.Item.RepoKey == repoKey && p == parent && n == name
		}, func(x, y model.ArchivedItem) int { return cmp.Compare(y.ID, x.ID) })
		return nil
	})
	return out, err
}

func (a archivedItemStore) Items(
	ctx context.Context, f port.ArchivedFilter, p kernel.Page,
) ([]model.ArchivedItem, string, error) {
	var (
		out  []model.ArchivedItem
		next string
	)
	err := a.s.Do(ctx, func() error {
		rows := a.items().All(func(_ int64, v model.ArchivedItem) bool {
			return (f.RepoKey == "" || v.Item.RepoKey == f.RepoKey) &&
				(f.PolicyKey == "" || v.PolicyKey == f.PolicyKey) &&
				(f.ExecutionID == 0 || v.ExecutionID == f.ExecutionID)
		}, func(x, y model.ArchivedItem) int { return cmp.Compare(x.ID, y.ID) })
		var err error
		out, next, err = memtx.Paginate(
			rows,
			p,
			false,
			func(v model.ArchivedItem) []any { return []any{v.ID} },
		)
		return err //nolint:wrapcheck // Paginate's error wraps kernel.ErrInvalidCursor
	})
	return out, next, err
}

func (a archivedItemStore) DeleteItem(ctx context.Context, id int64) error {
	return a.s.Do(ctx, func() error {
		if !a.items().Del(id) {
			return memtx.NotFound("archived item", id)
		}
		return nil
	})
}

func (a archivedItemStore) ExpiredItems(
	ctx context.Context,
	before time.Time,
	limit int,
) ([]model.ArchivedItem, error) {
	// Compared at the stored precision, as the SQL column is.
	before = before.Truncate(time.Microsecond)
	var out []model.ArchivedItem
	err := a.s.Do(ctx, func() error {
		out = a.items().All(func(_ int64, v model.ArchivedItem) bool {
			return !v.ExpiresAt.IsZero() && v.ExpiresAt.Before(before)
		}, func(x, y model.ArchivedItem) int {
			return cmp.Or(x.ExpiresAt.Compare(y.ExpiresAt), cmp.Compare(x.ID, y.ID))
		})
		out = out[:min(len(out), expiredLimit(limit))]
		return nil
	})
	return out, err
}

// expiredLimit applies the default and maximum limit for "at most limit"
// lists.
func expiredLimit(limit int) int {
	if limit <= 0 {
		return kernel.DefaultLimit
	}
	return min(limit, kernel.MaxLimit)
}
