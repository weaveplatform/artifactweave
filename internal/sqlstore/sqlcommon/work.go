package sqlcommon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// WorkQueue implements store.Store.
func (s *Store) WorkQueue() store.WorkQueue { return workQueue{s} }

// workQueue keeps the work_items table (decision 0033).
type workQueue struct{ s *Store }

const workCols = "kind, key, payload, state, attempts, generation, not_before, leased_by, " +
	"lease_until, dirty_since, last_error, enqueued_at, updated_at"

func scanWork(r interface{ Scan(...any) error }) (types.WorkItem, error) {
	var (
		it                           types.WorkItem
		payload                      sql.NullString
		state                        string
		notBefore, enqueued, updated int64
		leaseUntil, dirtySince       sql.NullInt64
	)
	err := r.Scan(&it.Kind, &it.Key, &payload, &state, &it.Attempts, &it.Generation, &notBefore,
		&it.LeasedBy, &leaseUntil, &dirtySince, &it.LastError, &enqueued, &updated)
	if payload.Valid {
		it.Payload = []byte(payload.String)
	}
	it.State = types.WorkState(state)
	it.NotBefore, it.Enqueued, it.Updated = fromMicros(
		sql.NullInt64{Int64: notBefore, Valid: true},
	),
		fromMicros(
			sql.NullInt64{Int64: enqueued, Valid: true},
		),
		fromMicros(
			sql.NullInt64{Int64: updated, Valid: true},
		)
	it.LeaseUntil, it.DirtySince = fromMicros(leaseUntil), fromMicros(dirtySince)
	return it, err //nolint:wrapcheck // wrapped by the caller
}

// nullPayload binds an empty payload as NULL.
func nullPayload(p []byte) any {
	if len(p) == 0 {
		return nil
	}
	return string(p)
}

func (w workQueue) Enqueue(
	ctx context.Context,
	kind, key string,
	payload []byte,
	now time.Time,
) error {
	if err := store.ValidateWork(kind, key, payload); err != nil {
		return err //nolint:wrapcheck // the store's error names the item
	}
	n := now.UnixMicro()
	// One statement, so two enqueues of a key racing on PostgreSQL meet at
	// the conflict and the second applies the rules of the first's row.
	_, err := w.s.exec(
		ctx,
		"enqueue work "+kind,
		"INSERT INTO work_items ("+workCols+") VALUES (?, ?, ?, 'pending', 0, 0, ?, '', NULL, NULL, '', ?, ?) "+
			"ON CONFLICT (kind, key) DO UPDATE SET "+
			"payload = excluded.payload, "+
			"state = CASE WHEN work_items.state = 'parked' THEN 'pending' ELSE work_items.state END, "+
			"attempts = CASE WHEN work_items.state = 'parked' THEN 0 ELSE work_items.attempts END, "+
			"not_before = CASE WHEN work_items.state = 'parked' THEN excluded.not_before "+
			"ELSE work_items.not_before END, "+
			"enqueued_at = CASE WHEN work_items.state = 'parked' THEN excluded.enqueued_at "+
			"ELSE work_items.enqueued_at END, "+
			"dirty_since = CASE WHEN work_items.state = 'leased' "+
			"THEN COALESCE(work_items.dirty_since, excluded.enqueued_at) ELSE work_items.dirty_since END, "+
			"updated_at = excluded.updated_at",
		kind,
		key,
		nullPayload(payload),
		n,
		n,
		n,
	)
	return err
}

// dueClause selects the items Lease may take at a time bound twice.
const dueClause = "((state = 'pending' AND not_before <= ?) OR (state = 'leased' AND lease_until <= ?))"

// take leases one selected item and returns it as taken.
func (w workQueue) take(
	ctx context.Context,
	kind, key string,
	req store.WorkLease,
) (types.WorkItem, error) {
	row := w.s.q(ctx).QueryRowContext(ctx, w.s.d.Rebind(
		"UPDATE work_items SET state = 'leased', leased_by = ?, lease_until = ?, "+
			"generation = generation + 1, attempts = attempts + 1, dirty_since = NULL, updated_at = ? "+
			"WHERE kind = ? AND key = ? RETURNING "+workCols),
		req.Holder, req.Until.UnixMicro(), req.Now.UnixMicro(), kind, key)
	it, err := scanWork(row)
	if err != nil {
		return it, w.s.wrap("lease work "+kind+"/"+key, err)
	}
	return it, nil
}

