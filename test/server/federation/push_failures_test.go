package federation_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
)

func TestAccSinglePushFailuresSQLite(t *testing.T)   { qualifyPushFailures(t, "sqlite") }
func TestAccSinglePushFailuresPostgres(t *testing.T) { qualifyPushFailures(t, "postgres") }

func qualifyPushFailures(t *testing.T, backend string) {
	t.Helper()
	for _, failure := range []string{"native401", "native403", "checksum", "temporary503", "socket timeout"} {
		t.Run(failure, func(t *testing.T) {
			w := newPushBackendWorld(t, backend)
			const path = "selected/file.bin"
			w.request(w.source, http.MethodPut, itemRoute("source", path), "verified source bytes", http.StatusCreated)
			w.configure("**/*.bin", "", false, false)
			switch failure {
			case "native401":
				w.request(w.source, http.MethodPost, replicationPath+"source", `{"password":"wrong-target-password"}`, http.StatusOK)
			case "native403":
				w.request(w.source, http.MethodPost, replicationPath+"source", `{"username":"receiver-readonly"}`, http.StatusOK)
			case "checksum":
				w.bridge.intercept(func(writer http.ResponseWriter, request *http.Request) bool {
					if request.Method != http.MethodGet || request.URL.Path != "/artifactory/api/storage/target/"+path || request.URL.Query().Has("properties") {
						return false
					}
					recorder := httptest.NewRecorder()
					w.bridge.proxy.ServeHTTP(recorder, request)
					if recorder.Code != http.StatusOK {
						forwardRecorded(writer, recorder)
						return true
					}
					var document map[string]any
					if json.Unmarshal(recorder.Body.Bytes(), &document) != nil {
						writer.WriteHeader(http.StatusBadGateway)
						return true
					}
					document["checksums"] = map[string]string{"sha256": strings.Repeat("0", 64)}
					writer.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(writer).Encode(document)
					return true
				})
			case "temporary503":
				w.bridge.intercept(func(writer http.ResponseWriter, request *http.Request) bool {
					if request.Method != http.MethodPut {
						return false
					}
					writer.WriteHeader(http.StatusServiceUnavailable)
					_, _ = io.WriteString(writer, "raw target diagnostic text")
					return true
				})
			case "socket timeout":
				w.request(w.source, http.MethodPost, replicationPath+"source", `{"socketTimeoutMillis":1000}`, http.StatusOK)
			}
			push, run := w.start("target-failure", "selected")
			if failure == "temporary503" || failure == "socket timeout" {
				run = w.untilPhase(push, run, model.PushUpload, model.PushIntent)
				entered := make(chan struct{})
				release := make(chan struct{})
				var unblock, signal sync.Once
				t.Cleanup(func() { unblock.Do(func() { close(release) }) })
				if failure == "socket timeout" {
					w.bridge.intercept(func(_ http.ResponseWriter, request *http.Request) bool {
						if request.Method != http.MethodPut {
							return false
						}
						_, _ = io.Copy(io.Discard, request.Body)
						signal.Do(func() { close(entered) })
						select {
						case <-request.Context().Done():
						case <-release:
						}
						return true
					})
				}
				started := time.Now()
				_, err := push.Step(t.Context(), run.Reference, w.fence)
				var permanent port.PushTargetFailure
				if err == nil || errors.As(err, &permanent) {
					t.Fatal("transient failure was accepted or made permanent", err)
				}
				if failure == "socket timeout" {
					select {
					case <-entered:
					default:
						t.Fatal("timeout fixture did not reach the upload")
					}
					if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 5*time.Second {
						t.Fatal("configured socket timeout was not honored", err)
					}
				}
				pending := w.pending(run)
				if pending.Phase != model.PushIntent || pending.Outcome != "" || pending.VerifiedTarget != nil {
					t.Fatal("transient failure advanced unverified evidence")
				}
				encoded, _ := json.Marshal(pending)
				if strings.Contains(string(encoded), "raw target diagnostic text") {
					t.Fatal("target body was persisted in progress")
				}
				unblock.Do(func() { close(release) })
				w.bridge.intercept(nil)
				items := w.finish(push, run)
				if len(items) != 1 || items[0].Outcome != model.PushSucceeded {
					t.Fatal("transient failure could not safely resume")
				}
				return
			}
			items := w.finish(push, run)
			want := map[string]string{"native401": "target_unauthorized", "native403": "target_forbidden", "checksum": "target_checksum_mismatch"}[failure]
			if len(items) != 1 || items[0].Outcome != model.PushItemFailed || items[0].Reason != want || items[0].VerifiedTarget != nil {
				t.Fatal("target refusal/checksum discrepancy produced success", len(items), want)
			}
			if failure != "checksum" {
				w.request(w.target, http.MethodGet, itemRoute("target", path), "", http.StatusNotFound)
			}
			t.Log("native target refusal or controlled metadata discrepancy retains a closed failed item result, never verified success")
		})
	}
}
