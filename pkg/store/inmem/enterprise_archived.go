package inmem

import (
	"cmp"
	"context"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func (a archiveStore) items() *memtx.Table[int64, types.ArchivedItem] {
	return memtx.TableOf[int64, types.ArchivedItem](a.s.Store, "archived_items")
}

// ColdBlobs implements store.ArchiveStore.
func (a archiveStore) ColdBlobs() store.BlobIndex { return blobIndex{s: a.s, table: "cold_blobs"} }

// ColdReplicas implements store.ArchiveStore.
func (a archiveStore) ColdReplicas() store.ReplicaIndex {
	return replicaIndex{s: a.s, table: "cold_blob_replicas"}
}

// archivedLocation is the canonical (parent, name) of an archived item's
// original path.
func archivedLocation(it types.Item) (parent, name string, err error) {
	parent, name, _, err = types.SplitItemPath(it.FullPath())
	if err != nil {
		return "", "", fmt.Errorf("archived item: %w: %w", store.ErrInvalid, err)
	}
	return parent, name, nil
}

func (a archiveStore) AddItem(
	ctx context.Context,
	it types.ArchivedItem,
) (types.ArchivedItem, error) {
	var out types.ArchivedItem
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

func (a archiveStore) Item(ctx context.Context, id int64) (types.ArchivedItem, error) {
	var out types.ArchivedItem
	err := a.s.Do(ctx, func() error {
		var ok bool
		if out, ok = a.items().Get(id); !ok {
			return memtx.NotFound("archived item", id)
		}
		return nil
	})
	return out, err
}

func (a archiveStore) FindItems(
	ctx context.Context,
	repoKey, fullPath string,
) ([]types.ArchivedItem, error) {
	parent, name, _, err := types.SplitItemPath(fullPath)
	if err != nil {
		return nil, fmt.Errorf("find archived items: %w: %w", store.ErrInvalid, err)
	}
	var out []types.ArchivedItem
	err = a.s.Do(ctx, func() error {
		out = a.items().All(func(_ int64, v types.ArchivedItem) bool {
			p, n, err := archivedLocation(v.Item)
			return err == nil && v.Item.RepoKey == repoKey && p == parent && n == name
		}, func(x, y types.ArchivedItem) int { return cmp.Compare(y.ID, x.ID) })
		return nil
	})
	return out, err
}

func (a archiveStore) Items(
	ctx context.Context, f store.ArchivedFilter, p store.Page,
) ([]types.ArchivedItem, string, error) {
	var (
		out  []types.ArchivedItem
		next string
	)
	err := a.s.Do(ctx, func() error {
		rows := a.items().All(func(_ int64, v types.ArchivedItem) bool {
			return (f.RepoKey == "" || v.Item.RepoKey == f.RepoKey) &&
				(f.PolicyKey == "" || v.PolicyKey == f.PolicyKey) &&
				(f.ExecutionID == 0 || v.ExecutionID == f.ExecutionID)
		}, func(x, y types.ArchivedItem) int { return cmp.Compare(x.ID, y.ID) })
		var err error
		out, next, err = memtx.Paginate(
			rows,
			p,
			false,
			func(v types.ArchivedItem) []any { return []any{v.ID} },
		)
		return err
	})
	return out, next, err
}

func (a archiveStore) DeleteItem(ctx context.Context, id int64) error {
	return a.s.Do(ctx, func() error {
		if !a.items().Del(id) {
			return memtx.NotFound("archived item", id)
		}
		return nil
	})
}

func (a archiveStore) ExpiredItems(
	ctx context.Context,
	before time.Time,
	limit int,
) ([]types.ArchivedItem, error) {
	// Compared at the stored precision, as the SQL column is.
	before = before.Truncate(time.Microsecond)
	var out []types.ArchivedItem
	err := a.s.Do(ctx, func() error {
		out = a.items().All(func(_ int64, v types.ArchivedItem) bool {
			return !v.ExpiresAt.IsZero() && v.ExpiresAt.Before(before)
		}, func(x, y types.ArchivedItem) int {
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
		return store.DefaultLimit
	}
	return min(limit, store.MaxLimit)
}
