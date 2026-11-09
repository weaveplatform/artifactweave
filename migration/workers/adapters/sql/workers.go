package sql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Workers implements store.Store.
func (s *Store) Workers() store.WorkerStore { return workerStore{s} }

// --- Workers ---------------------------------------------------------------

type workerStore struct{ s *Store }

// purposeWorkerSecret is the sealing purpose of worker secret values; the
// row id is "<worker>/<name>".
const purposeWorkerSecret = "worker_secrets.value"

var workerCols = []string{
	"key", "action", "enabled", "debug", "shared", "project_key", "description", "source",
	"program_hash", "failures", "blocked_until", "created_at", "updated_at", "filter_doc",
}

func workerRow(w types.Worker) []any {
	return []any{
		w.Key,
		string(w.Action),
		w.Enabled,
		w.Debug,
		w.Shared,
		w.ProjectKey,
		w.Description,
		w.SourceCode,
		w.ProgramHash,
		w.Failures,
		dialect.Micros(w.BlockedUntil),
		dialect.Micros(w.Created),
		dialect.Micros(w.Updated),
		dialect.MustJSON(w.Filter),
	}
}

func scanWorker(r dialect.EntRow) (types.Worker, error) {
	var (
		w                         types.Worker
		action                    string
		blocked, created, updated sql.NullInt64
		filter                    sql.NullString
	)
	if err := r.Scan(
		&w.Key,
		&action,
		&w.Enabled,
		&w.Debug,
		&w.Shared,
		&w.ProjectKey,
		&w.Description,
		&w.SourceCode,
		&w.ProgramHash,
		&w.Failures,
		&blocked,
		&created,
		&updated,
		&filter,
	); err != nil {
		return w, err //nolint:wrapcheck // wrapped by the caller
	}
	w.Action = types.WorkerAction(action)
	w.BlockedUntil, w.Created, w.Updated = dialect.FromMicros(
		blocked,
	), dialect.FromMicros(
		created,
	), dialect.FromMicros(
		updated,
	)
	return w, dialect.FromJSON(filter, &w.Filter)
}

func (w workerStore) Create(ctx context.Context, v types.Worker) error {
	_, err := w.s.Exec(ctx, "create worker "+v.Key, Insert("workers", workerCols), workerRow(v)...)
	return err
}

func (w workerStore) Get(ctx context.Context, key string) (types.Worker, error) {
	out, err := scanWorker(w.s.Q(ctx).QueryRowContext(ctx, w.s.Dialect().Rebind(
		"SELECT "+strings.Join(workerCols, ", ")+" FROM workers WHERE key = ?"), key))
	return out, w.s.Wrap("get worker "+key, err)
}

func (w workerStore) Update(ctx context.Context, v types.Worker) error {
	row := workerRow(v)
	sets := make([]string, 0, len(workerCols)-1)
	for _, c := range workerCols[1:] {
		sets = append(sets, c+" = ?")
	}
	return w.s.ExecOne(ctx, "update worker "+v.Key,
		"UPDATE workers SET "+strings.Join(sets, ", ")+" WHERE key = ?", append(row[1:], v.Key)...)
}

func (w workerStore) Delete(ctx context.Context, key string) error {
	// State, secrets and executions go with the row (ON DELETE CASCADE).
	return w.s.ExecOne(ctx, "delete worker "+key, "DELETE FROM workers WHERE key = ?", key)
}

func (w workerStore) List(ctx context.Context, f store.WorkerFilter) ([]types.Worker, error) {
	q := "SELECT " + strings.Join(workerCols, ", ") + " FROM workers WHERE 1 = 1"
	var args []any
	if f.Action != "" {
		q += " AND action = ?"
		args = append(args, string(f.Action))
	}
	if f.ProjectKey != "" {
		q += " AND project_key = ?"
		args = append(args, f.ProjectKey)
	}
	if f.EnabledOnly {
		q += " AND enabled = ?"
		args = append(args, true)
	}
	var out []types.Worker
	err := w.s.Rows(ctx, "list workers", q+" ORDER BY key", args, func(r *sql.Rows) error {
		v, err := scanWorker(r)
		out = append(out, v)
		return err
	})
	return out, err
}

// requireWorker fails with ErrNotFound when the worker is absent.
func (w workerStore) requireWorker(ctx context.Context, key string) error {
	ok, err := w.s.Exists(ctx, "find worker", "SELECT 1 FROM workers WHERE key = ?", key)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("worker %s: %w", key, store.ErrNotFound)
	}
	return nil
}

func (w workerStore) State(ctx context.Context, worker string) (map[string]string, error) {
	if err := w.requireWorker(ctx, worker); err != nil {
		return nil, err
	}
	out := map[string]string{}
	err := w.s.Rows(
		ctx,
		"list worker state",
		"SELECT key, value FROM worker_state WHERE worker_key = ?",
		[]any{worker},
		func(r *sql.Rows) error {
			var k, v string
			if err := r.Scan(&k, &v); err != nil {
				return err //nolint:wrapcheck // wrapped by Rows
			}
			out[k] = v
			return nil
		},
	)
	return out, err
}

func (w workerStore) SetState(ctx context.Context, worker, key, value string) error {
	return w.s.Tx(ctx, func(ctx context.Context) error {
		if err := w.requireWorker(ctx, worker); err != nil {
			return err
		}
		_, err := w.s.Exec(
			ctx,
			"set worker state",
			Upsert(
				"worker_state",
				[]string{"worker_key", "key", "value"},
				[]string{"worker_key", "key"},
			),
			worker,
			key,
			value,
		)
		return err
	})
}