func (w workQueue) Lease(ctx context.Context, req store.WorkLease) ([]types.WorkItem, error) {
	if err := store.ValidateLease(req); err != nil {
		return nil, err //nolint:wrapcheck // the store's error names the request
	}
	if req.Limit <= 0 {
		return nil, nil
	}
	var out []types.WorkItem
	err := w.s.Tx(ctx, func(ctx context.Context) error {
		out = nil
		now := req.Now.UnixMicro()
		q := "SELECT kind, key FROM work_items WHERE " + dueClause
		args := []any{now, now}
		if len(req.Kinds) > 0 {
			q += " AND kind IN (" + Placeholders(len(req.Kinds)) + ")"
			for _, k := range req.Kinds {
				args = append(args, k)
			}
		}
		q += " ORDER BY CASE WHEN state = 'pending' THEN not_before ELSE lease_until END, " +
			"enqueued_at, kind, key LIMIT ?"
		args = append(args, req.Limit)
		// SKIP LOCKED lets several nodes lease at once on PostgreSQL, each
		// passing over the rows another is taking; SQLite's one writer
		// already keeps them apart.
		if lock := w.s.lockClause(ctx); lock != "" {
			q += lock + " SKIP LOCKED"
		}
		type ref struct{ kind, key string }
		var refs []ref
		if err := w.s.rows(ctx, "lease work", q, args, func(r *sql.Rows) error {
			var x ref
			err := r.Scan(&x.kind, &x.key)
			refs = append(refs, x)
			return err //nolint:wrapcheck // wrapped by rows
		}); err != nil {
			return err
		}
		for _, x := range refs {
			it, err := w.take(ctx, x.kind, x.key, req)
			if err != nil {
				return err
			}
			out = append(out, it)
		}
		return nil
	})
	return out, err
}

func (w workQueue) LeaseKey(
	ctx context.Context,
	kind, key string,
	req store.WorkLease,
) (types.WorkItem, bool, error) {
	if err := store.ValidateLease(req); err != nil {
		return types.WorkItem{}, false, err //nolint:wrapcheck // the store's error names the request
	}
	var (
		out  types.WorkItem
		took bool
	)
	err := w.s.Tx(ctx, func(ctx context.Context) error {
		took = false
		row := w.s.q(ctx).QueryRowContext(ctx, w.s.d.Rebind(
			"SELECT "+workCols+" FROM work_items WHERE kind = ? AND key = ?"+w.s.lockClause(ctx)),
			kind, key)
		it, err := scanWork(row)
		if err != nil {
			return w.s.wrap("get work "+kind+"/"+key, err)
		}
		out = it
		expired := it.State == types.WorkLeased && it.LeaseUntil.UnixMicro() <= req.Now.UnixMicro()
		if it.State != types.WorkPending && !expired {
			return nil
		}
		if out, err = w.take(ctx, kind, key, req); err != nil {
			return err
		}
		took = true
		return nil
	})
	return out, took, err
}

// held runs one UPDATE or DELETE on the item the run with generation gen
// holds, when cond (if any) also holds; ErrConflict when it matches nothing.
func (w workQueue) held(
	ctx context.Context,
	op, kind, key string,
	gen int64,
	cond, q string,
	args ...any,
) error {
	where := " WHERE kind = ? AND key = ? AND state = 'leased' AND generation = ?"
	if cond != "" {
		where += " AND " + cond
	}
	n, err := w.s.exec(ctx, op, q+where, append(args, kind, key, gen)...)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("work item %s/%s run %d: %w", kind, key, gen, store.ErrConflict)
	}
	return nil
}

