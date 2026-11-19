package item

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	artapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	artifactevents "github.com/weaveplatform/artifactweave/internal/artifacts/core/events"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

func (s *Service) emitWritten(ctx context.Context, it model.Item, by string, at time.Time) error {
	topic := artapi.ArtifactDeployed
	if it.Cached {
		topic = artapi.ArtifactCached
	}
	return s.emitArtifact(ctx, topic, it, nil, by, at)
}

func (s *Service) emitArtifact(ctx context.Context, topic string, it model.Item, source *artapi.EventArtifact, by string, at time.Time) error {
	if err := artifactevents.EmitArtifact(ctx, s.Store, topic, it, source, by, at); err != nil {
		return fmt.Errorf("item: stage artifact event: %w", err)
	}
	return nil
}

func (s *Service) propertySnapshot(ctx context.Context, it model.Item) (model.Properties, error) {
	props, err := s.Store.Items().Properties(ctx, it.ID)
	if err != nil {
		return nil, fmt.Errorf("item: properties of %s: %w", it.FullPath(), err)
	}
	return props, nil
}

func (s *Service) emitPropertyChanges(ctx context.Context, it model.Item, before model.Properties, by string, at time.Time) error {
	after, err := s.propertySnapshot(ctx, it)
	if err != nil {
		return err
	}
	changed := map[string]bool{}
	for key, values := range before {
		if !slices.Equal(values, after[key]) {
			changed[key] = true
		}
	}
	for key, values := range after {
		if !slices.Equal(values, before[key]) {
			changed[key] = true
		}
	}
	if len(changed) == 0 {
		return nil
	}
	repository, err := artifactevents.Repository(ctx, s.Store, it.RepoKey)
	if err != nil {
		return fmt.Errorf("item: capture property repository: %w", err)
	}
	for _, key := range slices.Sorted(maps.Keys(changed)) {
		topic := artapi.PropertyAdded
		if _, present := after[key]; !present {
			topic = artapi.PropertyDeleted
		}
		facts := artapi.PropertyEvent{EventMetadata: artifactevents.Metadata(ctx, by), Item: artifactevents.Artifact(repository, it), Property: key}
		if err := artifactevents.Emit(ctx, s.Store, topic, artifactevents.Key("property", strconv.FormatInt(it.ID, 10), key), at, facts); err != nil {
			return fmt.Errorf("item: stage property event: %w", err)
		}
	}
	return nil
}

// Raw metadata has no item row. Its source identity is the non-secret cache
// key, never the upstream URL, retrieval headers or credentials.
func (s *Service) emitRawCached(ctx context.Context, entry model.RemoteCacheEntry, by string, at time.Time) error {
	if err := s.checkRawCacheBlob(ctx, entry.Blob.Checksums.SHA256); err != nil {
		return err
	}
	if err := artifactevents.EmitRawCache(ctx, s.Store, artapi.ArtifactCached, entry, by, at); err != nil {
		return fmt.Errorf("item: stage raw cache event: %w", err)
	}
	return nil
}

func (s *Service) emitRawDeleted(ctx context.Context, entry model.RemoteCacheEntry, by string) error {
	if err := artifactevents.EmitRawCache(ctx, s.Store, artapi.ArtifactDeleted, entry, by, s.now()); err != nil {
		return fmt.Errorf("item: stage raw cache deletion: %w", err)
	}
	return nil
}
