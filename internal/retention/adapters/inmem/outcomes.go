package inmem

import (
	"context"
	"fmt"
	"math"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

func (c cleanupStore) OutcomeTotals(ctx context.Context, run int64) (model.CleanupOutcomeTotals, error) {
	var out model.CleanupOutcomeTotals
	err := c.s.Do(ctx, func() error {
		if !c.runs().Has(run) {
			return memtx.NotFound("cleanup report run", run)
		}
		type contentKey struct {
			state model.CleanupEntryState
			hash  string
			id    int64
		}
		type contentGroup struct{ count, virtual, size int64 }
		groups := map[contentKey]contentGroup{}
		for _, entry := range c.entries().All(func(k cleanupEntryKey, _ model.CleanupEntry) bool {
			return k.run == run && k.kind == model.CleanupItemEntry
		}, nil) {
			size, hash := entry.Snapshot.Size, entry.Snapshot.Actual.SHA256
			if entry.State == model.CleanupDeleted {
				if entry.Removed == nil {
					return fmt.Errorf("cleanup report effect is missing: %w", kernel.ErrStore)
				}
				size, hash = entry.Removed.Size, entry.Removed.SHA256
			}
			key := contentKey{state: entry.State, hash: hash}
			if hash == "" {
				key.id = entry.EntityID
			}
			group, exists := groups[key]
			if size < 0 || exists && group.size != size || size > math.MaxInt64-group.virtual {
				return fmt.Errorf("inconsistent cleanup report content: %w", kernel.ErrStore)
			}
			group.count++
			group.virtual, group.size = group.virtual+size, size
			groups[key] = group
		}
		type stateGroup struct{ count, virtual, content int64 }
		states := map[model.CleanupEntryState]stateGroup{}
		for key, group := range groups {
			state := states[key.state]
			if group.virtual > math.MaxInt64-state.virtual || group.size > math.MaxInt64-state.content {
				return fmt.Errorf("cleanup report bytes overflow: %w", kernel.ErrStore)
			}
			state.count += group.count
			state.virtual += group.virtual
			state.content += group.size
			states[key.state] = state
		}
		for state, group := range states {
			if err := out.AddGroup(state, group.count, group.virtual, group.content); err != nil {
				return fmt.Errorf("cleanup report state: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return model.CleanupOutcomeTotals{}, err
	}
	return out, nil
}
