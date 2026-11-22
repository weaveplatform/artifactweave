package federation_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

func TestAccSingleReplicationNonadministratorSelectionLimitIsDurableAndTruthful(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		for _, direction := range []model.ReplicationDirection{model.ReplicationPush, model.ReplicationPull} {
			t.Run(backend+"/"+string(direction), func(t *testing.T) {
				var w *pushWorld
				repository, sourceKey, destination := "source", "source", "target"
				if direction == model.ReplicationPush {
					w = newPushBackendWorld(t, backend)
					w.configure("**/*.bin", "", true, false)
				} else {
					w, _, _ = newPullDriverWorld(t, backend, false)
					repository, sourceKey, destination = "pull-remote", "target", "pull-remote-cache"
					w.request(w.target, http.MethodDelete, itemRoute("target", pullFaultPath), "", http.StatusNoContent)
					w.request(w.source, http.MethodPost, replicationPath+repository, `{"syncDeletes":true}`, http.StatusOK)
				}
				upstream, consumer := w.source, w.target
				if direction == model.ReplicationPull {
					upstream, consumer = w.target, w.source
				}
				for _, path := range []string{"selected/a.bin", "selected/b.bin", "selected/c.bin"} {
					w.request(upstream, http.MethodPut, itemRoute(sourceKey, path), "bounded replication bytes", http.StatusCreated)
				}
				if err := w.runtime.Store.Users().Create(t.Context(), accesstest.User{Name: "bounded-executor", PasswordHash: support.CheapHash}); err != nil {
					t.Fatal(err)
				}
				if err := w.runtime.Store.Permissions().
					Create(t.Context(), accesstest.Permission{Name: "bounded-executor", Resources: map[accessapi.ResourceType]accesstest.Resource{
						accessapi.ResourceArtifact: {
							Targets: map[string]accesstest.Target{repository: {Include: []string{"**"}}},
							Actions: accesstest.Grants{
								Users: map[string]accessapi.Actions{
									"bounded-executor": accessapi.ActionRead | accessapi.ActionWrite | accessapi.ActionDelete | accessapi.ActionAnnotate | accessapi.ActionManage,
								},
							},
						},
					}}); err != nil {
					t.Fatal(err)
				}
				w.request(
					w.source,
					http.MethodPost,
					"/access/api/v1/projects",
					`{"project_key":"alpha","display_name":"Alpha","admin_privileges":{"manage_resources":true,"manage_remote_repository":true}}`,
					http.StatusCreated,
				)
				w.request(w.source, http.MethodPut, "/access/api/v1/projects/alpha/users/bounded-executor", `{"roles":["Project Admin"]}`, http.StatusOK)
				w.request(w.source, http.MethodPost, "/artifactory/api/repositories/"+repository, `{"projectKey":"alpha"}`, http.StatusOK)
				w.config.Search.UserQueryLimit = 2
				w.restartSource()
				if direction == model.ReplicationPull {
					consumer = w.source
				} else {
					upstream = w.source
				}
				principal, err := w.runtime.Auth.Basic(t.Context(), "bounded-executor", "pw")
				if err != nil {
					t.Fatal(err)
				}
				coordinator, err := w.runtime.Federation.Coordinator()
				if err != nil {
					t.Fatal(err)
				}
				run, err := coordinator.Execute(accessapi.WithCaller(t.Context(), principal, nil), repository, "selected")
				if err != nil || run.Binding.SelectionLimit != 2 {
					t.Fatal("nonadministrator selection ceiling was not retained at admission", err)
				}
				if _, err := w.runtime.Work.RunDue(t.Context(), replication.ExecutionWorkKind); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				tick := time.NewTicker(10 * time.Millisecond)
				defer tick.Stop()
				for run.Phase != model.ExecutionAborted {
					run, err = w.runtime.Store.Executions().Run(ctx, run.Reference)
					if err != nil {
						t.Fatal(err)
					}
					select {
					case <-tick.C:
					case <-ctx.Done():
						t.Fatal("selection ceiling left an unfinished or falsely successful run", run.Phase)
					}
				}
				if run.Failure != model.ExecutionSelectionLimited || run.Outcomes != (model.ExecutionOutcomes{Succeeded: 2}) {
					t.Fatal("selection ceiling changed actual counts or failure reason", run.Failure, run.Outcomes)
				}
				publicStatus(t, w, repository, "admin", http.StatusOK, model.ReplicationPartialFailure)
				for _, path := range []string{"selected/a.bin", "selected/b.bin"} {
					w.request(consumer, http.MethodGet, itemRoute(destination, path), "", http.StatusOK)
				}
				w.request(consumer, http.MethodGet, itemRoute(destination, "selected/c.bin"), "", http.StatusNotFound)
				// Exactly the limit is a complete selection. Pull reconciliation
				// retains the same files without counting them as new selections.
				w.request(upstream, http.MethodDelete, itemRoute(sourceKey, "selected/c.bin"), "", http.StatusNoContent)
				exact, err := coordinator.Execute(accessapi.WithCaller(t.Context(), principal, nil), repository, "selected")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := w.runtime.Work.RunDue(t.Context(), replication.ExecutionWorkKind); err != nil {
					t.Fatal(err)
				}
				waitLimitExecution(t, w, exact.Reference, model.ExecutionComplete)
				count, _, err := w.runtime.Store.Executions().SelectedPaths(t.Context(), exact.Reference, "selected/a.bin")
				if err != nil || count != 2 {
					t.Fatal("exact-limit reconciliation consumed extra file selections", count, err)
				}
				w.request(upstream, http.MethodPut, itemRoute(sourceKey, "selected/c.bin"), "administrator exemption bytes", http.StatusCreated)
				admin, err := w.runtime.Auth.Basic(t.Context(), "admin", "pw")
				if err != nil {
					t.Fatal(err)
				}
				unbounded, err := coordinator.Execute(accessapi.WithCaller(t.Context(), admin, nil), repository, "selected")
				if err != nil || unbounded.Binding.SelectionLimit != 0 {
					t.Fatal("administrator inherited a nonadministrator ceiling", err)
				}
				if _, err := w.runtime.Work.RunDue(t.Context(), replication.ExecutionWorkKind); err != nil {
					t.Fatal(err)
				}
				waitLimitExecution(t, w, unbounded.Reference, model.ExecutionComplete)
				w.request(consumer, http.MethodGet, itemRoute(destination, "selected/c.bin"), "", http.StatusOK)
			})
		}
	}
}

func waitLimitExecution(t *testing.T, w *pushWorld, ref model.ExecutionReference, phase model.ExecutionPhase) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		run, err := w.runtime.Store.Executions().Run(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		if run.Phase == phase {
			return
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal("bounded execution did not settle", run.Phase, run.Failure)
		}
	}
}
