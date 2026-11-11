package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/workers/core/model"
	"github.com/weaveplatform/artifactweave/internal/workers/core/port"
)

// runWorkers covers Workers (decision 0026).
func runWorkers(t *testing.T, newStore Factory) {
	t.Run("Workers", func(t *testing.T) { testWorkers(t, newStore(t)) })
	t.Run("WorkerState", func(t *testing.T) { testWorkerState(t, newStore(t)) })
	t.Run("WorkerSecrets", func(t *testing.T) { testWorkerSecrets(t, newStore(t)) })
	t.Run("WorkerExecutions", func(t *testing.T) { testWorkerExecutions(t, newStore(t)) })
}

// fxWorker builds a worker.
func fxWorker(key string, action model.WorkerAction) model.Worker {
	return model.Worker{
		Key:         key,
		Action:      action,
		SourceCode:  "export default async (ctx, data) => ({ status: 'DOWNLOAD_PROCEED' });",
		Enabled:     true,
		Description: "worker " + key,
		Filter: model.WorkerFilter{
			RepoKeys: []string{"libs"}, IncludePatterns: []string{"**/*.jar"}, AnyLocal: true,
		},
		ProgramHash: "sha256:abc",
		Created:     storekit.At(1),
		Updated:     storekit.At(2),
	}
}

func testWorkers(t *testing.T, s port.Store) {
	ctx := context.Background()
	ws := s.Workers()
	w := fxWorker("block-snapshots", "BEFORE_DOWNLOAD")
	storekit.Must(t, ws.Create(ctx, w))
	storekit.Same(t, "Get", w, storekit.Get[model.Worker](t)(ws.Get(ctx, w.Key)))
	storekit.WantErr(t, ws.Create(ctx, w), kernel.ErrConflict)
	_, err := ws.Get(ctx, "nope")
	storekit.WantErr(t, err, kernel.ErrNotFound)

	w.Enabled, w.Failures, w.BlockedUntil, w.Debug, w.Shared = false, 3, storekit.At(10), true, true
	w.Filter = model.WorkerFilter{}
	storekit.Must(t, ws.Update(ctx, w))
	storekit.Same(t, "Get after update", w, storekit.Get[model.Worker](t)(ws.Get(ctx, w.Key)))
	storekit.WantErr(t, ws.Update(ctx, fxWorker("nope", "AFTER_CREATE")), kernel.ErrNotFound)

	tag := fxWorker("Tagger", "AFTER_CREATE")
	tag.ProjectKey = "p1"
	storekit.Must(t, ws.Create(ctx, tag))
	storekit.Must(t, ws.Create(ctx, fxWorker("audit", "AFTER_CREATE")))
	keys := func(f port.WorkerFilter) []string {
		workers := storekit.Get[[]model.Worker](t)(ws.List(ctx, f))
		out := make([]string, 0, len(workers))
		for _, v := range workers {
			out = append(out, v.Key)
		}
		return out
	}
	storekit.Same(
		t,
		"List",
		[]string{"Tagger", "audit", "block-snapshots"},
		keys(port.WorkerFilter{}),
	)
	storekit.Same(
		t,
		"List(action)",
		[]string{"Tagger", "audit"},
		keys(port.WorkerFilter{Action: "AFTER_CREATE"}),
	)
	storekit.Same(
		t,
		"List(project)",
		[]string{"Tagger"},
		keys(port.WorkerFilter{ProjectKey: "p1"}),
	)
	storekit.Same(
		t,
		"List(enabled)",
		[]string{"Tagger", "audit"},
		keys(port.WorkerFilter{EnabledOnly: true}),
	)

	storekit.Must(t, ws.Delete(ctx, "audit"))
	_, err = ws.Get(ctx, "audit")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.WantErr(t, ws.Delete(ctx, "audit"), kernel.ErrNotFound)
}

