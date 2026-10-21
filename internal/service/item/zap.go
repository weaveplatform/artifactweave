package item

import (
	"context"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// Zap expires upstream metadata and clears negative state while retaining all
// complete bytes. Unique and mutable binaries keep their timestamps and cache
// state. The next normal metadata read refreshes; cache aliases never fetch.
func (s *Service) Zap(ctx context.Context, t Target) (int, error) {
	if !t.Cached || t.Repo.Class != repo.Remote {
		return 0, fmt.Errorf("%w: zap applies to a remote repository's cache", ErrConflict)
	}
	full := strings.Trim(t.Path, "/")
	var n int64
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		var err error
		n, err = s.Store.RemoteCache().Invalidate(ctx, t.Repo.Key, full, true)
		if err != nil {
			return fmt.Errorf("item: zap state: %w", err)
		}
		if n == 0 && full != "" {
			// Existing legacy cached content is already unvalidated when it lacks a
			// retrieval record. Keep the historical missing-path status.
			if _, err := s.Store.Items().Get(ctx, t.Repo.Key, true, full); err != nil {
				return fmt.Errorf("item: zap path: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("item: zap transaction: %w", err)
	}
	return int(n), nil
}

// invalidateCache joins item mutations to the durable retrieval fence.
func (s *Service) invalidateCache(ctx context.Context, t Target) (int64, error) {
	if !t.Cached || t.Repo.Class != repo.Remote {
		return 0, nil
	}
	n, err := s.Store.RemoteCache().Invalidate(ctx, t.Repo.Key, strings.Trim(t.Path, "/"), false)
	if err != nil {
		return 0, fmt.Errorf("item: invalidate cache: %w", err)
	}
	return n, nil
}
