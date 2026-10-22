package store

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// FenceLayoutSources joins the configuration document's mutation transaction.
// A layout defines both upstream representation and canonical authority, so
// editing its definition must invalidate in-flight publication even when the
// repository's layout reference has not changed.
func FenceLayoutSources(
	ctx context.Context,
	s Store,
	records RemoteCacheRecords,
	name string,
) error {
	repositories, err := s.Repos().List(ctx, RepoFilter{Class: repo.Remote})
	if err != nil {
		return fmt.Errorf("find layout sources: %w", err)
	}
	for _, c := range repositories {
		if c.RepoLayoutRef == "" {
			repo.Defaults(&c)
		}
		if c.RepoLayoutRef != name && c.RemoteRepoLayoutRef != name {
			continue
		}
		state, err := records.Source(ctx, c.Key)
		if err != nil {
			return fmt.Errorf("read layout source: %w", err)
		}
		state.Source.Generation = types.SourceGeneration(rand.Text())
		state.PolicyRevision++
		state.Revision++
		state.ConnectionFailures = 0
		state.OfflineUntil, state.RetryAfterUntil = time.Time{}, time.Time{}
		if err = records.PutSource(ctx, c.Key, state); err != nil {
			return fmt.Errorf("fence layout source: %w", err)
		}
	}
	return nil
}
