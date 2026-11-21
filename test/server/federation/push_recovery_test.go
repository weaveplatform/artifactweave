package federation_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbound"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func newPushBackendWorld(t *testing.T, backend string) *pushWorld {
	t.Helper()
	if backend == "postgres" {
		dsn := sqltest.PostgresDSN(t)
		return newPushWorld(t, backend, sqltest.ScratchDSN(t, dsn), sqltest.ScratchDSN(t, dsn), "generic")
	}
	return newPushWorld(t, "sqlite", "", "", "generic")
}

func (w *pushWorld) pending(run model.PushRun) model.PushItem {
	w.t.Helper()
	items, next, err := w.runtime.Store.PushProgress().Items(w.t.Context(), run.Reference, true, kernel.Page{Limit: 2})
	if err != nil || len(items) != 1 || next != "" {
		w.t.Fatal("expected one durable pending intent", len(items), err)
	}
	return items[0]
}

func (w *pushWorld) untilPhase(push replication.Push, run model.PushRun, action model.PushAction, phase model.PushItemPhase) model.PushRun {
	w.t.Helper()
	for attempt := 0; attempt < 80; attempt++ {
		items, _, err := w.runtime.Store.PushProgress().Items(w.t.Context(), run.Reference, true, kernel.Page{Limit: 2})
		if err != nil {
			w.t.Fatal(err)
		}
		if len(items) == 1 && items[0].Action == action && items[0].Phase == phase {
			return run
		}
		run, err = push.Step(w.t.Context(), run.Reference, w.fence)
		if err != nil {
			w.t.Fatal("prepare actual pending effect", err)
		}
		if run.Phase.Terminal() {
			w.t.Fatal("run ended before requested effect", action, phase)
		}
	}
	w.t.Fatal("pending effect never became ready")
	return run
}

func (w *pushWorld) restartSource() replication.Push {
	w.t.Helper()
	w.server.Close()
	if err := w.runtime.Close(context.Background()); err != nil {
		w.t.Fatal("close source before restart", err)
	}
	runtime, err := app.OpenRuntime(w.t.Context(), w.config, slog.New(slog.DiscardHandler), harness.AppOptions())
	if err != nil {
		w.t.Fatal("reopen same source database and blobs", err)
	}
	w.runtime = runtime
	w.server = httptest.NewServer(pushRuntimeRouter(runtime))
	w.source = w.server.URL
	push, err := runtime.Federation.Pusher()
	if err != nil {
		w.t.Fatal(err)
	}
	return push
}

func TestAccSinglePushRecoverySQLite(t *testing.T)   { qualifyPushRecovery(t, "sqlite") }
func TestAccSinglePushRecoveryPostgres(t *testing.T) { qualifyPushRecovery(t, "postgres") }

func qualifyPushRecovery(t *testing.T, backend string) {
	t.Helper()
	for _, effect := range []string{"upload", "properties", "delete"} {
		t.Run(effect, func(t *testing.T) {
			w := newPushBackendWorld(t, backend)
			const path = "selected/file.bin"
			const content = "durable content survives acknowledgement loss"
			if effect != "delete" {
				w.request(w.source, http.MethodPut, itemRoute("source", path), content, http.StatusCreated)
				w.request(w.source, http.MethodPatch, "/artifactory/api/metadata/source/"+path+"?recursiveProperties=0", `{"props":{"quality":["verified"]}}`, http.StatusNoContent)
			} else {
				w.request(w.target, http.MethodPut, itemRoute("target", path), content, http.StatusCreated)
			}
			w.configure("**/*.bin", "", effect == "delete", effect != "delete")
			push, run := w.start("lost-ack-"+effect, "selected")
			method, route, success := http.MethodPut, itemRoute("target", path), http.StatusCreated
			action, phase := model.PushUpload, model.PushIntent
			if effect == "properties" {
				method, route, success = http.MethodPatch, "/artifactory/api/metadata/target/"+path, http.StatusNoContent
				phase = model.PushContentVerified
			}
			if effect == "delete" {
				method, success = http.MethodDelete, http.StatusNoContent
				action = model.PushDelete
			}
			run = w.untilPhase(push, run, action, phase)
			var cut atomic.Bool
			fixtureError := make(chan error, 1)
			w.bridge.intercept(func(writer http.ResponseWriter, request *http.Request) bool {
				if request.Method != method || request.URL.Path != route || !cut.CompareAndSwap(false, true) {
					return false
				}
				recorder := httptest.NewRecorder()
				w.bridge.proxy.ServeHTTP(recorder, request)
				if recorder.Code != success {
					forwardRecorded(writer, recorder)
					return true
				}
				connection, _, err := http.NewResponseController(writer).Hijack()
				if err != nil {
					fixtureError <- err
					return true
				}
				if err := connection.Close(); err != nil {
					fixtureError <- err
				}
				return true
			})
			_, err := push.Step(t.Context(), run.Reference, w.fence)
			if !errors.Is(err, outbound.ErrConnection) {
				t.Fatal("lost acknowledgement did not retain retryable uncertainty", err)
			}
			select {
			case err := <-fixtureError:
				t.Fatal("owned disconnect fixture failed", err)
			default:
			}
			pending := w.pending(run)
			if pending.Phase != phase || pending.Outcome != "" {
				t.Fatal("lost acknowledgement advanced unverified progress", pending.Phase, pending.Outcome)
			}
			if effect == "delete" {
				w.request(w.target, http.MethodGet, itemRoute("target", path), "", http.StatusNotFound)
			} else {
				data, _ := w.request(w.target, http.MethodGet, itemRoute("target", path), "", http.StatusOK)
				if string(data) != content {
					t.Fatal("target did not commit before acknowledgement was cut")
				}
			}
			push = w.restartSource()
			persisted, err := w.runtime.Store.PushProgress().Run(t.Context(), run.Reference)
			if err != nil || persisted.Reference != run.Reference {
				t.Fatal("restart lost logical run identity", err)
			}
			items := w.finish(push, persisted)
			if len(items) != 1 || items[0].Outcome != model.PushSucceeded || items[0].Phase != model.PushItemComplete {
				t.Fatal("restart did not independently verify committed effect", len(items))
			}
			if w.bridge.count(method, route) != 1 {
				t.Fatal("acknowledgement recovery repeated the committed mutation", method, w.bridge.count(method, route))
			}
			if effect == "delete" && !items[0].TargetAbsent {
				t.Fatal("delete success lacked observed target absence")
			}
			t.Log("native target commit survives lost acknowledgement and source runtime/database reopen without repeating the committed mutation")
		})
	}
}
