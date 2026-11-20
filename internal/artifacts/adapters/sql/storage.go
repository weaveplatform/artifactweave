package sql

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

var _ port.StorageUsage = repos{}

func (r repos) ProjectStorageBytes(ctx context.Context, key string) (int64, error) {
	return r.storageBytes(ctx, " JOIN repositories r ON r.key = i.repo_key", "r.project_key", key)
}

func (r repos) RepositoryStorageBytes(ctx context.Context, key string) (int64, error) {
	return r.storageBytes(ctx, "", "i.repo_key", key)
}

func (r repos) storageBytes(ctx context.Context, join, owner, key string) (int64, error) {
	rawSize := "COALESCE(json_extract(i.doc, '$.blob.Size'), 0)"
	rawItem := "COALESCE(json_extract(i.doc, '$.item_id'), 0)"
	rawDigest := "COALESCE(json_extract(i.doc, '$.blob.Checksums.sha256'), '')"
	if r.s.Dialect().Name == "postgres" {
		rawSize = "COALESCE((i.doc::jsonb #>> '{blob,Size}')::bigint, 0)"
		rawItem = "COALESCE((i.doc::jsonb ->> 'item_id')::bigint, 0)"
		rawDigest = "COALESCE(i.doc::jsonb #>> '{blob,Checksums,sha256}', '')"
	}
	// Separate aggregates avoid an item/cache cross product. Canonical owner
	// and natural-key indexes bound the scans to this project or repository;
	// one scalar result never hydrates configurations, items or cache bodies.
	query := "SELECT CAST(COALESCE(SUM(bytes), 0) AS BIGINT), COALESCE(MIN(bytes), 0) FROM (" +
		"SELECT i.size AS bytes FROM items i" + join + " WHERE " + owner + " = ? AND i.type = 'file' AND i.depth > 0" +
		" UNION ALL SELECT " + rawSize + " AS bytes FROM remote_cache i" + join + " WHERE " + owner + " = ? AND " + rawItem + " = 0 AND " + rawDigest + " <> ''" +
		") logical_storage"
	var total, minimum int64
	err := r.s.Q(ctx).QueryRowContext(ctx, r.s.Dialect().Rebind(query), key, key).Scan(&total, &minimum)
	if err != nil {
		return 0, r.s.Wrap("read logical storage bytes", err)
	}
	if total < 0 || minimum < 0 {
		return 0, fmt.Errorf("negative logical storage bytes: %w", kernel.ErrStore)
	}
	return total, nil
}
