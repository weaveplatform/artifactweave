package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// runWorkers covers Workers (decision 0026).
func runWorkers(t *testing.T, newStore Factory) {
	t.Run("Workers", func(t *testing.T) { testWorkers(t, newStore(t)) })
	t.Run("WorkerState", func(t *testing.T) { testWorkerState(t, newStore(t)) })
	t.Run("WorkerSecrets", func(t *testing.T) { testWorkerSecrets(t, newStore(t)) })
	t.Run("WorkerExecutions", func(t *testing.T) { testWorkerExecutions(t, newStore(t)) })
}

// fxWorker builds a worker.
func fxWorker(key string, action types.WorkerAction) types.Worker {
	return types.Worker{
		Key:         key,
		Action:      action,
		SourceCode:  "export default async (ctx, data) => ({ status: 'DOWNLOAD_PROCEED' });",
		Enabled:     true,
		Description: "worker " + key,
		Filter: types.WorkerFilter{
			RepoKeys: []string{"libs"}, IncludePatterns: []string{"**/*.jar"}, AnyLocal: true,
		},
		ProgramHash: "sha256:abc",
		Created:     at(1),
		Updated:     at(2),
	}
}

func testWorkers(t *testing.T, s store.Store) {
	ctx := context.Background()
	ws := s.Workers()
	w := fxWorker("block-snapshots", "BEFORE_DOWNLOAD")
	must(t, ws.Create(ctx, w))
	same(t, "Get", w, get[types.Worker](t)(ws.Get(ctx, w.Key)))
	wantErr(t, ws.Create(ctx, w), store.ErrConflict)
	_, err := ws.Get(ctx, "nope")
	wantErr(t, err, store.ErrNotFound)

	w.Enabled, w.Failures, w.BlockedUntil, w.Debug, w.Shared = false, 3, at(10), true, true
	w.Filter = types.WorkerFilter{}
	must(t, ws.Update(ctx, w))
	same(t, "Get after update", w, get[types.Worker](t)(ws.Get(ctx, w.Key)))
	wantErr(t, ws.Update(ctx, fxWorker("nope", "AFTER_CREATE")), store.ErrNotFound)

	tag := fxWorker("Tagger", "AFTER_CREATE")
	tag.ProjectKey = "p1"
	must(t, ws.Create(ctx, tag))
	must(t, ws.Create(ctx, fxWorker("audit", "AFTER_CREATE")))
	keys := func(f store.WorkerFilter) []string {
		var out []string
		for _, v := range get[[]types.Worker](t)(ws.List(ctx, f)) {
			out = append(out, v.Key)
		}
		return out
	}
	same(t, "List", []string{"Tagger", "audit", "block-snapshots"}, keys(store.WorkerFilter{}))
	same(
		t,
		"List(action)",
		[]string{"Tagger", "audit"},
		keys(store.WorkerFilter{Action: "AFTER_CREATE"}),
	)
	same(t, "List(project)", []string{"Tagger"}, keys(store.WorkerFilter{ProjectKey: "p1"}))
	same(
		t,
		"List(enabled)",
		[]string{"Tagger", "audit"},
		keys(store.WorkerFilter{EnabledOnly: true}),
	)

	must(t, ws.Delete(ctx, "audit"))
	_, err = ws.Get(ctx, "audit")
	wantErr(t, err, store.ErrNotFound)
	wantErr(t, ws.Delete(ctx, "audit"), store.ErrNotFound)
}

func testWorkerState(t *testing.T, s store.Store) {
	ctx := context.Background()
	ws := s.Workers()
	must(t, ws.Create(ctx, fxWorker("w", "AFTER_CREATE")))
	same(
		t,
		"State of a new worker",
		map[string]string{},
		get[map[string]string](t)(ws.State(ctx, "w")),
	)
	must(t, ws.SetState(ctx, "w", "count", "1"))
	must(t, ws.SetState(ctx, "w", "count", "2"))
	must(t, ws.SetState(ctx, "w", "last", "lib.jar"))
	same(
		t,
		"State",
		map[string]string{"count": "2", "last": "lib.jar"},
		get[map[string]string](t)(ws.State(ctx, "w")),
	)
	must(t, ws.DeleteState(ctx, "w", "last"))
	must(t, ws.DeleteState(ctx, "w", "last"))
	same(
		t,
		"State after delete",
		map[string]string{"count": "2"},
		get[map[string]string](t)(ws.State(ctx, "w")),
	)
	wantErr(t, ws.SetState(ctx, "nope", "k", "v"), store.ErrNotFound)
	_, err := ws.State(ctx, "nope")
	wantErr(t, err, store.ErrNotFound)

	// State goes with the worker.
	must(t, ws.Delete(ctx, "w"))
	must(t, ws.Create(ctx, fxWorker("w", "AFTER_CREATE")))
	same(
		t,
		"State of a recreated worker",
		map[string]string{},
		get[map[string]string](t)(ws.State(ctx, "w")),
	)
}

