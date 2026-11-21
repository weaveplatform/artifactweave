package inmem

import (
	"fmt"
	"math"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

// The reference backend already holds entries in memory. One pass computes
// relevant content sizes before sorting, rather than rescanning per run.
func (c cleanupStore) runContent(rows []model.CleanupRunSnapshot) error {
	if len(rows) == 0 {
		return nil
	}
	indices := make(map[int64]int, len(rows))
	for i, row := range rows {
		indices[row.Run.ID] = i
	}
	type contentKey struct {
		run, id int64
		hash    string
	}
	seen := map[contentKey]int64{}
	for key, entry := range c.entries().Rows {
		i, visible := indices[key.run]
		if !visible || key.kind != model.CleanupItemEntry || entry.State != model.CleanupDeleted {
			continue
		}
		if entry.Removed == nil || entry.Removed.Size < 0 {
			return fmt.Errorf("invalid cleanup report content: %w", kernel.ErrStore)
		}
		content := contentKey{run: key.run, hash: entry.Removed.SHA256}
		if content.hash == "" {
			content.id = entry.EntityID
		}
		if size, found := seen[content]; found {
			if size != entry.Removed.Size {
				return fmt.Errorf("inconsistent cleanup report content: %w", kernel.ErrStore)
			}
			continue
		}
		if entry.Removed.Size > math.MaxInt64-rows[i].CleanedContentBytes {
			return fmt.Errorf("cleanup report content size overflow: %w", kernel.ErrStore)
		}
		seen[content] = entry.Removed.Size
		rows[i].CleanedContentBytes += entry.Removed.Size
	}
	return nil
}
