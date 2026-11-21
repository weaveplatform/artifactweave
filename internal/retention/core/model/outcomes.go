package model

import (
	"fmt"
	"math"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// CleanupOutcomeTotals describes persisted item outcomes, excluding separate
// version decisions. Content bytes count each checksum once within its outcome;
// virtual bytes count each item. Neither claims that garbage collection ran.
type CleanupOutcomeTotals struct {
	Items, Pending, Selected, Deleted, Skipped, Failed int64
	SelectedVirtualBytes, SelectedContentBytes         int64
	DeletedVirtualBytes, DeletedContentBytes           int64
}

// AddGroup folds one already-deduplicated outcome group without overflowing
// counters or accepting an unknown state as successful package work.
func (t *CleanupOutcomeTotals) AddGroup(state CleanupEntryState, count, virtualBytes, contentBytes int64) error {
	var items *int64
	var virtualTotal, contentTotal *int64
	switch state {
	case CleanupPending:
		items = &t.Pending
	case CleanupSelected:
		items, virtualTotal, contentTotal = &t.Selected, &t.SelectedVirtualBytes, &t.SelectedContentBytes
	case CleanupDeleted:
		items, virtualTotal, contentTotal = &t.Deleted, &t.DeletedVirtualBytes, &t.DeletedContentBytes
	case CleanupSkipped:
		items = &t.Skipped
	case CleanupEntryFailed:
		items = &t.Failed
	default:
		return fmt.Errorf("unknown cleanup report outcome: %w", kernel.ErrStore)
	}
	if count < 0 || virtualBytes < 0 || contentBytes < 0 || count > math.MaxInt64-t.Items || count > math.MaxInt64-*items {
		return fmt.Errorf("cleanup report totals overflow: %w", kernel.ErrStore)
	}
	if contentBytes > virtualBytes || count == 0 && virtualBytes != 0 {
		return fmt.Errorf("inconsistent cleanup report group: %w", kernel.ErrStore)
	}
	if virtualTotal != nil {
		if virtualBytes > math.MaxInt64-*virtualTotal || contentBytes > math.MaxInt64-*contentTotal {
			return fmt.Errorf("cleanup report bytes overflow: %w", kernel.ErrStore)
		}
		*virtualTotal += virtualBytes
		*contentTotal += contentBytes
	}
	t.Items += count
	*items += count
	return nil
}