func testWorkerSecrets(t *testing.T, s store.Store) {
	ctx := context.Background()
	ws := s.Workers()
	must(t, ws.Create(ctx, fxWorker("w", "AFTER_CREATE")))
	must(t, ws.Create(ctx, fxWorker("x", "AFTER_CREATE")))
	same(
		t,
		"Secrets of a new worker",
		map[string]string{},
		get[map[string]string](t)(ws.Secrets(ctx, "w")),
	)
	must(t, ws.PutSecret(ctx, "w", "token", "s3cret"))
	must(t, ws.PutSecret(ctx, "w", "token", "rotated"))
	must(t, ws.PutSecret(ctx, "w", "slack", "xoxb-1"))
	must(t, ws.PutSecret(ctx, "x", "token", "other"))
	same(t, "Secrets", map[string]string{"token": "rotated", "slack": "xoxb-1"},
		get[map[string]string](t)(ws.Secrets(ctx, "w")))
	same(t, "Secrets of another worker", map[string]string{"token": "other"},
		get[map[string]string](t)(ws.Secrets(ctx, "x")))
	must(t, ws.DeleteSecret(ctx, "w", "slack"))
	wantErr(t, ws.DeleteSecret(ctx, "w", "slack"), store.ErrNotFound)
	same(t, "Secrets after delete", map[string]string{"token": "rotated"},
		get[map[string]string](t)(ws.Secrets(ctx, "w")))
	wantErr(t, ws.PutSecret(ctx, "nope", "k", "v"), store.ErrNotFound)
	_, err := ws.Secrets(ctx, "nope")
	wantErr(t, err, store.ErrNotFound)

	// Secrets go with the worker.
	must(t, ws.Delete(ctx, "w"))
	must(t, ws.Create(ctx, fxWorker("w", "AFTER_CREATE")))
	same(
		t,
		"Secrets of a recreated worker",
		map[string]string{},
		get[map[string]string](t)(ws.Secrets(ctx, "w")),
	)
}

func testWorkerExecutions(t *testing.T, s store.Store) {
	ctx := context.Background()
	ws := s.Workers()
	must(t, ws.Create(ctx, fxWorker("a", "BEFORE_DOWNLOAD")))
	must(t, ws.Create(ctx, fxWorker("b", "AFTER_CREATE")))
	exec := func(id, worker string, started time.Time) types.WorkerExecution {
		return types.WorkerExecution{
			ID: id, WorkerKey: worker, Action: "BEFORE_DOWNLOAD", Trigger: types.TriggerEvent,
			Status: "STATUS_SUCCESS", Started: started,
			Duration: 1234567890 * time.Nanosecond, // 1234.56789 ms: stored as 1234 ms
			Payload:  []byte(`{"repoKey":"libs"}`), Result: []byte(`{"status":"DOWNLOAD_PROCEED"}`),
			Logs: []byte("line 1\nline 2\n"),
		}
	}
	e1, e2, e3 := exec("e1", "a", at(1)), exec("e2", "a", at(3)), exec("e3", "b", at(2))
	// e2 and e4 start in the same microsecond; the higher ID lists first.
	e4, e5 := exec("e4", "a", at(3)), exec("e5", "b", at(4))
	e5.Status, e5.Error, e5.Payload, e5.Result, e5.Logs = "STATUS_FAIL", "WORKER_TIMEOUT", nil, nil, nil
	for _, e := range []types.WorkerExecution{e3, e1, e4, e2, e5} {
		must(t, ws.AddExecution(ctx, e))
	}
	same(t, "Execution", e1, get[types.WorkerExecution](t)(ws.Execution(ctx, "e1")))
	if got := get[types.WorkerExecution](
		t,
	)(
		ws.Execution(ctx, "e1"),
	); got.Duration != 1234*time.Millisecond {
		t.Fatalf("Duration = %v, want 1.234s", got.Duration)
	}
	wantErr(t, ws.AddExecution(ctx, e1), store.ErrConflict)
	wantErr(t, ws.AddExecution(ctx, exec("e9", "nope", at(1))), store.ErrNotFound)
	_, err := ws.Execution(ctx, "nope")
	wantErr(t, err, store.ErrNotFound)

	ids := func(worker string) []string {
		var out []string
		for _, v := range pages(t, 2, func(p store.Page) ([]types.WorkerExecution, string, error) {
			return ws.Executions(ctx, worker, p)
		}) {
			out = append(out, v.ID)
		}
		return out
	}
	same(t, "Executions", []string{"e5", "e4", "e2", "e3", "e1"}, ids(""))
	same(t, "Executions(a)", []string{"e4", "e2", "e1"}, ids("a"))
	same(
		t,
		"Executions(b) full records",
		[]types.WorkerExecution{e5, e3},
		pages(
			t,
			1,
			func(p store.Page) ([]types.WorkerExecution, string, error) { return ws.Executions(ctx, "b", p) },
		),
	)
	_, _, err = ws.Executions(ctx, "", store.Page{After: store.Cursor("x", "e1")})
	wantErr(t, err, store.ErrInvalidCursor)
	_, _, err = ws.Executions(ctx, "", store.Page{After: "%%%"})
	wantErr(t, err, store.ErrInvalidCursor)

	n, err := ws.PruneExecutions(ctx, at(3))
	must(t, err)
	if n != 2 {
		t.Fatalf("PruneExecutions removed %d, want 2", n)
	}
	same(t, "Executions after prune", []string{"e5", "e4", "e2"}, ids(""))

	// Executions go with the worker.
	must(t, ws.Delete(ctx, "a"))
	same(t, "Executions after deleting a worker", []string{"e5"}, ids(""))
	_, err = ws.Execution(ctx, "e4")
	wantErr(t, err, store.ErrNotFound)
}
