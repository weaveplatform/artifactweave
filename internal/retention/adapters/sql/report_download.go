package sql

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func (c cleanupStore) MarkReportDownloaded(ctx context.Context, id int64) error {
	if err := c.transaction(ctx); err != nil {
		return err
	}
	r, err := c.RunMetadata(ctx, id)
	if err != nil {
		return err
	}
	if !r.Status.Terminal() {
		return fmt.Errorf("cleanup report is not final: %w", kernel.ErrInvalid)
	}
	return c.s.ExecOne(ctx, "mark cleanup report downloaded", "UPDATE cleanup_runs SET report_downloaded = ? WHERE id = ?", true, id)
}