func (w workerStore) DeleteState(ctx context.Context, worker, key string) error {
	_, err := w.s.Exec(ctx, "delete worker state",
		"DELETE FROM worker_state WHERE worker_key = ? AND key = ?", worker, key)
	return err
}

func (w workerStore) PutSecret(ctx context.Context, worker, name, value string) error {
	sealed, err := w.s.Seal(purposeWorkerSecret, worker+"/"+name, value)
	if err != nil {
		return err
	}
	return w.s.Tx(ctx, func(ctx context.Context) error {
		if err := w.requireWorker(ctx, worker); err != nil {
			return err
		}
		_, err := w.s.Exec(
			ctx,
			"put worker secret",
			Upsert(
				"worker_secrets",
				[]string{"worker_key", "name", "value"},
				[]string{"worker_key", "name"},
			),
			worker,
			name,
			sealed,
		)
		return err
	})
}

func (w workerStore) Secrets(ctx context.Context, worker string) (map[string]string, error) {
	if err := w.requireWorker(ctx, worker); err != nil {
		return nil, err
	}
	out := map[string]string{}
	err := w.s.Rows(
		ctx,
		"list worker secrets",
		"SELECT name, value FROM worker_secrets WHERE worker_key = ?",
		[]any{worker},
		func(r *sql.Rows) error {
			var (
				name   string
				sealed []byte
			)
			if err := r.Scan(&name, &sealed); err != nil {
				return err //nolint:wrapcheck // wrapped by Rows
			}
			v, err := w.s.Unseal(purposeWorkerSecret, worker+"/"+name, sealed)
			out[name] = v
			return err
		},
	)
	return out, err
}

func (w workerStore) DeleteSecret(ctx context.Context, worker, name string) error {
	return w.s.ExecOne(ctx, "delete worker secret "+worker+"/"+name,
		"DELETE FROM worker_secrets WHERE worker_key = ? AND name = ?", worker, name)
}

var workerExecutionCols = []string{
	"id", "worker_key", "action", "trigger_kind", "status", "started_at", "duration_ms",
	"payload", "result", "logs", "error",
}

func scanWorkerExecution(r dialect.EntRow) (types.WorkerExecution, error) {
	var (
		e                types.WorkerExecution
		action, trigger  string
		started, elapsed int64
	)
	if err := r.Scan(&e.ID, &e.WorkerKey, &action, &trigger, &e.Status, &started, &elapsed,
		&e.Payload, &e.Result, &e.Logs, &e.Error); err != nil {
		return e, err //nolint:wrapcheck // wrapped by the caller
	}
	e.Action, e.Trigger = types.WorkerAction(action), types.WorkerTrigger(trigger)
	e.Started, e.Duration = time.UnixMicro(started).UTC(), dialect.FromMillis(elapsed)
	return e, nil
}

func (w workerStore) AddExecution(ctx context.Context, e types.WorkerExecution) error {
	return w.s.Tx(ctx, func(ctx context.Context) error {
		if err := w.requireWorker(ctx, e.WorkerKey); err != nil {
			return err
		}
		// Started is NOT NULL so the newest-first keyset compares integers
		// only; a zero time stores as its own (very negative) instant.
		_, err := w.s.Exec(
			ctx,
			"add worker execution "+e.ID,
			Insert("worker_executions", workerExecutionCols),
			e.ID,
			e.WorkerKey,
			string(e.Action),
			string(e.Trigger),
			e.Status,
			e.Started.UnixMicro(),
			dialect.Millis(e.Duration),
			dialect.EntBytes(e.Payload),
			dialect.EntBytes(e.Result),
			dialect.EntBytes(e.Logs),
			e.Error,
		)
		return err
	})
}

func (w workerStore) Execution(ctx context.Context, id string) (types.WorkerExecution, error) {
	out, err := scanWorkerExecution(w.s.Q(ctx).QueryRowContext(ctx, w.s.Dialect().Rebind(
		"SELECT "+strings.Join(
			workerExecutionCols,
			", ",
		)+" FROM worker_executions WHERE id = ?",
	), id))
	return out, w.s.Wrap("get worker execution "+id, err)
}

func (w workerStore) Executions(
	ctx context.Context, worker string, p store.Page,
) ([]types.WorkerExecution, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	order := []string{"started_at", "id"}
	cond, args, err := dialect.Keyset(p, order, map[string]bool{"started_at": true}, true)
	if err != nil {
		return nil, "", err
	}
	if worker != "" {
		cond += " AND worker_key = ?"
		args = append(args, worker)
	}
	var out []types.WorkerExecution
	err = w.s.Rows(ctx, "list worker executions",
		"SELECT "+strings.Join(workerExecutionCols, ", ")+" FROM worker_executions WHERE "+cond+
			dialect.OrderBy(order, true)+" LIMIT ?",
		append(args, p.Limit+1),
		func(r *sql.Rows) error {
			v, err := scanWorkerExecution(r)
			out = append(out, v)
			return err
		})
	if err != nil {
		return nil, "", err
	}
	page, next := store.Trim(out, p.Limit, func(v types.WorkerExecution) []string {
		return []string{dialect.Itoa(v.Started.UnixMicro()), v.ID}
	})
	return page, next, nil
}

func (w workerStore) PruneExecutions(ctx context.Context, before time.Time) (int64, error) {
	return w.s.Exec(ctx, "prune worker executions",
		"DELETE FROM worker_executions WHERE started_at < ?", before.UnixMicro())
}
