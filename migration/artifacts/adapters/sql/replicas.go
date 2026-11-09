package sql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/mirror"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
)

// Replicas returns the replica index over the blob_replicas table: which
// mirror members hold which blobs (decision 0028).
func (s *Store) Replicas() port.ReplicaIndex { return replicaIndex{s: s, table: "blob_replicas"} }

// replicaIndex is a ReplicaIndex over one table with the blob_replicas
// layout, shared by the hot tier and the archive tier (cold_blob_replicas).
type replicaIndex struct {
	s     *Store
	table string
}

const replicaCols = "sha256, member, state, size, attempts, last_error, updated_at"

// replicaMicros stores an unset time as 0, not NULL (see the migration).
func replicaMicros(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMicro()
}

func scanReplica(row interface{ Scan(...any) error }) (mirror.BlobReplica, error) {
	var (
		r       mirror.BlobReplica
		state   string
		updated int64
	)
	if err := row.Scan(
		&r.SHA256,
		&r.Member,
		&state,
		&r.Size,
		&r.Attempts,
		&r.LastError,
		&updated,
	); err != nil {
		return r, err //nolint:wrapcheck // wrapped by the caller
	}
	r.State = mirror.ReplicaState(state)
	if updated != 0 {
		r.Updated = time.UnixMicro(updated).UTC()
	}
	return r, nil
}

func (r replicaIndex) Put(ctx context.Context, v mirror.BlobReplica) error {
	if v.SHA256 == "" || v.Member == "" {
		return fmt.Errorf("%s: sha256 and member are required: %w", r.table, kernel.ErrInvalid)
	}
	_, err := r.s.Exec(
		ctx,
		"put "+r.table,
		dialect.Upsert(r.table, strings.Split(replicaCols, ", "), []string{"sha256", "member"}),
		v.SHA256,
		v.Member,
		string(v.State),
		v.Size,
		v.Attempts,
		v.LastError,
		replicaMicros(v.Updated),
	)
	return err
}

func (r replicaIndex) Get(ctx context.Context, sha256, member string) (mirror.BlobReplica, error) {
	out, err := scanReplica(r.s.Q(ctx).QueryRowContext(ctx, r.s.Dialect().Rebind(
		"SELECT "+replicaCols+" FROM "+r.table+" WHERE sha256 = ? AND member = ?"), sha256, member))
	return out, r.s.Wrap(fmt.Sprintf("get %s %s@%s", r.table, sha256, member), err)
}

func (r replicaIndex) query(
	ctx context.Context,
	op, q string,
	args ...any,
) ([]mirror.BlobReplica, error) {
	var out []mirror.BlobReplica
	err := r.s.Rows(ctx, op, q, args, func(rs *sql.Rows) error {
		v, err := scanReplica(rs)
		out = append(out, v)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r replicaIndex) ForBlob(ctx context.Context, sha256 string) ([]mirror.BlobReplica, error) {
	return r.query(ctx, "list "+r.table,
		"SELECT "+replicaCols+" FROM "+r.table+" WHERE sha256 = ? ORDER BY member", sha256)
}

var replicaOrder = []string{"updated_at", "sha256", "member"}

func (r replicaIndex) List(
	ctx context.Context, f mirror.ReplicaFilter, p kernel.Page,
) ([]mirror.BlobReplica, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	cond, args, err := dialect.Keyset(p, replicaOrder, map[string]bool{"updated_at": true}, false)
	if err != nil {
		return nil, "", err
	}
	where := []string{cond}
	if f.Member != "" {
		where = append(where, "member = ?")
		args = append(args, f.Member)
	}
	if len(f.States) > 0 {
		where = append(where, "state IN ("+dialect.Placeholders(len(f.States))+")")
		for _, st := range f.States {
			args = append(args, string(st))
		}
	}
	rows, err := r.query(ctx, "list "+r.table,
		"SELECT "+replicaCols+" FROM "+r.table+" WHERE "+strings.Join(where, " AND ")+
			dialect.OrderBy(replicaOrder, false)+" LIMIT ?", append(args, p.Limit+1)...)
	if err != nil {
		return nil, "", err
	}
	page, next := kernel.Trim(rows, p.Limit, func(v mirror.BlobReplica) []string {
		return []string{dialect.Itoa(replicaMicros(v.Updated)), v.SHA256, v.Member}
	})
	return page, next, nil
}

func (r replicaIndex) Delete(ctx context.Context, sha256, member string) error {
	return r.s.ExecOne(ctx, fmt.Sprintf("delete %s %s@%s", r.table, sha256, member),
		"DELETE FROM "+r.table+" WHERE sha256 = ? AND member = ?", sha256, member)
}

func (r replicaIndex) DeleteBlob(ctx context.Context, sha256 string) (int64, error) {
	return r.s.Exec(ctx, "delete "+r.table, "DELETE FROM "+r.table+" WHERE sha256 = ?", sha256)
}

func (r replicaIndex) UnderReplicated(ctx context.Context, redundancy int) (int64, error) {
	var n int64
	err := r.s.Q(ctx).QueryRowContext(ctx, r.s.Dialect().Rebind(
		"SELECT COUNT(*) FROM (SELECT sha256 FROM "+r.table+" GROUP BY sha256"+
			" HAVING SUM(CASE WHEN state = 'deleting' THEN 1 ELSE 0 END) = 0"+
			" AND SUM(CASE WHEN state = 'present' THEN 1 ELSE 0 END) < ?) under"),
		redundancy).Scan(&n)
	return n, r.s.Wrap("under-replicated "+r.table, err)
}

func (r replicaIndex) SoleCopies(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	err := r.s.Rows(ctx, "sole copies "+r.table,
		"SELECT member, COUNT(*) FROM "+r.table+" WHERE state = 'present' AND sha256 IN"+
			" (SELECT sha256 FROM "+r.table+" GROUP BY sha256"+
			" HAVING SUM(CASE WHEN state = 'deleting' THEN 1 ELSE 0 END) = 0"+
			" AND SUM(CASE WHEN state = 'present' THEN 1 ELSE 0 END) = 1)"+
			" GROUP BY member", nil,
		func(rs *sql.Rows) error {
			var (
				member string
				n      int64
			)
			if err := rs.Scan(&member, &n); err != nil {
				return err //nolint:wrapcheck // wrapped by Rows
			}
			out[member] = n
			return nil
		})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r replicaIndex) Counts(ctx context.Context) ([]mirror.ReplicaCount, error) {
	var out []mirror.ReplicaCount
	err := r.s.Rows(ctx, "count "+r.table,
		"SELECT member, state, COUNT(*), COALESCE(SUM(size), 0) FROM "+r.table+
			" GROUP BY member, state ORDER BY member, state", nil,
		func(rs *sql.Rows) error {
			var (
				c     mirror.ReplicaCount
				state string
			)
			if err := rs.Scan(&c.Member, &state, &c.Count, &c.Bytes); err != nil {
				return err //nolint:wrapcheck // wrapped by Rows
			}
			c.State = mirror.ReplicaState(state)
			out = append(out, c)
			return nil
		})
	if err != nil {
		return nil, err
	}
	return out, nil
}
