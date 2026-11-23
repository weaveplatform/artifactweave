package federation_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

func TestAccSingleReplicationOneShotAdmissionSurvivesRuntimeRestart(t *testing.T) {
	t.Parallel()
	for _, backend := range []string{"sqlite", "postgres"} {
		for _, direction := range []model.ReplicationDirection{model.ReplicationPush, model.ReplicationPull} {
			t.Run(backend+"/"+string(direction), func(t *testing.T) {
				var w *pushWorld
				repository, upstreamKey, destination := "source", "source", "target"
				if direction == model.ReplicationPush {
					w = newPushBackendWorld(t, backend)
					w.configure("**/*.bin", "", false, false)
				} else {
					w, _, _ = newPullDriverWorld(t, backend, false)
					repository, upstreamKey, destination = "pull-remote", "target", "pull-remote-cache"
				}
				upstream := w.source
				if direction == model.ReplicationPull {
					upstream = w.target
				}
				const path, content = "selected/restart.txt", "immutable one-shot selection survives restart"
				w.request(upstream, http.MethodPut, itemRoute(upstreamKey, path), content, http.StatusCreated)
				principal, err := w.runtime.Auth.Basic(t.Context(), "admin", "pw")
				if err != nil {
					t.Fatal(err)
				}
				coordinator, err := w.runtime.Federation.Coordinator()
				if err != nil {
					t.Fatal(err)
				}
				include, properties := "**/*.txt", false
				var run model.ExecutionRun
				var held []work.WorkItem
				caller := accessapi.WithCaller(t.Context(), principal, nil)
				// Hold the actual work before admission commits: either runtime's
				// background worker may otherwise advance it before we inspect it.
				err = w.runtime.Store.Tx(caller, func(ctx context.Context) error {
					var err error
					run, err = coordinator.ExecuteWithOptions(ctx, repository, "selected",
						replication.ExecutionInput{Include: &include, SyncProperties: &properties})
					if err != nil {
						return err
					}
					now := time.Now().UTC()
					held, err = w.runtime.Store.WorkQueue().Lease(ctx, work.WorkLease{
						Kinds: []string{replication.ExecutionWorkKind}, Holder: "restart-fixture", Limit: 1,
						Now: now, Until: now.Add(time.Hour),
					})
					return err
				})
				if err != nil || len(held) != 1 {
					t.Fatal("admit and hold one-shot before restart", len(held), err)
				}
				var queued model.ExecutionReference
				if err := json.Unmarshal(held[0].Payload, &queued); err != nil || queued != run.Reference {
					t.Fatal("restart hold did not identify the admitted execution", err)
				}
				options, err := w.runtime.Store.Executions().Options(t.Context(), run.Reference)
				if err != nil || !run.Binding.OneShot || run.Phase != model.ExecutionPending || options.Include != include {
					t.Fatal("fixture did not retain pending one-shot options", err)
				}
				configuration, err := w.runtime.Store.Replications().ForRepo(t.Context(), repository)
				if err != nil {
					t.Fatal(err)
				}
				w.restartSource()
				reopened, err := w.runtime.Store.Executions().Run(t.Context(), run.Reference)
				if err != nil || !reflect.DeepEqual(reopened, run) {
					t.Fatalf("runtime restart lost immutable execution provenance: err=%v before phase=%s version=%d updated=%s; after phase=%s version=%d updated=%s",
						err, run.Phase, run.Version, run.Updated, reopened.Phase, reopened.Version, reopened.Updated)
				}
				retained, err := w.runtime.Store.Executions().Options(t.Context(), run.Reference)
				if err != nil || retained != options {
					t.Fatal("runtime restart changed resolved or sealed execution options", err)
				}
				if count, err := w.runtime.Work.RunDue(t.Context(), replication.ExecutionWorkKind); err != nil || count != 0 {
					t.Fatal("restarted worker bypassed the retained fixture lease", count, err)
				}
				queue := w.runtime.Store.WorkQueue()
				pending, err := queue.Get(t.Context(), held[0].Kind, held[0].Key)
				if err != nil || pending.State != work.WorkLeased || pending.Generation != held[0].Generation || pending.LeasedBy != held[0].LeasedBy {
					t.Fatal("runtime restart lost the generation-fenced queue hold", err)
				}
				if err := queue.Release(t.Context(), pending.Kind, pending.Key, pending.Generation, time.Now().UTC()); err != nil {
					t.Fatal("release retained work for real resumed execution", err)
				}
				settlePublicExecution(t, w, repository)
				consumer := w.target
				if direction == model.ReplicationPull {
					consumer = w.source
				}
				actual, _ := w.request(consumer, http.MethodGet, itemRoute(destination, path), "", http.StatusOK)
				if string(actual) != content {
					t.Fatal("restarted worker did not transfer the immutable one-shot selection")
				}
				current, err := w.runtime.Store.Replications().ForRepo(t.Context(), repository)
				if err != nil || !reflect.DeepEqual(configuration, current) {
					t.Fatal("restarted one-shot work mutated the saved target", err)
				}
			})
		}
	}
}
