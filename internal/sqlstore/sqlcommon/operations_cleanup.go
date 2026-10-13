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

// Replications implements store.Store.
func (s *Store) Replications() store.ReplicationStore { return replicationStore{s} }

// replicationStore keeps replications and replication_runs.
type replicationStore struct{ s *Store }

// purposeReplicationPassword is the sealing purpose of replications.password.
const purposeReplicationPassword = "replications.password" // #nosec G101 -- a column name, not a credential

const replicationCols = "replication_key, repo_key, url, username, password, enabled, cron_exp, " +
	"event_replication, sync_deletes, sync_properties, sync_statistics, include_prefix, exclude_prefix, " +
	"proxy, disable_proxy, check_binary_existence, socket_timeout_ms"

func (rs replicationStore) scan(row interface{ Scan(...any) error }) (types.Replication, error) {
	var (
		r        types.Replication
		password []byte
		timeout  int64
	)
	if err := row.Scan(
		&r.Key,
		&r.RepoKey,
		&r.URL,
		&r.Username,
		&password,
		&r.Enabled,
		&r.CronExp,
		&r.EventReplication,
		&r.SyncDeletes,
		&r.SyncProperties,
		&r.SyncStatistics,
		&r.IncludePathPrefix,
		&r.ExcludePathPrefix,
		&r.Proxy,
		&r.DisableProxy,
		&r.CheckBinaryExistence,
		&timeout,
	); err != nil {
		return r, err //nolint:wrapcheck // wrapped by the caller
	}
	r.SocketTimeout = fromMillis(timeout)
	var err error
	r.Password, err = rs.s.unseal(purposeReplicationPassword, r.Key, password)
	return r, err
}

func (rs replicationStore) Put(ctx context.Context, r types.Replication) error {
	if r.Key == "" {
		return fmt.Errorf("replication of %s: key is required: %w", r.RepoKey, store.ErrInvalid)
	}
	password, err := rs.s.seal(purposeReplicationPassword, r.Key, r.Password)
	if err != nil {
		return err
	}
	_, err = rs.s.exec(
		ctx,
		"put replication "+r.Key,
		Upsert("replications", opsCols(replicationCols), []string{"replication_key"}),
		r.Key,
		r.RepoKey,
		r.URL,
		r.Username,
		password,
		r.Enabled,
		r.CronExp,
		r.EventReplication,
		r.SyncDeletes,
		r.SyncProperties,
		r.SyncStatistics,
		r.IncludePathPrefix,
		r.ExcludePathPrefix,
		r.Proxy,
		r.DisableProxy,
		r.CheckBinaryExistence,
		millis(r.SocketTimeout),
	)
	return err
}

func (rs replicationStore) Get(ctx context.Context, key string) (types.Replication, error) {
	r, err := rs.scan(rs.s.q(ctx).QueryRowContext(ctx,
		rs.s.d.Rebind("SELECT "+replicationCols+" FROM replications WHERE replication_key = ?"),
		key),
	)
	return r, rs.s.wrap("get replication "+key, err)
}

func (rs replicationStore) list(
	ctx context.Context,
	q string,
	args ...any,
) ([]types.Replication, error) {
	var out []types.Replication
	err := rs.s.rows(ctx, "list replications", q, args, func(r *sql.Rows) error {
		v, err := rs.scan(r)
		out = append(out, v)
		return err
	})
	return out, err
}

func (rs replicationStore) ForRepo(
	ctx context.Context,
	repoKey string,
) ([]types.Replication, error) {
	return rs.list(
		ctx,
		"SELECT "+replicationCols+" FROM replications WHERE repo_key = ? ORDER BY replication_key",
		repoKey,
	)
}

func (rs replicationStore) List(ctx context.Context) ([]types.Replication, error) {
	return rs.list(
		ctx,
		"SELECT "+replicationCols+" FROM replications ORDER BY repo_key, replication_key",
	)
}

func (rs replicationStore) Delete(ctx context.Context, key string) error {
	return rs.s.Tx(ctx, func(ctx context.Context) error {
		if _, err := rs.s.exec(ctx, "delete replication "+key,
			"DELETE FROM replication_runs WHERE replication_key = ?", key); err != nil {
			return err
		}
		return rs.s.execOne(
			ctx,
			"delete replication "+key,
			"DELETE FROM replications WHERE replication_key = ?",
			key,
		)
	})
}

func (rs replicationStore) DeleteForRepo(ctx context.Context, repoKey string) error {
	return rs.s.Tx(ctx, func(ctx context.Context) error {
		if _, err := rs.s.exec(
			ctx,
			"delete replications of "+repoKey,
			"DELETE FROM replication_runs WHERE "+
				"replication_key IN (SELECT replication_key FROM replications WHERE repo_key = ?)",
			repoKey,
		); err != nil {
			return err
		}
		_, err := rs.s.exec(
			ctx,
			"delete replications of "+repoKey,
			"DELETE FROM replications WHERE repo_key = ?",
			repoKey,
		)
		return err
	})
}

func (rs replicationStore) PutRun(ctx context.Context, r types.ReplicationRun) error {
	return rs.s.Tx(ctx, func(ctx context.Context) error {
		ok, err := rs.s.exists(ctx, "find replication "+r.ReplicationKey,
			"SELECT 1 FROM replications WHERE replication_key = ?", r.ReplicationKey)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("replication %s: %w", r.ReplicationKey, store.ErrNotFound)
		}
		_, err = rs.s.exec(ctx, "put replication run "+r.ReplicationKey, Upsert("replication_runs",
			[]string{"replication_key", "started_at", "completed_at", "status"},
			[]string{"replication_key", "started_at"}),
			r.ReplicationKey, r.Started.UnixMicro(), micros(r.Completed), r.Status)
		return err
	})
}

func (rs replicationStore) LastRun(ctx context.Context, key string) (types.ReplicationRun, error) {
	var (
		r                  types.ReplicationRun
		started, completed sql.NullInt64
	)
	err := rs.s.q(ctx).
		QueryRowContext(ctx, rs.s.d.Rebind("SELECT replication_key, started_at, completed_at, status "+
			"FROM replication_runs WHERE replication_key = ? ORDER BY started_at DESC LIMIT 1"), key).
		Scan(&r.ReplicationKey, &started, &completed, &r.Status)
	r.Started, r.Completed = fromMicros(started), fromMicros(completed)
	return r, rs.s.wrap("last replication run of "+key, err)
}
