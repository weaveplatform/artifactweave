package item

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// zapped is the update time a zap gives cached items: the epoch, older than
// any retrieval cache period, so the next request for them goes upstream.
var zapped = time.Unix(0, 0).UTC()

// Zap marks the cached items at t.Path and below as expired, so the next
// request for them asks the upstream again (decision 0007). Content is kept.
// It returns how many items it marked; t must name a remote repository's
// cache.
func (s *Service) Zap(ctx context.Context, t Target) (int, error) {
	if !t.Cached {
		return 0, fmt.Errorf("%w: zap applies to a remote repository's cache", ErrConflict)
	}
	full := strings.Trim(t.Path, "/")
	n := 0
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		items, err := s.affected(ctx, t, full)
		if err != nil {
			return err
		}
		for _, it := range items {
			it.Updated = zapped
			if _, err := s.Store.Items().Put(ctx, it); err != nil {
				return fmt.Errorf("item: zap %s: %w", it.FullPath(), err)
			}
		}
		n = len(items)
		return nil
	})
	if err != nil {
		return 0, err //nolint:wrapcheck // the transaction returns ours
	}
	return n, nil
}
