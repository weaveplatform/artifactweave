package federation_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
)

func TestAccSingleReplicationWorkerProcessesSQLite(t *testing.T) {
	qualifyReplicationWorkerProcesses(t, "sqlite")
}

func TestAccSingleReplicationWorkerProcessesPostgres(t *testing.T) {
	qualifyReplicationWorkerProcesses(t, "postgres")
}

func qualifyReplicationWorkerProcesses(t *testing.T, backend string) {
	t.Helper()
	for _, direction := range []model.ReplicationDirection{model.ReplicationPush, model.ReplicationPull} {
		t.Run(string(direction), func(t *testing.T) {
			var w *pushWorld
			repository, path, body, destination := "source", "selected/file.bin", "real queued push content", "target"
			if direction == model.ReplicationPush {
				w = newPushBackendWorld(t, backend)
				w.request(w.source, http.MethodPut, itemRoute("source", path), body, http.StatusCreated)
				w.configure("**/*.bin", "", false, false)
			} else {
				w, _, _ = newPullDriverWorld(t, backend, false)
				repository, path, body, destination = "pull-remote", pullFaultPath, pullFaultBody, "pull-remote-cache"
			}
			principal, err := w.runtime.Auth.Basic(t.Context(), "admin", "pw")
			if err != nil {
				t.Fatal(err)
			}
			coordinator, err := w.runtime.Federation.Coordinator()
			if err != nil {
				t.Fatal(err)
			}
			run, err := coordinator.Execute(accessapi.WithCaller(t.Context(), principal, nil), repository, "selected")
			if err != nil {
				t.Fatal("admit real queued transfer", err)
			}
			if _, err := w.runtime.Work.RunDue(t.Context(), replication.ExecutionWorkKind); err != nil {
				t.Fatal("dispatch real replication worker", err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for run.Phase != model.ExecutionComplete && run.Phase != model.ExecutionAborted {
				run, err = w.runtime.Store.Executions().Run(ctx, run.Reference)
				if err != nil {
					t.Fatal("read real queued execution", err)
				}
				select {
				case <-ticker.C:
				case <-ctx.Done():
					t.Fatal("actual worker did not settle selected effects", run.Phase, run.Outcomes)
				}
			}
			if run.Status() != model.ReplicationOK || run.Outcomes != (model.ExecutionOutcomes{Succeeded: 1}) {
				t.Fatal("queued result disagreed with committed transfer effects", run.Status(), run.Outcomes)
			}
			origin := w.target
			if direction == model.ReplicationPull {
				origin = w.source
			}
			actual, _ := w.request(origin, http.MethodGet, itemRoute(destination, path), "", http.StatusOK)
			if string(actual) != body {
				t.Fatal("actual queued worker did not populate verified destination bytes")
			}
			status, err := w.runtime.Store.Executions().PathStatus(t.Context(), run.Binding.TargetKey, run.Binding.Repository.Incarnation, "selected")
			if err != nil || status.Latest.Status != model.ReplicationOK || status.LastCompleted.IsZero() {
				t.Fatal("actual transfer did not publish matching scoped status", err)
			}
			unrelated, err := w.runtime.Store.Executions().PathStatus(t.Context(), run.Binding.TargetKey, run.Binding.Repository.Incarnation, "private")
			if err != nil || !unrelated.Latest.Started.IsZero() {
				t.Fatal("repository completion fabricated unrelated folder evidence", err)
			}
		})
	}
}
