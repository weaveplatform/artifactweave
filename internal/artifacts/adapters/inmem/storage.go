package inmem

import (
	"context"
	"fmt"
	"math"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

var _ port.StorageUsage = repos{}

func (r repos) ProjectStorageBytes(ctx context.Context, key string) (int64, error) {
	return r.storageBytes(ctx, func(repository string) bool {
		value, exists := r.t().Rows[repository]
		return exists && value.ProjectKey == key
	})
}

func (r repos) RepositoryStorageBytes(ctx context.Context, key string) (int64, error) {
	return r.storageBytes(ctx, func(repository string) bool { return repository == key })
}

func (r repos) storageBytes(ctx context.Context, owns func(string) bool) (int64, error) {
	var total int64
	add := func(bytes int64) error {
		if bytes < 0 || bytes > math.MaxInt64-total {
			return fmt.Errorf("invalid logical storage byte total: %w", kernel.ErrStore)
		}
		total += bytes
		return nil
	}
	err := r.s.Do(ctx, func() error {
		for _, item := range items(r).t().Rows {
			if !item.Folder && !item.IsRoot() && owns(item.RepoKey) {
				if err := add(item.Size); err != nil {
					return err
				}
			}
		}
		for _, entry := range remoteRecords(r).entries().Rows {
			if entry.ItemID == 0 && entry.Blob.Checksums.SHA256 != "" && owns(entry.Token.Key.RepoKey) {
				if err := add(entry.Blob.Size); err != nil {
					return err
				}
			}
		}
		for _, copy := range copyRecords(r).rows().Rows {
			if owns(copy.Virtual) {
				if err := add(copy.Item.Size); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}
