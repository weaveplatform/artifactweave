package usage

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Capacity measures the volume holding a Location's Root, the same volume
// and the same figures GET /api/storageinfo reports, so the global storage
// quota and the storage summary never disagree about how full it is. It
// satisfies configuration/api.StorageCapacity.
type Capacity struct{ Root string }

// CapacityOf returns the measurable capacity of loc, or nil for object
// storage, whose capacity is unknown.
func CapacityOf(loc Location) *Capacity {
	if loc.Root == "" {
		return nil
	}
	return &Capacity{Root: loc.Root}
}

// Measure reports the volume's total and used bytes. Used is total less the
// space available to the process, as the summary's usedSpace is: blocks a
// filesystem reserves for root can never hold a blob, so counting them as
// free would let the quota admit writes the volume cannot take.
func (c *Capacity) Measure(context.Context) (total, used int64, err error) {
	total, free, ok := diskSpace(c.Root)
	if !ok || total <= 0 || free < 0 || free > total {
		return 0, 0, fmt.Errorf("measure blob volume capacity: %w", kernel.ErrStore)
	}
	return total, total - free, nil
}
