package sqlcommon

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

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
	r.SocketTimeout = dialect.FromMillis(timeout)
	var err error
	r.Password, err = rs.s.Unseal(purposeReplicationPassword, r.Key, password)
	return r, err
}

func (rs replicationStore) Put(ctx context.Context, r types.Replication) error {
	if r.Key == "" {
		return fmt.Errorf("replication of %s: key is required: %w", r.RepoKey, store.ErrInvalid)
	}
	password, err := rs.s.Seal(purposeReplicationPassword, r.Key, r.Password)
	if err != nil {
		return err
	}
	_, err = rs.s.Exec(
		ctx,
		"put replication "+r.Key,
		Upsert("replications", dialect.OpsCols(replicationCols), []string{"replication_key"}),
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
		dialect.Millis(r.SocketTimeout),
	)
	return err
}

func (rs replicationStore) Get(ctx context.Context, key string) (types.Replication, error) {
	r, err := rs.scan(rs.s.Q(ctx).QueryRowContext(ctx,
		rs.s.Dialect().
			Rebind("SELECT "+replicationCols+" FROM replications WHERE replication_key = ?"),
		key),
	)
	return r, rs.s.Wrap("get replication "+key, err)
}

func (rs replicationStore) list(
	ctx context.Context,
	q string,
	args ...any,
) ([]types.Replication, error) {
	var out []types.Replication
	err := rs.s.Rows(ctx, "list replications", q, args, func(r *sql.Rows) error {
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
		if _, err := rs.s.Exec(ctx, "delete replication "+key,
			"DELETE FROM replication_runs WHERE replication_key = ?", key); err != nil {
			return err
		}
		return rs.s.ExecOne(
			ctx,
			"delete replication "+key,
			"DELETE FROM replications WHERE replication_key = ?",
			key,
		)
	})
}

func (rs replicationStore) DeleteForRepo(ctx context.Context, repoKey string) error {
	return rs.s.Tx(ctx, func(ctx context.Context) error {
		if _, err := rs.s.Exec(
			ctx,
			"delete replications of "+repoKey,
			"DELETE FROM replication_runs WHERE "+
				"replication_key IN (SELECT replication_key FROM replications WHERE repo_key = ?)",
			repoKey,
		); err != nil {
			return err
		}
		_, err := rs.s.Exec(
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
		ok, err := rs.s.Exists(ctx, "find replication "+r.ReplicationKey,
			"SELECT 1 FROM replications WHERE replication_key = ?", r.ReplicationKey)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("replication %s: %w", r.ReplicationKey, store.ErrNotFound)
		}
		_, err = rs.s.Exec(ctx, "put replication run "+r.ReplicationKey, Upsert("replication_runs",
			[]string{"replication_key", "started_at", "completed_at", "status"},
			[]string{"replication_key", "started_at"}),
			r.ReplicationKey, r.Started.UnixMicro(), dialect.Micros(r.Completed), r.Status)
		return err
	})
}

func (rs replicationStore) LastRun(ctx context.Context, key string) (types.ReplicationRun, error) {
	var (
		r                  types.ReplicationRun
		started, completed sql.NullInt64
	)
	err := rs.s.Q(ctx).
		QueryRowContext(ctx, rs.s.Dialect().Rebind("SELECT replication_key, started_at, completed_at, status "+
			"FROM replication_runs WHERE replication_key = ? ORDER BY started_at DESC LIMIT 1"), key).
		Scan(&r.ReplicationKey, &started, &completed, &r.Status)
	r.Started, r.Completed = dialect.FromMicros(started), dialect.FromMicros(completed)
	return r, rs.s.Wrap("last replication run of "+key, err)
}
