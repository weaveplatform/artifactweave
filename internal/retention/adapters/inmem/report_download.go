package inmem

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

func (c cleanupStore) MarkReportDownloaded(ctx context.Context, id int64) error {
	if err := c.transaction(ctx); err != nil {
		return err
	}
	return c.s.Do(ctx, func() error {
		r, found := c.runs().Get(id)
		if !found {
			return memtx.NotFound("cleanup report", id)
		}
		if !r.Status.Terminal() {
			return fmt.Errorf("cleanup report is not final: %w", kernel.ErrInvalid)
		}
		r.ReportDownloaded = true
		c.runs().Put(id, r)
		return nil
	})
}
