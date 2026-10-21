package item

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	KindCacheCleanupSchedule = "remote.cache-cleanup-schedule"
	KindCacheCleanup         = "remote.cache-cleanup"
	CacheCleanupInterval     = time.Hour
	CacheCleanupBatch        = 128
)

type cacheCleanupCursor struct {
	Repository  string                      `json:"repository"`
	Incarnation types.RepositoryIncarnation `json:"incarnation"`
	Items       bool                        `json:"items"`
	After       string                      `json:"after"`
}

// RegisterCacheMaintenance registers a periodic scheduler and bounded worker.
// Existing queued pages survive restart. The scheduler repeats; each repository
// worker drains pages without waiting another hour between batches.
func (s *Service) RegisterCacheMaintenance(ctx context.Context, q *work.Queue) error {
	if err := q.HandleEvery(
		KindCacheCleanupSchedule,
		CacheCleanupInterval,
		s.scheduleCacheCleanup,
	); err != nil {
		return fmt.Errorf("item: register cache scheduler: %w", err)
	}
	if err := q.Handle(KindCacheCleanup, s.cacheCleanupWork); err != nil {
		return fmt.Errorf("item: register cache cleanup: %w", err)
	}
	return s.cacheMaintenanceTx(ctx, func(ctx context.Context) error {
		_, err := s.Store.WorkQueue().Get(ctx, KindCacheCleanupSchedule, "scan")
		if errors.Is(err, store.ErrNotFound) {
			return cacheMaintenanceError(
				s.Store.WorkQueue().Enqueue(ctx, KindCacheCleanupSchedule, "scan", nil, s.now()),
			)
		}
		return cacheMaintenanceError(err)
	})
}

func (s *Service) scheduleCacheCleanup(ctx context.Context, _ types.WorkItem) error {
	configs, err := s.Store.Repos().List(ctx, store.RepoFilter{Class: repo.Remote})
	if err != nil {
		return fmt.Errorf("item: list cleanup repositories: %w", err)
	}
	for _, c := range configs {
		if !cacheCleanupEnabled(c) {
			continue
		}
		if err := s.Store.Tx(ctx, func(ctx context.Context) error {
			// Never overwrite the cursor of a pending or leased batch.
			if _, err := s.Store.WorkQueue().Get(ctx, KindCacheCleanup, c.Key); err == nil {
				return nil
			} else if !errors.Is(err, store.ErrNotFound) {
				return cacheMaintenanceError(err)
			}
			identity, err := s.Store.RemoteCache().Source(ctx, c.Key)
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			if err != nil {
				return cacheMaintenanceError(err)
			}
			return s.enqueueCacheCleanup(
				ctx,
				cacheCleanupCursor{Repository: c.Key, Incarnation: identity.Repository},
			)
		}); err != nil {
			return fmt.Errorf("item: schedule cache cleanup: %w", err)
		}
	}
	return cacheMaintenanceError(
		s.Store.WorkQueue().Enqueue(ctx, KindCacheCleanupSchedule, "scan", nil, s.now()),
	)
}

func cacheCleanupEnabled(c repo.Config) bool {
	return c.Class == repo.Remote && c.UnusedArtifactsCleanupPeriodHours != nil &&
		*c.UnusedArtifactsCleanupPeriodHours > 0
}

func (s *Service) enqueueCacheCleanup(ctx context.Context, cursor cacheCleanupCursor) error {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return fmt.Errorf("item: encode cache cleanup: %w", err)
	}
	if err = s.Store.WorkQueue().
		Enqueue(ctx, KindCacheCleanup, cursor.Repository, payload, s.now()); err != nil {
		return fmt.Errorf("item: enqueue cache cleanup: %w", err)
	}
	return nil
}

func (s *Service) cacheCleanupWork(ctx context.Context, it types.WorkItem) error {
	var cursor cacheCleanupCursor
	if err := json.Unmarshal(it.Payload, &cursor); err != nil {
		return fmt.Errorf("item: decode cache cleanup: %w", err)
	}
	c, err := s.currentCleanupRepo(ctx, cursor)
	if err != nil || c.Key == "" {
		return cacheMaintenanceError(err)
	}
	page := store.Page{Limit: CacheCleanupBatch, After: cursor.After}
	var next string
	if cursor.Items {
		var items []types.Item
		items, next, err = s.Store.Items().Descendants(ctx, c.Key, true, "", page)
		if err == nil {
			for _, item := range items {
				if err = s.cleanupCachedItem(ctx, cursor, item); err != nil {
					break
				}
			}
		}
	} else {
		var entries []types.RemoteCacheEntry
		entries, next, err = s.Store.RemoteCache().List(ctx, c.Key, "", page)
		if err == nil {
			for _, entry := range entries {
				if err = s.cleanupCacheEntry(ctx, cursor, entry); err != nil {
					break
				}
			}
		}
	}
	if err != nil {
		return fmt.Errorf("item: cache cleanup batch: %w", err)
	}
	if next == "" {
		if cursor.Items {
			return nil
		}
		cursor.Items = true
	}
	cursor.After = next
	// Recheck zero/deletion before requesting another page. Even a queued page
	// rechecks on entry and before each deletion, so disabling cannot revive it.
	if c, err = s.currentCleanupRepo(ctx, cursor); err != nil || c.Key == "" {
		return cacheMaintenanceError(err)
	}
	return s.enqueueCacheCleanup(ctx, cursor)
}

