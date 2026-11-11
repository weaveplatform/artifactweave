package sql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

// Archive returns the archive policy and execution store (decision
// 0024).
func (s *Store) Archive() port.ArchiveStore { return archiveStore{s} }

type archiveStore struct{ s *Store }

var archivePolicyCols = []string{
	"key", "family", "description", "enabled", "cron_exp", "project_key", "duration_minutes",
	"expiration_months", "skip_trashcan", "created_at", "updated_at", "criteria",
}

func archivePolicyRow(p model.ArchivePolicy) []any {
	return []any{
		p.Key,
		string(p.Family),
		p.Description,
		p.Enabled,
		p.CronExp,
		p.ProjectKey,
		p.DurationMinutes,
		p.ExpirationMonths,
		p.SkipTrashcan,
		dialect.Micros(p.Created),
		dialect.Micros(p.Updated),
		dialect.RawJSON(p.Criteria),
	}
}

func scanArchivePolicy(r dialect.EntRow) (model.ArchivePolicy, error) {
	var (
		p                model.ArchivePolicy
		family           string
		created, updated sql.NullInt64
		criteria         sql.NullString
	)
	if err := r.Scan(
		&p.Key,
		&family,
		&p.Description,
		&p.Enabled,
		&p.CronExp,
		&p.ProjectKey,
		&p.DurationMinutes,
		&p.ExpirationMonths,
		&p.SkipTrashcan,
		&created,
		&updated,
		&criteria,
	); err != nil {
		return p, err //nolint:wrapcheck // wrapped by the caller
	}
	p.Family, p.Created, p.Updated = model.ArchiveFamily(
		family,
	), dialect.FromMicros(
		created,
	), dialect.FromMicros(
		updated,
	)
	p.Criteria = dialect.FromRaw(criteria)
	return p, nil
}

func (a archiveStore) CreatePolicy(ctx context.Context, p model.ArchivePolicy) error {
	_, err := a.s.Exec(ctx, "create archive policy "+p.Key,
		dialect.Insert("archive_policies", archivePolicyCols), archivePolicyRow(p)...)
	return err
}

func (a archiveStore) Policy(ctx context.Context, key string) (model.ArchivePolicy, error) {
	out, err := scanArchivePolicy(a.s.Q(ctx).QueryRowContext(ctx, a.s.Dialect().Rebind(
		"SELECT "+strings.Join(
			archivePolicyCols,
			", ",
		)+" FROM archive_policies WHERE key = ?",
	), key))
	return out, a.s.Wrap("get archive policy "+key, err)
}

func (a archiveStore) UpdatePolicy(ctx context.Context, p model.ArchivePolicy) error {
	sets := make([]string, 0, len(archivePolicyCols)-1)
	for _, c := range archivePolicyCols[1:] {
		sets = append(sets, c+" = ?")
	}
	row := archivePolicyRow(p)
	return a.s.ExecOne(
		ctx,
		"update archive policy "+p.Key,
		"UPDATE archive_policies SET "+strings.Join(
			sets,
			", ",
		)+" WHERE key = ?",
		append(row[1:], p.Key)...)
}

func (a archiveStore) DeletePolicy(ctx context.Context, key string) error {
	return a.s.ExecOne(
		ctx,
		"delete archive policy "+key,
		"DELETE FROM archive_policies WHERE key = ?",
		key,
	)
}

func (a archiveStore) Policies(
	ctx context.Context,
	family model.ArchiveFamily,
) ([]model.ArchivePolicy, error) {
	q := "SELECT " + strings.Join(archivePolicyCols, ", ") + " FROM archive_policies"
	var args []any
	if family != "" {
		q += " WHERE family = ?"
		args = append(args, string(family))
	}
	var out []model.ArchivePolicy
	err := a.s.Rows(ctx, "list archive policies", q+" ORDER BY key", args, func(r *sql.Rows) error {
		v, err := scanArchivePolicy(r)
		out = append(out, v)
		return err
	})
	return out, err
}

var executionCols = []string{
	"policy_key", "kind", "state", "started_at", "finished_at", "total", "completed", "failed",
	"total_bytes", "success_bytes", "failure_bytes", "triggered_by", "destination",
}

