package inmem

import (
	"cmp"
	"context"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// workerEntry identifies one state entry or secret of a worker.
type workerEntry struct{ Worker, Name string }

// Workers implements store.Store.
func (s *Store) Workers() store.WorkerStore { return workerStore{s} }

// --- Workers ---------------------------------------------------------------

type workerStore struct{ s *Store }

func (w workerStore) workers() *table[string, types.Worker] {
	return tableOf[string, types.Worker](w.s, "workers")
}

func (w workerStore) state() *table[workerEntry, string] {
	return tableOf[workerEntry, string](w.s, "worker_state")
}

func (w workerStore) secrets() *table[workerEntry, string] {
	return tableOf[workerEntry, string](w.s, "worker_secrets")
}

func (w workerStore) executions() *table[string, types.WorkerExecution] {
	return tableOf[string, types.WorkerExecution](w.s, "worker_executions")
}

// exists fails with ErrNotFound when the worker does not exist.
func (w workerStore) exists(key string) error {
	if !w.workers().has(key) {
		return notFound("worker", key)
	}
	return nil
}

func (w workerStore) Create(ctx context.Context, v types.Worker) error {
	return w.s.do(ctx, func() error {
		if w.workers().has(v.Key) {
			return conflict("worker", v.Key)
		}
		w.workers().put(v.Key, v)
		return nil
	})
}

func (w workerStore) Get(ctx context.Context, key string) (types.Worker, error) {
	var out types.Worker
	err := w.s.do(ctx, func() error {
		var ok bool
		if out, ok = w.workers().get(key); !ok {
			return notFound("worker", key)
		}
		return nil
	})
	return out, err
}

func (w workerStore) Update(ctx context.Context, v types.Worker) error {
	return w.s.do(ctx, func() error {
		if err := w.exists(v.Key); err != nil {
			return err
		}
		w.workers().put(v.Key, v)
		return nil
	})
}

func (w workerStore) Delete(ctx context.Context, key string) error {
	return w.s.do(ctx, func() error {
		if !w.workers().del(key) {
			return notFound("worker", key)
		}
		owned := func(k workerEntry, _ string) bool { return k.Worker == key }
		w.state().deleteWhere(owned)
		w.secrets().deleteWhere(owned)
		w.executions().
			deleteWhere(func(_ string, v types.WorkerExecution) bool { return v.WorkerKey == key })
		return nil
	})
}

func (w workerStore) List(ctx context.Context, f store.WorkerFilter) ([]types.Worker, error) {
	var out []types.Worker
	err := w.s.do(ctx, func() error {
		out = w.workers().all(func(_ string, v types.Worker) bool {
			return (f.Action == "" || v.Action == f.Action) &&
				(f.ProjectKey == "" || v.ProjectKey == f.ProjectKey) &&
				(!f.EnabledOnly || v.Enabled)
		}, func(x, y types.Worker) int { return strings.Compare(x.Key, y.Key) })
		return nil
	})
	return out, err
}

// entries collects a worker's rows of an entry table into a map.
func (w workerStore) entries(t *table[workerEntry, string], worker string) map[string]string {
	out := map[string]string{}
	for k, v := range t.rows {
		if k.Worker == worker {
			out[k.Name] = v
		}
	}
	return out
}

func (w workerStore) State(ctx context.Context, worker string) (map[string]string, error) {
	var out map[string]string
	err := w.s.do(ctx, func() error {
		if err := w.exists(worker); err != nil {
			return err
		}
		out = w.entries(w.state(), worker)
		return nil
	})
	return out, err
}

func (w workerStore) SetState(ctx context.Context, worker, key, value string) error {
	return w.s.do(ctx, func() error {
		if err := w.exists(worker); err != nil {
			return err
		}
		w.state().put(workerEntry{worker, key}, value)
		return nil
	})
}

func (w workerStore) DeleteState(ctx context.Context, worker, key string) error {
	return w.s.do(ctx, func() error {
		w.state().del(workerEntry{worker, key})
		return nil
	})
}

func (w workerStore) PutSecret(ctx context.Context, worker, name, value string) error {
	return w.s.do(ctx, func() error {
		if err := w.exists(worker); err != nil {
			return err
		}
		w.secrets().put(workerEntry{worker, name}, value)
		return nil
	})
}

func (w workerStore) Secrets(ctx context.Context, worker string) (map[string]string, error) {
	var out map[string]string
	err := w.s.do(ctx, func() error {
		if err := w.exists(worker); err != nil {
			return err
		}
		out = w.entries(w.secrets(), worker)
		return nil
	})
	return out, err
}

func (w workerStore) DeleteSecret(ctx context.Context, worker, name string) error {
	return w.s.do(ctx, func() error {
		if !w.secrets().del(workerEntry{worker, name}) {
			return notFound("worker secret", workerEntry{worker, name})
		}
		return nil
	})
}

func (w workerStore) AddExecution(ctx context.Context, e types.WorkerExecution) error {
	return w.s.do(ctx, func() error {
		if w.executions().has(e.ID) {
			return conflict("worker execution", e.ID)
		}
		if err := w.exists(e.WorkerKey); err != nil {
			return err
		}
		w.executions().put(e.ID, e)
		return nil
	})
}

func (w workerStore) Execution(ctx context.Context, id string) (types.WorkerExecution, error) {
	var out types.WorkerExecution
	err := w.s.do(ctx, func() error {
		var ok bool
		if out, ok = w.executions().get(id); !ok {
			return notFound("worker execution", id)
		}
		return nil
	})
	return out, err
}

func (w workerStore) Executions(
	ctx context.Context, worker string, p store.Page,
) ([]types.WorkerExecution, string, error) {
	var (
		out  []types.WorkerExecution
		next string
	)
	err := w.s.do(ctx, func() error {
		rows := w.executions().all(func(_ string, v types.WorkerExecution) bool {
			return worker == "" || v.WorkerKey == worker
		}, func(x, y types.WorkerExecution) int {
			return cmp.Or(y.Started.Compare(x.Started), strings.Compare(y.ID, x.ID))
		})
		var err error
		out, next, err = paginate(rows, p, true, func(v types.WorkerExecution) []any {
			return []any{v.Started.UnixMicro(), v.ID}
		})
		return err
	})
	return out, next, err
}

func (w workerStore) PruneExecutions(ctx context.Context, before time.Time) (int64, error) {
	before = before.Truncate(time.Microsecond)
	var n int64
	err := w.s.do(ctx, func() error {
		n = w.executions().
			deleteWhere(func(_ string, v types.WorkerExecution) bool { return v.Started.Before(before) })
		return nil
	})
	return n, err
}
