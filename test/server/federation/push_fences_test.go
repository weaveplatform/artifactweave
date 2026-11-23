package federation_test

import (
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestAccSinglePushCurrentFencesSQLite(t *testing.T) {
	t.Parallel()
	qualifyPushCurrentFences(t, "sqlite")
}

func TestAccSinglePushCurrentFencesPostgres(t *testing.T) {
	t.Parallel()
	qualifyPushCurrentFences(t, "postgres")
}

func qualifyPushCurrentFences(t *testing.T, backend string) {
	t.Helper()
	for _, change := range []string{"source overwrite", "source password rotation", "target password rotation", "target disabled"} {
		t.Run(change, func(t *testing.T) {
			w := newPushBackendWorld(t, backend)
			const path = "selected/file.bin"
			w.request(w.source, http.MethodPut, itemRoute("source", path), "original content", http.StatusCreated)
			w.configure("**/*.bin", "", false, false)
			push, run := w.start("current-fence", "selected")
			run = w.untilPhase(push, run, model.PushUpload, model.PushIntent)
			entered := make(chan struct{})
			release := make(chan struct{})
			var signal, unblock sync.Once
			t.Cleanup(func() { unblock.Do(func() { close(release) }) })
			w.bridge.intercept(func(writer http.ResponseWriter, request *http.Request) bool {
				if request.Method != http.MethodPut || request.URL.Path != itemRoute("target", path) {
					return false
				}
				signal.Do(func() { close(entered) })
				select {
				case <-release:
				case <-request.Context().Done():
					return true
				}
				w.bridge.proxy.ServeHTTP(writer, request)
				return true
			})
			done := make(chan error, 1)
			go func() { _, err := push.Step(t.Context(), run.Reference, w.fence); done <- err }()
			joined := false
			t.Cleanup(func() {
				unblock.Do(func() { close(release) })
				if !joined {
					select {
					case <-done:
					case <-time.After(10 * time.Second):
						t.Error("owned push worker did not join")
					}
				}
			})
			select {
			case <-entered:
			case err := <-done:
				joined = true
				t.Fatal("transfer ended before the block", err)
			case <-time.After(10 * time.Second):
				t.Fatal("transfer never reached owned HTTP gate")
			}
			switch change {
			case "source overwrite":
				w.request(w.source, http.MethodPut, itemRoute("source", path), "replacement content", http.StatusCreated)
			case "source password rotation":
				w.request(
					w.source,
					http.MethodPut,
					"/artifactory/api/security/users/replica-reader",
					`{"email":"replica-reader@example.invalid","password":"replacement-password"}`,
					http.StatusCreated,
				)
			case "target password rotation":
				w.request(w.source, http.MethodPost, replicationPath+"source", `{"password":"replacement-target-password"}`, http.StatusOK)
			case "target disabled":
				w.request(w.source, http.MethodPost, replicationPath+"source", `{"enabled":false}`, http.StatusOK)
			}
			unblock.Do(func() { close(release) })
			var stepError error
			select {
			case stepError = <-done:
				joined = true
			case <-time.After(10 * time.Second):
				t.Fatal("released push worker did not finish")
			}
			if change == "source overwrite" {
				if stepError != nil {
					t.Fatal("changed source did not record a safe skipped outcome", stepError)
				}
			} else if change == "source password rotation" {
				if !errors.Is(stepError, kernel.ErrBadCredentials) {
					t.Fatal("revoked original source credential was accepted", stepError)
				}
			} else if !errors.Is(stepError, kernel.ErrConflict) {
				t.Fatal("revoked target configuration was accepted", stepError)
			}
			items, _, err := w.runtime.Store.PushProgress().Items(t.Context(), run.Reference, false, kernel.Page{Limit: 10})
			if err != nil || len(items) != 1 {
				t.Fatal("missing retained obsolete intent", err)
			}
			if items[0].Outcome == model.PushSucceeded || items[0].VerifiedTarget != nil {
				t.Fatal("obsolete transfer published verified success", items[0].Outcome)
			}
			if change == "source overwrite" && (items[0].Outcome != model.PushSkipped || items[0].Reason != "source_changed") {
				t.Fatal("changed source lost its explicit outcome", items[0].Outcome, items[0].Reason)
			}
			data, _ := w.request(w.target, http.MethodGet, itemRoute("target", path), "", http.StatusOK)
			if string(data) != "original content" {
				t.Fatal("fixture did not permit original remote commit before local final fence")
			}
			if change != "source overwrite" {
				_, err := push.Step(t.Context(), run.Reference, w.fence)
				if err == nil {
					t.Fatal("obsolete run resumed after its authority/configuration was revoked")
				}
				if w.bridge.count(http.MethodPut, itemRoute("target", path)) != 1 {
					t.Fatal("revoked run performed another upload")
				}
			}
			t.Log("blocked native transfer rechecks current source/configuration after I/O and cannot publish obsolete success")
		})
	}
}
