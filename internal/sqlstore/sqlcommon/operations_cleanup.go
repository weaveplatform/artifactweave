package sqlcommon

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Cleanup implements store.Store.
func (s *Store) Cleanup() store.CleanupStore { return cleanupStore{s} }

// cleanupStore keeps cleanup_policies and cleanup_runs.
type cleanupStore struct{ s *Store }

const policyCols = "key, description, cron_exp, project_key, item_type, duration_minutes, enabled, " +
	"skip_trashcan, criteria, created_at, created_by, updated_at, updated_by"

func policyArgs(p types.CleanupPolicy) ([]any, error) {
	criteria, err := toJSON(p.Criteria)
	if err != nil {
		return nil, err
	}
	return []any{
		p.Key, p.Description, p.CronExp, p.ProjectKey, p.ItemType, p.DurationMinutes, p.Enabled,
		p.SkipTrashcan, criteria, micros(p.Created), p.CreatedBy, micros(p.Updated), p.UpdatedBy,
	}, nil
}

func scanPolicy(row interface{ Scan(...any) error }) (types.CleanupPolicy, error) {
	var (
		p                types.CleanupPolicy
		criteria         sql.NullString
		created, updated sql.NullInt64
	)
	if err := row.Scan(
		&p.Key,
		&p.Description,
		&p.CronExp,
		&p.ProjectKey,
		&p.ItemType,
		&p.DurationMinutes,
		&p.Enabled,
		&p.SkipTrashcan,
		&criteria,
		&created,
		&p.CreatedBy,
		&updated,
		&p.UpdatedBy,
	); err != nil {
		return p, err //nolint:wrapcheck // wrapped by the caller
	}
	p.Created, p.Updated = fromMicros(created), fromMicros(updated)
	return p, fromJSON(criteria, &p.Criteria)
}

func (c cleanupStore) CreatePolicy(ctx context.Context, p types.CleanupPolicy) error {
	args, err := policyArgs(p)
	if err != nil {
		return err
	}
	_, err = c.s.exec(
		ctx,
		"create cleanup policy "+p.Key,
		Insert("cleanup_policies", opsCols(policyCols)),
		args...)
	return err
}

func (c cleanupStore) Policy(ctx context.Context, key string) (types.CleanupPolicy, error) {
	p, err := scanPolicy(c.s.q(ctx).QueryRowContext(ctx,
		c.s.d.Rebind("SELECT "+policyCols+" FROM cleanup_policies WHERE key = ?"), key))
	return p, c.s.wrap("get cleanup policy "+key, err)
}

func (c cleanupStore) UpdatePolicy(ctx context.Context, p types.CleanupPolicy) error {
	args, err := policyArgs(p)
	if err != nil {
		return err
	}
	sets := make([]string, 0, len(args)-1)
	for _, col := range opsCols(policyCols)[1:] {
		sets = append(sets, col+" = ?")
	}
	return c.s.execOne(
		ctx,
		"update cleanup policy "+p.Key,
		"UPDATE cleanup_policies SET "+strings.Join(
			sets,
			", ",
		)+" WHERE key = ?",
		append(args[1:], p.Key)...)
}

func (c cleanupStore) DeletePolicy(ctx context.Context, key string) error {
	return c.s.execOne(
		ctx,
		"delete cleanup policy "+key,
		"DELETE FROM cleanup_policies WHERE key = ?",
		key,
	)
}

func (c cleanupStore) Policies(
	ctx context.Context,
	f store.PolicyFilter,
) ([]types.CleanupPolicy, error) {
	where := []string{"1 = 1"}
	var args []any
	if f.ItemType != "" {
		where, args = append(where, "item_type = ?"), append(args, f.ItemType)
	}
	if f.ProjectKey != "" {
		where, args = append(where, "project_key = ?"), append(args, f.ProjectKey)
	}
	var out []types.CleanupPolicy
	err := c.s.rows(
		ctx,
		"list cleanup policies",
		"SELECT "+policyCols+" FROM cleanup_policies WHERE "+
			strings.Join(where, " AND ")+" ORDER BY key",
		args,
		func(r *sql.Rows) error {
			p, err := scanPolicy(r)
			out = append(out, p)
			return err
		},
	)
	return out, err
}

const runCols = "policy_key, trigger_id, item_type, status, dry_run, manual, triggered_by, started_at, ended_at, " +
	"total_items, cleaned_items, cleaned_size, report"

