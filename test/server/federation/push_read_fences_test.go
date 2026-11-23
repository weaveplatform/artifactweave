package federation_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
)

func TestAccSinglePushReadFencesSQLite(t *testing.T) {
	t.Parallel()
	qualifyPushReadFences(t, "sqlite")
}

func TestAccSinglePushReadFencesPostgres(t *testing.T) {
	t.Parallel()
	qualifyPushReadFences(t, "postgres")
}

func qualifyPushReadFences(t *testing.T, backend string) {
	t.Helper()
	for _, effect := range []string{"properties", "delete"} {
		t.Run(effect, func(t *testing.T) {
			w := newPushBackendWorld(t, backend)
			const path = "selected/file.bin"
			action, phase := model.PushUpload, model.PushContentVerified
			method, route := http.MethodPatch, "/artifactory/api/metadata/target/"+path
			if effect == "properties" {
				w.request(w.source, http.MethodPut, itemRoute("source", path), "source content", http.StatusCreated)
				w.request(w.source, http.MethodPatch, "/artifactory/api/metadata/source/"+path+"?recursiveProperties=0", `{"props":{"quality":["source"]}}`, http.StatusNoContent)
			} else {
				action, phase = model.PushDelete, model.PushIntent
				method, route = http.MethodDelete, itemRoute("target", path)
			}
			w.request(w.target, http.MethodPut, itemRoute("target", path), "target content", http.StatusCreated)
			w.request(
				w.target,
				http.MethodPatch,
				"/artifactory/api/metadata/target/"+path+"?recursiveProperties=0",
				`{"props":{"destination-only":["keep"]}}`,
				http.StatusNoContent,
			)
			w.configure("**/*.bin", "", effect == "delete", effect == "properties")
			push, run := w.start("slow-read-fence", "selected")
			run = w.untilPhase(push, run, action, phase)
			entered, release := make(chan struct{}), make(chan struct{})
			var reads atomic.Int32
			var unblock sync.Once
			t.Cleanup(func() { unblock.Do(func() { close(release) }) })
			w.bridge.intercept(func(writer http.ResponseWriter, request *http.Request) bool {
				if request.Method != http.MethodGet || request.URL.Path != "/artifactory/api/storage/target/"+path || !request.URL.Query().Has("properties") {
					return false
				}
				// The second properties read belongs to the adapter's final
				// inspection, after the driver's pre-operation observation.
				if reads.Add(1) != 2 {
					return false
				}
				recorder := httptest.NewRecorder()
				w.bridge.proxy.ServeHTTP(recorder, request)
				close(entered)
				select {
				case <-release:
				case <-request.Context().Done():
					return true
				}
				forwardRecorded(writer, recorder)
				return true
			})
			done := make(chan error, 1)
			joined := false
			go func() { _, err := push.Step(t.Context(), run.Reference, w.fence); done <- err }()
			t.Cleanup(func() {
				unblock.Do(func() { close(release) })
				if !joined {
					select {
					case <-done:
					case <-time.After(10 * time.Second):
						t.Error("owned slow-read worker did not join")
					}
				}
			})
			select {
			case <-entered:
			case err := <-done:
				joined = true
				t.Fatal("mutation ended before adapter inspection gate", err)
			case <-time.After(10 * time.Second):
				t.Fatal("adapter inspection gate was not reached")
			}
			w.request(w.source, http.MethodPost, replicationPath+"source", `{"enabled":false}`, http.StatusOK)
			unblock.Do(func() { close(release) })
			select {
			case <-done:
				joined = true
			case <-time.After(10 * time.Second):
				t.Fatal("released slow-read worker did not finish")
			}
			if w.bridge.count(method, route) != 0 {
				t.Fatal("obsolete mutation started after configuration was revoked during its final read", method)
			}
			w.request(w.target, http.MethodGet, itemRoute("target", path), "", http.StatusOK)
		})
	}
}
