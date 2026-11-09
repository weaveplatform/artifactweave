package inmem

import (
	"cmp"
	"context"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/migration/workers/core/model"
	"github.com/weaveplatform/artifactweave/migration/workers/core/port"
)

// workerEntry identifies one state entry or secret of a worker.
type workerEntry struct{ Worker, Name string }

// Workers returns the worker store: definitions, state, secrets and
// executions (decision 0026). Nothing here outlives the process, so
// secrets are kept as given, not sealed as the SQL store seals them.
func (s *Store) Workers() port.WorkerStore { return workerStore{s} }

type workerStore struct{ s *Store }

func (w workerStore) workers() *memtx.Table[string, model.Worker] {
	return memtx.TableOf[string, model.Worker](w.s.Store, "workers")
}

func (w workerStore) state() *memtx.Table[workerEntry, string] {
	return memtx.TableOf[workerEntry, string](w.s.Store, "worker_state")
}

func (w workerStore) secrets() *memtx.Table[workerEntry, string] {
	return memtx.TableOf[workerEntry, string](w.s.Store, "worker_secrets")
}

func (w workerStore) executions() *memtx.Table[string, model.WorkerExecution] {
	return memtx.TableOf[string, model.WorkerExecution](w.s.Store, "worker_executions")
}

// exists fails with ErrNotFound when the worker does not exist.
func (w workerStore) exists(key string) error {
	if !w.workers().Has(key) {
		return memtx.NotFound("worker", key) //nolint:wrapcheck // wraps kernel.ErrNotFound
	}
	return nil
}

func (w workerStore) Create(ctx context.Context, v model.Worker) error {
	return w.s.Do(ctx, func() error {
		if w.workers().Has(v.Key) {
			return memtx.Conflict("worker", v.Key)
		}
		w.workers().Put(v.Key, v)
		return nil
	})
}

func (w workerStore) Get(ctx context.Context, key string) (model.Worker, error) {
	var out model.Worker
	err := w.s.Do(ctx, func() error {
		var ok bool
		if out, ok = w.workers().Get(key); !ok {
			return memtx.NotFound("worker", key)
		}
		return nil
	})
	return out, err
}

func (w workerStore) Update(ctx context.Context, v model.Worker) error {
	return w.s.Do(ctx, func() error {
		if err := w.exists(v.Key); err != nil {
			return err
		}
		w.workers().Put(v.Key, v)
		return nil
	})
}

func (w workerStore) Delete(ctx context.Context, key string) error {
	return w.s.Do(ctx, func() error {
		if !w.workers().Del(key) {
			return memtx.NotFound("worker", key)
		}
		owned := func(k workerEntry, _ string) bool { return k.Worker == key }
		w.state().DeleteWhere(owned)
		w.secrets().DeleteWhere(owned)
		w.executions().
			DeleteWhere(func(_ string, v model.WorkerExecution) bool { return v.WorkerKey == key })
		return nil
	})
}

func (w workerStore) List(ctx context.Context, f port.WorkerFilter) ([]model.Worker, error) {
	var out []model.Worker
	err := w.s.Do(ctx, func() error {
		out = w.workers().All(func(_ string, v model.Worker) bool {
			return (f.Action == "" || v.Action == f.Action) &&
				(f.ProjectKey == "" || v.ProjectKey == f.ProjectKey) &&
				(!f.EnabledOnly || v.Enabled)
		}, func(x, y model.Worker) int { return strings.Compare(x.Key, y.Key) })
		return nil
	})
	return out, err
}

// entries collects a worker's rows of an entry table into a map.
func (w workerStore) entries(t *memtx.Table[workerEntry, string], worker string) map[string]string {
	out := map[string]string{}
	for k, v := range t.Rows {
		if k.Worker == worker {
			out[k.Name] = v
		}
	}
	return out
}

func (w workerStore) State(ctx context.Context, worker string) (map[string]string, error) {
	var out map[string]string
	err := w.s.Do(ctx, func() error {
		if err := w.exists(worker); err != nil {
			return err
		}
		out = w.entries(w.state(), worker)
		return nil
	})
	return out, err
}

func (w workerStore) SetState(ctx context.Context, worker, key, value string) error {
	return w.s.Do(ctx, func() error {
		if err := w.exists(worker); err != nil {
			return err
		}
		w.state().Put(workerEntry{worker, key}, value)
		return nil
	})
}

func (w workerStore) DeleteState(ctx context.Context, worker, key string) error {
	return w.s.Do(ctx, func() error {
		w.state().Del(workerEntry{worker, key})
		return nil
	})
}

func (w workerStore) PutSecret(ctx context.Context, worker, name, value string) error {
	return w.s.Do(ctx, func() error {
		if err := w.exists(worker); err != nil {
			return err
		}
		w.secrets().Put(workerEntry{worker, name}, value)
		return nil
	})
}

func (w workerStore) Secrets(ctx context.Context, worker string) (map[string]string, error) {
	var out map[string]string
	err := w.s.Do(ctx, func() error {
		if err := w.exists(worker); err != nil {
			return err
		}
		out = w.entries(w.secrets(), worker)
		return nil
	})
	return out, err
}

func (w workerStore) DeleteSecret(ctx context.Context, worker, name string) error {
	return w.s.Do(ctx, func() error {
		if !w.secrets().Del(workerEntry{worker, name}) {
			return memtx.NotFound("worker secret", workerEntry{worker, name})
		}
		return nil
	})
}

func (w workerStore) AddExecution(ctx context.Context, e model.WorkerExecution) error {
	return w.s.Do(ctx, func() error {
		if w.executions().Has(e.ID) {
			return memtx.Conflict("worker execution", e.ID)
		}
		if err := w.exists(e.WorkerKey); err != nil {
			return err
		}
		w.executions().Put(e.ID, e)
		return nil
	})
}

func (w workerStore) Execution(ctx context.Context, id string) (model.WorkerExecution, error) {
	var out model.WorkerExecution
	err := w.s.Do(ctx, func() error {
		var ok bool
		if out, ok = w.executions().Get(id); !ok {
			return memtx.NotFound("worker execution", id)
		}
		return nil
	})
	return out, err
}

func (w workerStore) Executions(
	ctx context.Context, worker string, p kernel.Page,
) ([]model.WorkerExecution, string, error) {
	var (
		out  []model.WorkerExecution
		next string
	)
	err := w.s.Do(ctx, func() error {
		rows := w.executions().All(func(_ string, v model.WorkerExecution) bool {
			return worker == "" || v.WorkerKey == worker
		}, func(x, y model.WorkerExecution) int {
			return cmp.Or(y.Started.Compare(x.Started), strings.Compare(y.ID, x.ID))
		})
		var err error
		out, next, err = memtx.Paginate(rows, p, true, func(v model.WorkerExecution) []any {
			return []any{v.Started.UnixMicro(), v.ID}
		})
		return err //nolint:wrapcheck // Paginate's error wraps kernel.ErrInvalidCursor
	})
	return out, next, err
}

func (w workerStore) PruneExecutions(ctx context.Context, before time.Time) (int64, error) {
	before = before.Truncate(time.Microsecond)
	var n int64
	err := w.s.Do(ctx, func() error {
		n = w.executions().
			DeleteWhere(func(_ string, v model.WorkerExecution) bool { return v.Started.Before(before) })
		return nil
	})
	return n, err
}