func testWorkerState(t *testing.T, s port.Store) {
	ctx := context.Background()
	ws := s.Workers()
	storekit.Must(t, ws.Create(ctx, fxWorker("w", "AFTER_CREATE")))
	storekit.Same(
		t,
		"State of a new worker",
		map[string]string{},
		storekit.Get[map[string]string](t)(ws.State(ctx, "w")),
	)
	storekit.Must(t, ws.SetState(ctx, "w", "count", "1"))
	storekit.Must(t, ws.SetState(ctx, "w", "count", "2"))
	storekit.Must(t, ws.SetState(ctx, "w", "last", "lib.jar"))
	storekit.Same(
		t,
		"State",
		map[string]string{"count": "2", "last": "lib.jar"},
		storekit.Get[map[string]string](t)(ws.State(ctx, "w")),
	)
	storekit.Must(t, ws.DeleteState(ctx, "w", "last"))
	storekit.Must(t, ws.DeleteState(ctx, "w", "last"))
	storekit.Same(
		t,
		"State after delete",
		map[string]string{"count": "2"},
		storekit.Get[map[string]string](t)(ws.State(ctx, "w")),
	)
	storekit.WantErr(t, ws.SetState(ctx, "nope", "k", "v"), kernel.ErrNotFound)
	_, err := ws.State(ctx, "nope")
	storekit.WantErr(t, err, kernel.ErrNotFound)

	// State goes with the worker.
	storekit.Must(t, ws.Delete(ctx, "w"))
	storekit.Must(t, ws.Create(ctx, fxWorker("w", "AFTER_CREATE")))
	storekit.Same(
		t,
		"State of a recreated worker",
		map[string]string{},
		storekit.Get[map[string]string](t)(ws.State(ctx, "w")),
	)
}

func testWorkerSecrets(t *testing.T, s port.Store) {
	ctx := context.Background()
	ws := s.Workers()
	storekit.Must(t, ws.Create(ctx, fxWorker("w", "AFTER_CREATE")))
	storekit.Must(t, ws.Create(ctx, fxWorker("x", "AFTER_CREATE")))
	storekit.Same(
		t,
		"Secrets of a new worker",
		map[string]string{},
		storekit.Get[map[string]string](t)(ws.Secrets(ctx, "w")),
	)
	storekit.Must(t, ws.PutSecret(ctx, "w", "token", "s3cret"))
	storekit.Must(t, ws.PutSecret(ctx, "w", "token", "rotated"))
	storekit.Must(t, ws.PutSecret(ctx, "w", "slack", "xoxb-1"))
	storekit.Must(t, ws.PutSecret(ctx, "x", "token", "other"))
	storekit.Same(t, "Secrets", map[string]string{"token": "rotated", "slack": "xoxb-1"},
		storekit.Get[map[string]string](t)(ws.Secrets(ctx, "w")))
	storekit.Same(t, "Secrets of another worker", map[string]string{"token": "other"},
		storekit.Get[map[string]string](t)(ws.Secrets(ctx, "x")))
	storekit.Must(t, ws.DeleteSecret(ctx, "w", "slack"))
	storekit.WantErr(t, ws.DeleteSecret(ctx, "w", "slack"), kernel.ErrNotFound)
	storekit.Same(t, "Secrets after delete", map[string]string{"token": "rotated"},
		storekit.Get[map[string]string](t)(ws.Secrets(ctx, "w")))
	storekit.WantErr(t, ws.PutSecret(ctx, "nope", "k", "v"), kernel.ErrNotFound)
	_, err := ws.Secrets(ctx, "nope")
	storekit.WantErr(t, err, kernel.ErrNotFound)

	// Secrets go with the worker.
	storekit.Must(t, ws.Delete(ctx, "w"))
	storekit.Must(t, ws.Create(ctx, fxWorker("w", "AFTER_CREATE")))
	storekit.Same(
		t,
		"Secrets of a recreated worker",
		map[string]string{},
		storekit.Get[map[string]string](t)(ws.Secrets(ctx, "w")),
	)
}

