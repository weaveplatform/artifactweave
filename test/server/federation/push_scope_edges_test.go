package federation_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
)

func TestAccSinglePushScopeEdgesSQLite(t *testing.T) {
	t.Parallel()
	qualifyPushScopeEdges(t, "sqlite")
}

func TestAccSinglePushScopeEdgesPostgres(t *testing.T) {
	t.Parallel()
	qualifyPushScopeEdges(t, "postgres")
}

func qualifyPushScopeEdges(t *testing.T, backend string) {
	t.Helper()
	t.Run("selected empty folder properties", func(t *testing.T) {
		w := newPushBackendWorld(t, backend)
		w.request(w.source, http.MethodPut, itemRoute("source", "selected")+"/", "", http.StatusCreated)
		w.request(w.source, http.MethodPatch, "/artifactory/api/metadata/source/selected?recursiveProperties=0", `{"props":{"folder-quality":["verified"]}}`, http.StatusNoContent)
		w.configure("**", "", false, true)
		push, run := w.start("selected-folder-properties", "selected")
		items := w.finish(push, run)
		if len(items) != 1 || !items[0].Source.Folder || items[0].Outcome != model.PushSucceeded {
			t.Fatal("selected folder was omitted from replication", len(items))
		}
		data, _ := w.request(w.target, http.MethodGet, "/artifactory/api/storage/target/selected?properties", "", http.StatusOK)
		var document struct {
			Properties map[string][]string `json:"properties"`
		}
		if json.Unmarshal(data, &document) != nil || !slices.Equal(document.Properties["folder-quality"], []string{"verified"}) {
			t.Fatal("selected folder properties were not transferred")
		}
	})
	t.Run("already absent target scope", func(t *testing.T) {
		w := newPushBackendWorld(t, backend)
		w.configure("**/*.bin", "", true, false)
		push, run := w.start("already-absent-scope", "selected")
		if items := w.finish(push, run); len(items) != 0 {
			t.Fatal("empty scope invented an effect", len(items))
		}
		w.request(w.target, http.MethodGet, itemRoute("target", "selected/file.bin"), "", http.StatusNotFound)
	})
}