func runArgs(r types.CleanupRun) []any {
	return []any{
		r.PolicyKey,
		r.TriggerID,
		r.ItemType,
		r.Status,
		r.DryRun,
		r.Manual,
		r.TriggeredBy,
		micros(
			r.Started,
		),
		micros(r.Ended),
		r.TotalItems,
		r.CleanedItems,
		r.CleanedSize,
		rawJSON(r.Report),
	}
}

func scanRun(row interface{ Scan(...any) error }) (types.CleanupRun, error) {
	var (
		r              types.CleanupRun
		started, ended sql.NullInt64
		report         sql.NullString
	)
	if err := row.Scan(
		&r.ID,
		&r.PolicyKey,
		&r.TriggerID,
		&r.ItemType,
		&r.Status,
		&r.DryRun,
		&r.Manual,
		&r.TriggeredBy,
		&started,
		&ended,
		&r.TotalItems,
		&r.CleanedItems,
		&r.CleanedSize,
		&report,
	); err != nil {
		return r, err //nolint:wrapcheck // wrapped by the caller
	}
	r.Started, r.Ended, r.Report = fromMicros(started), fromMicros(ended), fromRaw(report)
	return r, nil
}

func (c cleanupStore) CreateRun(ctx context.Context, r types.CleanupRun) (types.CleanupRun, error) {
	id, err := c.s.insertID(
		ctx,
		"create cleanup run "+r.PolicyKey,
		Insert("cleanup_runs", opsCols(runCols)),
		runArgs(r)...)
	if err != nil {
		return types.CleanupRun{}, err
	}
	r.ID = id
	return store.Normalise(r), nil
}

func (c cleanupStore) Run(ctx context.Context, id int64) (types.CleanupRun, error) {
	r, err := scanRun(c.s.q(ctx).QueryRowContext(ctx,
		c.s.d.Rebind("SELECT id, "+runCols+" FROM cleanup_runs WHERE id = ?"), id))
	return r, c.s.wrap(fmt.Sprintf("get cleanup run %d", id), err)
}

func (c cleanupStore) UpdateRun(ctx context.Context, r types.CleanupRun) error {
	sets := make([]string, 0, 13)
	for _, col := range opsCols(runCols) {
		sets = append(sets, col+" = ?")
	}
	return c.s.execOne(
		ctx,
		fmt.Sprintf("update cleanup run %d", r.ID),
		"UPDATE cleanup_runs SET "+strings.Join(
			sets,
			", ",
		)+" WHERE id = ?",
		append(runArgs(r), r.ID)...)
}

func (c cleanupStore) Runs(
	ctx context.Context,
	f store.RunFilter,
	p store.Page,
) ([]types.CleanupRun, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	cond, args, err := keyset(p, []string{"id"}, map[string]bool{"id": true}, true)
	if err != nil {
		return nil, "", err
	}
	where := []string{cond}
	if f.PolicyKey != "" {
		where, args = append(where, "policy_key = ?"), append(args, f.PolicyKey)
	}
	if f.ItemType != "" {
		where, args = append(where, "item_type = ?"), append(args, f.ItemType)
	}
	if len(f.Statuses) > 0 {
		where = append(where, "status IN ("+Placeholders(len(f.Statuses))+")")
		for _, st := range f.Statuses {
			args = append(args, st)
		}
	}
	var out []types.CleanupRun
	err = c.s.rows(ctx, "list cleanup runs", "SELECT id, "+runCols+" FROM cleanup_runs WHERE "+
		strings.Join(
			where,
			" AND ",
		)+orderBy([]string{"id"}, true)+" LIMIT ?", append(args, p.Limit+1),
		func(r *sql.Rows) error {
			v, err := scanRun(r)
			out = append(out, v)
			return err
		})
	if err != nil {
		return nil, "", err
	}
	page, next := store.Trim(
		out,
		p.Limit,
		func(r types.CleanupRun) []string { return []string{itoa(r.ID)} },
	)
	return page, next, nil
}

func (c cleanupStore) LastRun(ctx context.Context, policyKey string) (types.CleanupRun, error) {
	r, err := scanRun(c.s.q(ctx).QueryRowContext(ctx, c.s.d.Rebind(
		"SELECT id, "+runCols+" FROM cleanup_runs WHERE policy_key = ? ORDER BY id DESC LIMIT 1",
	), policyKey))
	return r, c.s.wrap("last cleanup run of "+policyKey, err)
}
