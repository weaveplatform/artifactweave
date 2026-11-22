package federation_test

import (
	"net/http"
	"reflect"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
)

func TestAccSingleReplicationOneShotAdmissionSurvivesRuntimeRestart(t *testing.T) {
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
				run, err := coordinator.ExecuteWithOptions(accessapi.WithCaller(t.Context(), principal, nil), repository, "selected",
					replication.ExecutionInput{Include: &include, SyncProperties: &properties})
				if err != nil {
					t.Fatal("admit one-shot before restart", err)
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
					t.Fatal("runtime restart lost immutable execution provenance", err)
				}
				retained, err := w.runtime.Store.Executions().Options(t.Context(), run.Reference)
				if err != nil || retained != options {
					t.Fatal("runtime restart changed resolved or sealed execution options", err)
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