func executionRow(e model.ArchiveExecution) []any {
	return []any{
		e.PolicyKey,
		string(e.Kind),
		string(e.State),
		dialect.Micros(e.Started),
		dialect.Micros(e.Finished),
		e.Total,
		e.Completed,
		e.Failed,
		e.TotalBytes,
		e.SuccessBytes,
		e.FailureBytes,
		e.TriggeredBy,
		dialect.RawJSON(e.Destination),
	}
}

func scanExecution(r dialect.EntRow) (model.ArchiveExecution, error) {
	var (
		e                 model.ArchiveExecution
		kind, state       string
		started, finished sql.NullInt64
		destination       sql.NullString
	)
	if err := r.Scan(
		&e.ID,
		&e.PolicyKey,
		&kind,
		&state,
		&started,
		&finished,
		&e.Total,
		&e.Completed,
		&e.Failed,
		&e.TotalBytes,
		&e.SuccessBytes,
		&e.FailureBytes,
		&e.TriggeredBy,
		&destination,
	); err != nil {
		return e, err //nolint:wrapcheck // wrapped by the caller
	}
	e.Kind, e.State = model.ExecutionKind(kind), model.ExecutionState(state)
	e.Started, e.Finished, e.Destination = dialect.FromMicros(
		started,
	), dialect.FromMicros(
		finished,
	), dialect.FromRaw(
		destination,
	)
	return e, nil
}

func (a archiveStore) CreateExecution(
	ctx context.Context,
	e model.ArchiveExecution,
) (model.ArchiveExecution, error) {
	e = kernel.Normalise(e)
	id, err := a.s.InsertID(ctx, "create archive execution",
		dialect.Insert("archive_executions", executionCols), executionRow(e)...)
	if err != nil {
		return model.ArchiveExecution{}, err
	}
	e.ID = id
	return e, nil
}

func (a archiveStore) Execution(ctx context.Context, id int64) (model.ArchiveExecution, error) {
	out, err := scanExecution(a.s.Q(ctx).QueryRowContext(ctx, a.s.Dialect().Rebind(
		"SELECT id, "+strings.Join(
			executionCols,
			", ",
		)+" FROM archive_executions WHERE id = ?",
	), id))
	return out, a.s.Wrap(fmt.Sprintf("get archive execution %d", id), err)
}

func (a archiveStore) UpdateExecution(ctx context.Context, e model.ArchiveExecution) error {
	sets := make([]string, 0, len(executionCols))
	for _, c := range executionCols {
		sets = append(sets, c+" = ?")
	}
	return a.s.ExecOne(ctx, fmt.Sprintf("update archive execution %d", e.ID),
		"UPDATE archive_executions SET "+strings.Join(sets, ", ")+" WHERE id = ?",
		append(executionRow(e), e.ID)...)
}

func (a archiveStore) Executions(
	ctx context.Context, f port.ExecutionFilter, p kernel.Page,
) ([]model.ArchiveExecution, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	cond, args, err := dialect.Keyset(p, []string{"id"}, map[string]bool{"id": true}, true)
	if err != nil {
		return nil, "", err
	}
	where := []string{cond}
	if f.PolicyKey != "" {
		where = append(where, "policy_key = ?")
		args = append(args, f.PolicyKey)
	}
	if f.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, string(f.Kind))
	}
	var out []model.ArchiveExecution
	err = a.s.Rows(ctx, "list archive executions",
		"SELECT id, "+strings.Join(executionCols, ", ")+" FROM archive_executions WHERE "+
			strings.Join(where, " AND ")+" ORDER BY id DESC LIMIT ?",
		append(args, p.Limit+1),
		func(r *sql.Rows) error {
			v, err := scanExecution(r)
			out = append(out, v)
			return err
		})
	if err != nil {
		return nil, "", err
	}
	page, next := kernel.Trim(
		out,
		p.Limit,
		func(v model.ArchiveExecution) []string { return []string{dialect.Itoa(v.ID)} },
	)
	return page, next, nil
}
