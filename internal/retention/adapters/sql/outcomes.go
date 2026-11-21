package sql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

func (c cleanupStore) OutcomeTotals(ctx context.Context, run int64) (model.CleanupOutcomeTotals, error) {
	if _, err := c.Run(ctx, run); err != nil {
		return model.CleanupOutcomeTotals{}, err
	}
	return c.outcomeTotals(ctx, run)
}

func (c cleanupStore) outcomeTotals(ctx context.Context, run int64) (model.CleanupOutcomeTotals, error) {
	removedSize, selectedSize := c.outcomeField("Removed", "Size"), c.outcomeField("Snapshot", "Size")
	removedHash, selectedHash := c.outcomeField("Removed", "SHA256"), c.outcomeField("Snapshot", "Actual", "sha256")
	facts := `SELECT state, entity_id,
CAST(CASE WHEN state = 'deleted' THEN ` + removedSize + ` ELSE ` + selectedSize + ` END AS BIGINT) AS size,
CASE WHEN state = 'deleted' THEN ` + removedHash + ` ELSE ` + selectedHash + ` END AS hash
FROM cleanup_entries o WHERE run_id = ? AND kind = 'item'`
	content := `SELECT state, COUNT(*) AS items, SUM(size) AS virtual_bytes, MAX(size) AS content_bytes,
CASE WHEN COUNT(size) <> COUNT(*) OR MIN(size) < 0 OR MIN(size) <> MAX(size) THEN 1 ELSE 0 END AS invalid
FROM (` + facts + `) facts
GROUP BY state, CASE WHEN COALESCE(hash, '') = '' THEN 'item:' || CAST(entity_id AS TEXT) ELSE 'hash:' || hash END`
	query := `SELECT state, SUM(items), COALESCE(SUM(virtual_bytes), 0), COALESCE(SUM(content_bytes), 0), SUM(invalid)
FROM (` + content + `) content GROUP BY state`
	var out model.CleanupOutcomeTotals
	err := c.s.Rows(ctx, "aggregate cleanup report outcomes", query, []any{run}, func(row *sql.Rows) error {
		var state model.CleanupEntryState
		var count, virtual, unique, invalid int64
		if err := row.Scan(&state, &count, &virtual, &unique, &invalid); err != nil {
			return fmt.Errorf("scan cleanup report totals: %w", err)
		}
		if invalid != 0 {
			return fmt.Errorf("inconsistent cleanup report content: %w", kernel.ErrStore)
		}
		if err := out.AddGroup(state, count, virtual, unique); err != nil {
			return fmt.Errorf("cleanup report state: %w", err)
		}
		return nil
	})
	if err != nil {
		return model.CleanupOutcomeTotals{}, err
	}
	return out, nil
}

// Every path is an adapter-owned field in the persisted outcome projection.
func (c cleanupStore) outcomeField(path ...string) string {
	if c.s.Dialect().Dollar {
		return "(o.snapshot #>> '{" + strings.Join(path, ",") + "}')"
	}
	return "json_extract(o.snapshot, '$." + strings.Join(path, ".") + "')"
}
