package sql

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

var reportCols = []string{
	"id", "action", "project_key", "name", "build_id", "report", "created_at", "expires_at",
}

func (b buildStore) PutReport(ctx context.Context, r model.BuildReport) (int64, error) {
	if err := port.CheckBuildReport(r); err != nil {
		return 0, fmt.Errorf("build report of %s %s/%s: %w", r.Action, r.ProjectKey, r.Name, err)
	}
	return b.s.InsertID(
		ctx,
		fmt.Sprintf("store build report of %s %s/%s", r.Action, r.ProjectKey, r.Name),
		dialect.Insert("build_reports", reportCols[1:]),
		r.Action,
		r.ProjectKey,
		r.Name,
		r.BuildID,
		string(r.Report),
		r.Created.UnixMicro(),
		r.Expires.UnixMicro(),
	)
}

func (b buildStore) ReportForUpdate(ctx context.Context, id int64) (model.BuildReport, error) {
	if !b.s.InTx(ctx) {
		return model.BuildReport{}, fmt.Errorf(
			"get build report for update requires a transaction: %w",
			kernel.ErrInvalid,
		)
	}
	var (
		r                model.BuildReport
		report           string
		created, expires int64
	)
	err := b.s.Q(ctx).QueryRowContext(ctx, b.s.Dialect().Rebind(
		"SELECT "+strings.Join(reportCols, ", ")+" FROM build_reports WHERE id = ? "+
			b.s.Dialect().ForUpdate,
	), id).Scan(&r.ID, &r.Action, &r.ProjectKey, &r.Name, &r.BuildID, &report, &created, &expires)
	if err != nil {
		return model.BuildReport{}, b.s.Wrap(fmt.Sprintf("lock build report %d", id), err)
	}
	r.Report = []byte(report)
	r.Created, r.Expires = time.UnixMicro(created).UTC(), time.UnixMicro(expires).UTC()
	return r, nil
}

func (b buildStore) PruneReports(ctx context.Context, now time.Time, limit int) (int64, error) {
	if limit < 1 || limit > kernel.MaxLimit {
		return 0, fmt.Errorf("build report prune limit: %w", kernel.ErrInvalid)
	}
	// The subquery bounds the delete, which neither SQLite nor PostgreSQL
	// allows a LIMIT on directly.
	return b.s.Exec(
		ctx,
		"prune build reports",
		"DELETE FROM build_reports WHERE id IN ("+
			"SELECT id FROM build_reports WHERE expires_at <= ? ORDER BY expires_at, id LIMIT ?)",
		now.UnixMicro(),
		limit,
	)
}
