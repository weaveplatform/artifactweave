package npm_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// TestAccNPMRemoteFailedAvailabilityWriteIsAServerFault: a remote npm
// tarball, on a remote that stores artifacts and one that does not, whose
// upstream answers 200 or 404 and whose record of that answer in the
// repository's availability state meets a statement the database fails to
// run, answers 500 "internal error", logged once, and leaves the circuit
// untouched; it answered 502, as the upstream's failure. 7.x keeps this
// state in memory, so no source says what a failed write of it answers [I]
// (#1489). The runtime's SQLite database refuses the update through a
// trigger, so the failure comes up through the SQL store as a driver's
// does.
func TestAccNPMRemoteFailedAvailabilityWriteIsAServerFault(t *testing.T) {
	for _, store := range []bool{true, false} {
		for _, c := range []struct {
			file   string
			status int
		}{{"/pkg/-/pkg-1.0.0.tgz", http.StatusOK}, {"/pkg/-/pkg-1.0.1.tgz", http.StatusNotFound}} {
			w, tarballs := remoteTarballWorld(t, store, "application/x-gzip")
			var logs bytes.Buffer
			w.H = harness.Router(harness.Config{
				Store: w.Runtime.Store, Blobs: w.Runtime.Blobs, Auth: w.Runtime.Auth,
				Tokens: w.Runtime.Tokens, Keys: w.Runtime.Keys,
				RemoteSources: w.Runtime.Artifacts.RemoteSources(),
				RemoteHTTP:    w.Runtime.Artifacts.RemoteHTTP(),
				Logger:        slog.New(slog.NewTextHandler(&logs, nil)),
			})
			res := fakeupstream.Resource{Status: c.status, Body: tarballs["1.0.0"]}
			barrier := &fakeupstream.Barrier{}
			t.Cleanup(barrier.Release)
			res.Fault.BeforeHeaders = barrier
			w.Upstream.Set(c.file, res)
			path := "/artifactory/api/npm/remote" + c.file
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- w.Request(http.MethodGet, path, "reader", "") }()
			select {
			case <-barrier.Reached():
			case <-time.After(10 * time.Second):
				t.Fatalf("store=%v, upstream %d: fetch did not start", store, c.status)
			}
			db := w.Runtime.Store.DB()
			if _, err := db.ExecContext(t.Context(), `CREATE TRIGGER refuse_remote_state
				BEFORE UPDATE ON remote_sources BEGIN SELECT RAISE(ABORT, 'statement refused'); END`); err != nil {
				t.Fatal(err)
			}
			barrier.Release()
			var got *httptest.ResponseRecorder
			select {
			case got = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("request did not stop")
			}
			if got.Code != http.StatusInternalServerError ||
				got.Body.String() != `{"error":"internal error"}`+"\n" {
				t.Fatalf("store=%v, upstream %d: %d %q", store, c.status, got.Code, got.Body)
			}
			records := 0
			for _, record := range strings.Split(logs.String(), "\ntime=") {
				if strings.Contains(record, "statement refused") {
					records++
				}
			}
			if records != 1 {
				t.Fatalf("store=%v, upstream %d: the fault was logged %d times, want once:\n%s",
					store, c.status, records, logs.String())
			}
			if _, err := db.ExecContext(
				t.Context(),
				`DROP TRIGGER refuse_remote_state`,
			); err != nil {
				t.Fatal(err)
			}
			state, err := w.Runtime.Store.RemoteCache().State(t.Context(), "remote")
			if err != nil || state.ConnectionFailures != 0 || !state.OfflineUntil.IsZero() {
				t.Fatalf("circuit %d, offline until %v: %v", state.ConnectionFailures,
					state.OfflineUntil, err)
			}
			w.Upstream.Set(c.file, fakeupstream.Resource{Status: c.status, Body: tarballs["1.0.0"]})
			harness.WantStatus(t, w.Request(http.MethodGet, path, "reader", ""), c.status)
		}
	}
}
