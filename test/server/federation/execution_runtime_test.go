package federation_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/test/server/support"
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
			publicStatus(t, w, repository+"/selected", "admin", http.StatusOK, model.ReplicationNeverRun)
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
			publicStatus(t, w, repository, "admin", http.StatusOK, model.ReplicationIncomplete)
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
			// Public admission must enqueue the real driver, not merely publish
			// an accepted timestamp over the earlier completed execution.
			updated := body + " after public execute"
			writeOrigin, writeRepository := w.source, repository
			if direction == model.ReplicationPull {
				writeOrigin, writeRepository = w.target, "target"
			}
			w.request(writeOrigin, http.MethodPut, itemRoute(writeRepository, path), updated, http.StatusCreated)
			publicExecute(t, w, repository+"/selected", "admin", "[]", http.StatusAccepted)
			publicStatus(t, w, repository, "admin", http.StatusOK, model.ReplicationIncomplete)
			if _, err := w.runtime.Work.RunDue(t.Context(), replication.ExecutionWorkKind); err != nil {
				t.Fatal(err)
			}
			second, stop := context.WithTimeout(t.Context(), 20*time.Second)
			defer stop()
			for publicStatus(t, w, repository, "admin", http.StatusOK, "") != model.ReplicationOK {
				select {
				case <-ticker.C:
				case <-second.Done():
					t.Fatal("public execute did not settle actual effects")
				}
			}
			actual, _ = w.request(origin, http.MethodGet, itemRoute(destination, path), "", http.StatusOK)
			if string(actual) != updated {
				t.Fatal("accepted public execution did not transfer changed bytes")
			}
			for _, malformed := range []string{"{}", "null", "[null]", "[{},{}]", "[]{}"} {
				publicExecute(t, w, repository, "admin", malformed, http.StatusBadRequest)
			}
			status, err := w.runtime.Store.Executions().PathStatus(t.Context(), run.Binding.TargetKey, run.Binding.Repository.Incarnation, "selected")
			if err != nil || status.Latest.Status != model.ReplicationOK || status.LastCompleted.IsZero() {
				t.Fatal("actual transfer did not publish matching scoped status", err)
			}
			publicStatus(t, w, repository+"/selected", "admin", http.StatusOK, model.ReplicationOK)
			publicStatus(t, w, repository+"/private", "admin", http.StatusOK, model.ReplicationNeverRun)
			if err := w.runtime.Store.Users().Create(t.Context(), accesstest.User{Name: "status-reader", PasswordHash: support.CheapHash}); err != nil {
				t.Fatal(err)
			}
			if err := w.runtime.Store.Permissions().
				Create(t.Context(), accesstest.Permission{Name: "selected-status-only", Resources: map[accessapi.ResourceType]accesstest.Resource{
					accessapi.ResourceArtifact: {
						Targets: map[string]accesstest.Target{repository: {Include: []string{"selected/**"}}},
						Actions: accesstest.Grants{Users: map[string]accessapi.Actions{"status-reader": accessapi.ActionRead}},
					},
				}}); err != nil {
				t.Fatal(err)
			}
			publicStatus(t, w, repository+"/selected", "status-reader", http.StatusOK, model.ReplicationOK)
			publicStatus(t, w, repository, "status-reader", http.StatusForbidden, "")
			publicStatus(t, w, repository+"/private", "status-reader", http.StatusForbidden, "")
			publicExecute(t, w, repository+"/selected", "status-reader", "", http.StatusForbidden)
			publicExecute(t, w, repository+"/selected", "", "", http.StatusUnauthorized)
			w.request(w.source, http.MethodPost, replicationPath+repository, `{"enabled":false}`, http.StatusOK)
			publicExecute(t, w, repository, "admin", "", http.StatusConflict)
			w.request(w.source, http.MethodPost, replicationPath+repository, `{"enabled":true}`, http.StatusOK)
			w.request(w.source, http.MethodPost, "/artifactory/api/system/replications/block", "", http.StatusOK)
			publicExecute(t, w, repository, "admin", "", http.StatusConflict)
			w.request(w.source, http.MethodPost, "/artifactory/api/system/replications/unblock", "", http.StatusOK)
			if direction == model.ReplicationPull {
				w.request(w.source, http.MethodPost, "/artifactory/api/repositories/"+repository, `{"offline":true}`, http.StatusOK)
				publicExecute(t, w, repository, "admin", "", http.StatusConflict)
				w.request(w.source, http.MethodPost, "/artifactory/api/repositories/"+repository, `{"offline":false,"storeArtifactsLocally":false}`, http.StatusOK)
				publicExecute(t, w, repository, "admin", "", http.StatusConflict)
			}

			publicStatus(t, w, repository+"/selected", "", http.StatusUnauthorized, "")
			unrelated, err := w.runtime.Store.Executions().PathStatus(t.Context(), run.Binding.TargetKey, run.Binding.Repository.Incarnation, "private")
			if err != nil || !unrelated.Latest.Started.IsZero() {
				t.Fatal("repository completion fabricated unrelated folder evidence", err)
			}
		})
	}
}

func publicStatus(t *testing.T, w *pushWorld, path, user string, want int, state model.ReplicationStatus) model.ReplicationStatus {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, w.source+"/artifactory/api/replication/"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if user != "" {
		request.SetBasicAuth(user, "pw")
	}
	response, err := w.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != want {
		t.Fatalf("public replication status: got%d want%d: %s err=%v", response.StatusCode, want, body, err)
	}
	if want != http.StatusOK {
		return ""
	}
	var view struct {
		Status  model.ReplicationStatus `json:"status"`
		Targets []struct {
			Status model.ReplicationStatus `json:"status"`
			URL    string                  `json:"url"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	actual := view.Status
	if len(view.Targets) > 0 {
		actual = view.Targets[0].Status
	}
	if state != "" && actual != state {
		t.Fatalf("public replication status got%s want%s: %s", actual, state, body)
	}
	return actual
}

func publicExecute(t *testing.T, w *pushWorld, path, user, body string, want int) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, w.source+"/artifactory/api/replication/execute/"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if user != "" {
		request.SetBasicAuth(user, "pw")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := w.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != want || want == http.StatusAccepted && len(data) != 0 {
		t.Fatalf("public execute got%d want%d: %s err=%v", response.StatusCode, want, data, err)
	}
}
