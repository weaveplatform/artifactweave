package federation_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
)

func TestAccSingleReplicationExplicitPushDestinationDoesNotMutateConfiguration(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		for _, mode := range []string{"without-configuration", "same-destination", "different-destination"} {
			t.Run(backend+"/"+mode, func(t *testing.T) {
				w := newPushBackendWorld(t, backend)
				if mode != "without-configuration" {
					w.configure("**/*.bin", "", false, false)
				}
				saved, err := w.runtime.Store.Replications().ForRepo(t.Context(), "source")
				if err != nil {
					t.Fatal(err)
				}
				destination := "target"
				if mode == "different-destination" {
					destination = "alternative"
					w.request(w.target, http.MethodPut, "/artifactory/api/repositories/alternative", `{"rclass":"local","packageType":"generic"}`, http.StatusOK)
					w.request(w.target, http.MethodPut, "/artifactory/api/security/permissions/explicit-alternative",
						`{"name":"explicit-alternative","repositories":["alternative"],"principals":{"users":{"receiver":["r","w","n","d"]}}}`, http.StatusOK)
				}
				url := w.bridge.URL + "/artifactory/" + destination
				const path, content = "selected/explicit.txt", "one-shot explicit destination bytes"
				w.request(w.source, http.MethodPut, itemRoute("source", path), content, http.StatusCreated)
				body, err := json.Marshal([]map[string]any{{"url": url, "username": "receiver", "password": "pw", "properties": false, "includePathPrefixPattern": "**/*.txt"}})
				if err != nil {
					t.Fatal(err)
				}
				publicExecute(t, w, "source/selected", "admin", string(body), http.StatusAccepted)
				explicitDestinationStatus(t, w, url, model.ReplicationIncomplete)
				if _, err := w.runtime.Work.RunDue(t.Context(), replication.ExecutionWorkKind); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				tick := time.NewTicker(10 * time.Millisecond)
				defer tick.Stop()
				for explicitDestinationStatus(t, w, url, "") != model.ReplicationOK {
					select {
					case <-tick.C:
					case <-ctx.Done():
						t.Fatal("explicit push did not settle actual selected effects")
					}
				}
				actual, _ := w.request(w.target, http.MethodGet, itemRoute(destination, path), "", http.StatusOK)
				if string(actual) != content {
					t.Fatal("explicit push did not populate the requested peer repository")
				}
				if mode == "different-destination" {
					w.request(w.target, http.MethodGet, itemRoute("target", path), "", http.StatusNotFound)
				}
				current, err := w.runtime.Store.Replications().ForRepo(t.Context(), "source")
				if err != nil || !reflect.DeepEqual(current, saved) {
					t.Fatal("explicit destination created or rewrote a saved configuration", err)
				}
				w.request(w.source, http.MethodPost, "/artifactory/api/system/replications/block?push=true", "", http.StatusOK)
				publicExecute(t, w, "source", "admin", string(body), http.StatusConflict)
			})
		}
	}
}

func explicitDestinationStatus(t *testing.T, w *pushWorld, url string, want model.ReplicationStatus) model.ReplicationStatus {
	t.Helper()
	body, _ := w.request(w.source, http.MethodGet, "/artifactory/api/replication/source", "", http.StatusOK)
	var view struct {
		Targets []struct {
			URL    string                  `json:"url"`
			Status model.ReplicationStatus `json:"status"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"password"`) {
		t.Fatal("status exposed private execution connection fields")
	}
	var state model.ReplicationStatus
	count := 0
	for _, target := range view.Targets {
		if target.URL == url {
			count++
			state = target.Status
		}
	}
	if count != 1 || want != "" && state != want {
		t.Fatal("explicit destination status is missing, duplicated, or disagrees with actual execution", count, state, want)
	}
	return state
}