func (w workQueue) Extend(
	ctx context.Context,
	kind, key string,
	gen int64,
	until, now time.Time,
) error {
	return w.held(ctx, "extend work "+kind, kind, key, gen, "",
		"UPDATE work_items SET lease_until = ?, updated_at = ?", until.UnixMicro(), now.UnixMicro())
}

// unlease sets a run's item pending again; any work asked for during the
// run folds into the pending item.
const unlease = "state = 'pending', not_before = ?, leased_by = '', lease_until = NULL, " +
	"dirty_since = NULL, updated_at = ?"

func (w workQueue) Complete(
	ctx context.Context,
	kind, key string,
	gen int64,
	now time.Time,
) error {
	return w.s.Tx(ctx, func(ctx context.Context) error {
		// The item stays only when work was asked for during the run; that
		// work is then owed from when it was asked for.
		err := w.held(
			ctx,
			"complete work "+kind,
			kind,
			key,
			gen,
			"dirty_since IS NOT NULL",
			"UPDATE work_items SET enqueued_at = dirty_since, attempts = 0, last_error = '', "+unlease,
			now.UnixMicro(),
			now.UnixMicro(),
		)
		if !errors.Is(err, store.ErrConflict) {
			return err
		}
		return w.held(ctx, "complete work "+kind, kind, key, gen, "", "DELETE FROM work_items")
	})
}

func (w workQueue) Fail(
	ctx context.Context,
	kind, key string,
	gen int64,
	f store.WorkFailure,
) error {
	return w.s.Tx(ctx, func(ctx context.Context) error {
		if f.Park {
			// Work asked for during the run is new: it is retried rather
			// than parked with the run that failed.
			err := w.held(ctx, "park work "+kind, kind, key, gen, "dirty_since IS NULL",
				"UPDATE work_items SET state = 'parked', not_before = ?, leased_by = '', "+
					"lease_until = NULL, last_error = ?, updated_at = ?",
				f.Now.UnixMicro(), f.Error, f.Now.UnixMicro())
			if !errors.Is(err, store.ErrConflict) {
				return err
			}
		}
		return w.held(ctx, "fail work "+kind, kind, key, gen, "",
			"UPDATE work_items SET last_error = ?, "+unlease,
			f.Error, f.RetryAt.UnixMicro(), f.Now.UnixMicro())
	})
}

func (w workQueue) Release(
	ctx context.Context,
	kind, key string,
	gen int64,
	now time.Time,
) error {
	return w.held(
		ctx,
		"release work "+kind,
		kind,
		key,
		gen,
		"",
		"UPDATE work_items SET attempts = CASE WHEN attempts > 0 THEN attempts - 1 ELSE 0 END, "+unlease,
		now.UnixMicro(),
		now.UnixMicro(),
	)
}

func (w workQueue) Get(ctx context.Context, kind, key string) (types.WorkItem, error) {
	row := w.s.q(ctx).QueryRowContext(ctx,
		w.s.d.Rebind("SELECT "+workCols+" FROM work_items WHERE kind = ? AND key = ?"), kind, key)
	it, err := scanWork(row)
	if err != nil {
		return it, w.s.wrap("get work "+kind+"/"+key, err)
	}
	return it, nil
}

func (w workQueue) Stats(ctx context.Context) ([]types.WorkStats, error) {
	var out []types.WorkStats
	err := w.s.rows(ctx, "work stats",
		"SELECT kind, "+
			"SUM(CASE WHEN state = 'pending' THEN 1 ELSE 0 END), "+
			"SUM(CASE WHEN state = 'leased' THEN 1 ELSE 0 END), "+
			"SUM(CASE WHEN state = 'parked' THEN 1 ELSE 0 END), "+
			"MIN(CASE WHEN state = 'pending' THEN enqueued_at END) "+
			"FROM work_items GROUP BY kind ORDER BY kind",
		nil,
		func(r *sql.Rows) error {
			var (
				st     types.WorkStats
				oldest sql.NullInt64
			)
			err := r.Scan(&st.Kind, &st.Pending, &st.Leased, &st.Parked, &oldest)
			st.OldestPending = fromMicros(oldest)
			out = append(out, st)
			return err //nolint:wrapcheck // wrapped by rows
		})
	return out, err
}
