package events

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// CacheStore supplies retrieval state for positive raw representations.
type CacheStore interface {
	Store
	RemoteCache() port.RemoteCacheStore
}

// RawCache captures the positive raw deletion set under the same source guard
// used by publication/invalidation. The caller must retain its transaction;
// a preceding unlocked listing is not an authoritative deletion set.
func RawCache(ctx context.Context, store CacheStore, repository, prefix string) ([]model.RemoteCacheEntry, error) {
	prefix = strings.Trim(prefix, "/")
	if prefix != "" {
		if err := port.ValidateRemoteKey(model.RemoteCacheKey{RepoKey: repository, Path: prefix}); err != nil {
			return nil, fmt.Errorf("capture raw cache path: %w", err)
		}
	}
	if _, err := store.RemoteCache().Source(ctx, repository); err != nil {
		return nil, fmt.Errorf("guard raw cache facts: %w", err)
	}
	var result []model.RemoteCacheEntry
	page := kernel.Page{Limit: 128}
	for {
		entries, next, err := store.RemoteCache().List(ctx, repository, prefix, page)
		if err != nil {
			return nil, fmt.Errorf("capture raw cache facts: %w", err)
		}
		for _, entry := range entries {
			if entry.ItemID == 0 && entry.Blob.Checksums.SHA256 != "" {
				result = append(result, entry)
			}
		}
		if next == "" {
			return result, nil
		}
		page.After = next
	}
}

// EmitRawCache identifies raw content without an item row or retrieval secrets.
func EmitRawCache(ctx context.Context, store Store, topic string, entry model.RemoteCacheEntry, by string, at time.Time) error {
	key := entry.Token.Key
	repository, err := Repository(ctx, store, key.RepoKey)
	if err != nil {
		return err
	}
	facts := api.ArtifactEvent{
		EventMetadata: Metadata(ctx, by),
		Item:          api.EventArtifact{Repository: repository, Path: key.Path, Cached: true, Variant: key.Variant},
		Size:          entry.Blob.Size,
		SHA256:        entry.Blob.Checksums.SHA256,
	}
	return Emit(ctx, store, topic, Key("cache", key.RepoKey, key.Path, key.Variant, key.SettingsRevision), at, facts)
}