func (s *Service) currentCleanupRepo(
	ctx context.Context,
	cursor cacheCleanupCursor,
) (repo.Config, error) {
	identity, err := s.Store.RemoteCache().Source(ctx, cursor.Repository)
	if errors.Is(err, store.ErrNotFound) {
		return repo.Config{}, nil
	}
	if err != nil {
		return repo.Config{}, fmt.Errorf("item: cleanup identity: %w", err)
	}
	if identity.Repository != cursor.Incarnation {
		return repo.Config{}, nil
	}
	c, err := s.Store.Repos().Get(ctx, cursor.Repository)
	if err != nil {
		return repo.Config{}, fmt.Errorf("item: cleanup config: %w", err)
	}
	if !cacheCleanupEnabled(c) {
		return repo.Config{}, nil
	}
	return c, nil
}

func cleanupUnused(c repo.Config, now, used time.Time) bool {
	if used.IsZero() || used.After(now) {
		return false
	}
	// Compare hours without overflowing time.Duration for a large accepted int.
	return now.Sub(used).Hours() >= float64(*c.UnusedArtifactsCleanupPeriodHours)
}

func latestUse(times ...time.Time) time.Time {
	var newest time.Time
	for _, at := range times {
		if at.After(newest) {
			newest = at
		}
	}
	return newest
}

func (s *Service) cleanupCacheEntry(
	ctx context.Context,
	cursor cacheCleanupCursor,
	candidate types.RemoteCacheEntry,
) error {
	return s.cacheMaintenanceTx(ctx, func(ctx context.Context) error {
		c, err := s.currentCleanupRepo(ctx, cursor)
		if err != nil || c.Key == "" {
			return cacheMaintenanceError(err)
		}
		token, err := s.Store.RemoteCache().Begin(ctx, candidate.Token.Key)
		if err != nil {
			return cacheMaintenanceError(err)
		}
		// A newer completion wins over this page's snapshot. Activity is read
		// again below under the same source lock; Touch does not revise content.
		if token.Epoch != candidate.Token.Epoch || token.Revision != candidate.Token.Revision {
			return nil
		}
		e, err := s.Store.RemoteCache().Read(ctx, token.Key)
		if errors.Is(err, store.ErrNotFound) {
			if e.ItemID != 0 {
				return s.Store.RemoteCache().Evict(ctx, token)
			}
			return nil // durable empty checkpoint; never recycle its epoch
		}
		if err != nil {
			return cacheMaintenanceError(err)
		}
		if e.ItemID != 0 {
			return nil
		} // the item pass coalesces all variants' usage
		if e.Blob.Checksums.SHA256 == "" {
			if !e.NegativeUntil.After(s.now()) {
				return s.Store.RemoteCache().Evict(ctx, token)
			}
			return nil
		}
		if !cleanupUnused(c, s.now(), latestUse(e.LastUsed, e.FetchedAt)) {
			return nil
		}
		return s.Store.RemoteCache().Evict(ctx, token)
	})
}

func (s *Service) cleanupCachedItem(
	ctx context.Context,
	cursor cacheCleanupCursor,
	candidate types.Item,
) error {
	if candidate.Folder {
		return nil
	}
	return s.cacheMaintenanceTx(ctx, func(ctx context.Context) error {
		c, err := s.currentCleanupRepo(ctx, cursor)
		if err != nil || c.Key == "" {
			return cacheMaintenanceError(err)
		}
		item, err := s.Store.Items().GetByID(ctx, candidate.ID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return cacheMaintenanceError(err)
		}
		if !item.Cached || item.RepoKey != c.Key || item.Actual != candidate.Actual {
			return nil
		}
		if format.ClassOf(s.format(c.PackageType), c, item.FullPath()) == format.ClassDerived {
			return nil
		}
		unused, err := s.cachedItemUnused(ctx, c, item)
		if err != nil || !unused {
			return cacheMaintenanceError(err)
		}
		// Deleting an owner also removes its sidecars. A recently downloaded
		// signature must not disappear merely because its owner is older.
		sidecars, err := s.sidecarsOf(ctx, c, true, []types.Item{item})
		if err != nil {
			return err
		}
		for _, sidecar := range sidecars {
			sidecar, err = s.Store.Items().GetByID(ctx, sidecar.ID)
			if err != nil {
				return cacheMaintenanceError(err)
			}
			unused, err = s.cachedItemUnused(ctx, c, sidecar)
			if err != nil || !unused {
				return cacheMaintenanceError(err)
			}
		}
		_, err = s.Delete(
			ctx,
			Target{Repo: c, Cached: true, Path: item.FullPath()},
			"remote-cache-cleanup",
		)
		return cacheMaintenanceError(err)
	})
}

// cachedItemUnused requires the item row lock and enclosing source lock. It
// reads representations in bounded pages without losing activity in a later page.
func (s *Service) cachedItemUnused(
	ctx context.Context,
	c repo.Config,
	item types.Item,
) (bool, error) {
	stats, err := s.Store.Items().Stats(ctx, item.ID)
	if err != nil {
		return false, cacheMaintenanceError(err)
	}
	used := latestUse(item.Created, stats.LastDownloaded)
	// A path's representations share an item slot. Any recent representation
	// protects the item; raw metadata is independently evicted in the first pass.
	page := store.Page{Limit: CacheCleanupBatch}
	for {
		entries, next, err := s.Store.RemoteCache().List(ctx, c.Key, item.FullPath(), page)
		if err != nil {
			return false, cacheMaintenanceError(err)
		}
		for _, e := range entries {
			if e.Token.Key.Path == item.FullPath() {
				used = latestUse(used, e.LastUsed, e.FetchedAt)
			}
		}
		if !cleanupUnused(c, s.now(), used) {
			return false, nil
		}
		if next == "" {
			break
		}
		page.After = next
	}
	return true, nil
}

func cacheMaintenanceError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("item: cache maintenance: %w", err)
}

func (s *Service) cacheMaintenanceTx(ctx context.Context, run func(context.Context) error) error {
	return cacheMaintenanceError(s.Store.Tx(ctx, run))
}
