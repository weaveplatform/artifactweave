package federation_test

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
)

func TestAccSingleReplicationOneShotSelectionPropertiesAndDeletes(t *testing.T) {
	t.Parallel()
	for _, backend := range []string{"sqlite", "postgres"} {
		for _, direction := range []model.ReplicationDirection{model.ReplicationPush, model.ReplicationPull} {
			t.Run(backend+"/"+string(direction), func(t *testing.T) {
				var w *pushWorld
				repository, upstreamRepo, destination := "source", "source", "target"
				if direction == model.ReplicationPush {
					w = newPushBackendWorld(t, backend)
					w.configure("**/*.bin", "", false, false)
				} else {
					w, _, _ = newPullDriverWorld(t, backend, false)
					repository, upstreamRepo, destination = "pull-remote", "target", "pull-remote-cache"
				}
				upstream, consumer := w.source, w.target
				if direction == model.ReplicationPull {
					upstream, consumer = w.target, w.source
				}
				const selected, excluded = "selected/one-shot.txt", "selected/excluded.txt"
				for _, path := range []string{selected, excluded} {
					w.request(upstream, http.MethodPut, itemRoute(upstreamRepo, path), "one-shot transfer bytes", http.StatusCreated)
				}
				w.request(upstream, http.MethodPut, "/artifactory/api/storage/"+upstreamRepo+"/"+selected+"?properties=qualification=one-shot", "", http.StatusNoContent)
				saved, err := w.runtime.Store.Replications().ForRepo(t.Context(), repository)
				if err != nil || len(saved) != 1 {
					t.Fatal("saved configuration fixture unavailable", err)
				}
				body := `[{"properties":true,"delete":true,"includePathPrefixPattern":"**/*.txt","excludePathPrefixPattern":"**/excluded.txt"}]`
				publicExecute(t, w, repository+"/selected", "admin", body, http.StatusAccepted)
				settlePublicExecution(t, w, repository)
				actual, _ := w.request(consumer, http.MethodGet, itemRoute(destination, selected), "", http.StatusOK)
				if string(actual) != "one-shot transfer bytes" {
					t.Fatal("per-run selection did not transfer the requested bytes")
				}
				properties, _ := w.request(consumer, http.MethodGet, "/artifactory/api/storage/"+destination+"/"+selected+"?properties", "", http.StatusOK)
				if !strings.Contains(string(properties), "one-shot") {
					t.Fatal("per-run property synchronization was discarded")
				}
				w.request(consumer, http.MethodGet, itemRoute(destination, excluded), "", http.StatusNotFound)
				w.request(upstream, http.MethodDelete, itemRoute(upstreamRepo, selected), "", http.StatusNoContent)
				body = `[{"delete":true,"includePathPrefixPattern":"**/*.txt","excludePathPrefixPattern":"**/excluded.txt"}]`
				publicExecute(t, w, repository+"/selected", "admin", body, http.StatusAccepted)
				settlePublicExecution(t, w, repository)
				w.request(consumer, http.MethodGet, itemRoute(destination, selected), "", http.StatusNotFound)
				current, err := w.runtime.Store.Replications().ForRepo(t.Context(), repository)
				if err != nil || !reflect.DeepEqual(saved, current) {
					t.Fatal("one-shot execution changed the saved configuration", err)
				}
			})
		}
	}
}

func settlePublicExecution(t *testing.T, w *pushWorld, repository string) {
	t.Helper()
	publicStatus(t, w, repository, "admin", http.StatusOK, model.ReplicationIncomplete)
	if _, err := w.runtime.Work.RunDue(t.Context(), replication.ExecutionWorkKind); err != nil {
		t.Fatal("dispatch one-shot replication", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for publicStatus(t, w, repository, "admin", http.StatusOK, "") != model.ReplicationOK {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("one-shot replication did not settle actual effects")
		}
	}
}