func testWorkerExecutions(t *testing.T, s port.Store) {
	ctx := context.Background()
	ws := s.Workers()
	storekit.Must(t, ws.Create(ctx, fxWorker("a", "BEFORE_DOWNLOAD")))
	storekit.Must(t, ws.Create(ctx, fxWorker("b", "AFTER_CREATE")))
	exec := func(id, worker string, started time.Time) model.WorkerExecution {
		return model.WorkerExecution{
			ID: id, WorkerKey: worker, Action: "BEFORE_DOWNLOAD", Trigger: model.TriggerEvent,
			Status: "STATUS_SUCCESS", Started: started,
			Duration: 1234567890 * time.Nanosecond, // 1234.56789 ms: stored as 1234 ms
			Payload:  []byte(`{"repoKey":"libs"}`), Result: []byte(`{"status":"DOWNLOAD_PROCEED"}`),
			Logs: []byte("line 1\nline 2\n"),
		}
	}
	e1, e2, e3 := exec(
		"e1",
		"a",
		storekit.At(1),
	), exec(
		"e2",
		"a",
		storekit.At(3),
	), exec(
		"e3",
		"b",
		storekit.At(2),
	)
	// e2 and e4 start in the same microsecond; the higher ID lists first.
	e4, e5 := exec("e4", "a", storekit.At(3)), exec("e5", "b", storekit.At(4))
	e5.Status, e5.Error, e5.Payload, e5.Result, e5.Logs = "STATUS_FAIL", "WORKER_TIMEOUT", nil, nil, nil
	for _, e := range []model.WorkerExecution{e3, e1, e4, e2, e5} {
		storekit.Must(t, ws.AddExecution(ctx, e))
	}
	storekit.Same(
		t,
		"Execution",
		e1,
		storekit.Get[model.WorkerExecution](t)(ws.Execution(ctx, "e1")),
	)
	if got := storekit.Get[model.WorkerExecution](
		t,
	)(
		ws.Execution(ctx, "e1"),
	); got.Duration != 1234*time.Millisecond {
		t.Fatalf("Duration = %v, want 1.234s", got.Duration)
	}
	storekit.WantErr(t, ws.AddExecution(ctx, e1), kernel.ErrConflict)
	storekit.WantErr(
		t,
		ws.AddExecution(ctx, exec("e9", "nope", storekit.At(1))),
		kernel.ErrNotFound,
	)
	_, err := ws.Execution(ctx, "nope")
	storekit.WantErr(t, err, kernel.ErrNotFound)

	ids := func(worker string) []string {
		executions := storekit.Pages(
			t,
			2,
			func(p kernel.Page) ([]model.WorkerExecution, string, error) {
				return ws.Executions(ctx, worker, p)
			},
		)
		out := make([]string, 0, len(executions))
		for _, v := range executions {
			out = append(out, v.ID)
		}
		return out
	}
	storekit.Same(t, "Executions", []string{"e5", "e4", "e2", "e3", "e1"}, ids(""))
	storekit.Same(t, "Executions(a)", []string{"e4", "e2", "e1"}, ids("a"))
	storekit.Same(
		t,
		"Executions(b) full records",
		[]model.WorkerExecution{e5, e3},
		storekit.Pages(
			t,
			1,
			func(p kernel.Page) ([]model.WorkerExecution, string, error) { return ws.Executions(ctx, "b", p) },
		),
	)
	_, _, err = ws.Executions(ctx, "", kernel.Page{After: kernel.Cursor("x", "e1")})
	storekit.WantErr(t, err, kernel.ErrInvalidCursor)
	_, _, err = ws.Executions(ctx, "", kernel.Page{After: "%%%"})
	storekit.WantErr(t, err, kernel.ErrInvalidCursor)

	n, err := ws.PruneExecutions(ctx, storekit.At(3))
	storekit.Must(t, err)
	if n != 2 {
		t.Fatalf("PruneExecutions removed %d, want 2", n)
	}
	storekit.Same(t, "Executions after prune", []string{"e5", "e4", "e2"}, ids(""))

	// Executions go with the worker.
	storekit.Must(t, ws.Delete(ctx, "a"))
	storekit.Same(t, "Executions after deleting a worker", []string{"e5"}, ids(""))
	_, err = ws.Execution(ctx, "e4")
	storekit.WantErr(t, err, kernel.ErrNotFound)
}
