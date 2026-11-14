package generic_test

// This file holds the availability bookkeeping suite of #1489: a remote read
// records its upstream's answer in the repository's availability state (the
// assumed-offline circuit), and a statement the database fails to run there
// is the server's fault, never the upstream's.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// refuseStateWrites makes the runtime's SQLite database refuse every
// update of a remote's source row, which holds its availability state, as
// a database that fails the statement would, until the returned function
// drops the trigger.
func refuseStateWrites(t *testing.T, w *harness.RemoteWorld) func() {
	t.Helper()
	db := w.Runtime.Store.DB()
	if _, err := db.ExecContext(t.Context(), `CREATE TRIGGER refuse_remote_state
		BEFORE UPDATE ON remote_sources BEGIN SELECT RAISE(ABORT, 'statement refused'); END`); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), `DROP TRIGGER refuse_remote_state`); err != nil {
			t.Fatal(err)
		}
	}
}

// heldAnswer sends a GET of path to w as reader, while the upstream holds
// res, its answer to file, runs during, and returns the answer.
func heldAnswer(
	t *testing.T,
	w *harness.RemoteWorld,
	path, file string,
	res fakeupstream.Resource,
	during func(),
) *httptest.ResponseRecorder {
	t.Helper()
	barrier := &fakeupstream.Barrier{}
	t.Cleanup(barrier.Release)
	res.Fault.BeforeHeaders = barrier
	w.Upstream.Set(file, res)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- w.Request(http.MethodGet, path, "reader", "") }()
	select {
	case <-barrier.Reached():
	case <-time.After(10 * time.Second):
		t.Fatal("fetch did not start")
	}
	during()
	barrier.Release()
	select {
	case got := <-done:
		return got
	case <-time.After(10 * time.Second):
		t.Fatal("request did not stop")
		return nil
	}
}

// loggedRecords counts the records of logs that name text. A failed
// statement inside a transaction comes back joined with the transaction's
// own record of it, so its text appears twice in one record.
func loggedRecords(logs, text string) int {
	n := 0
	for _, record := range strings.Split(logs, "\ntime=") {
		if strings.Contains(record, text) {
			n++
		}
	}
	return n
}

// wantCircuitUntouched fails t unless repository key counts no connection
// failure and is not assumed offline.
func wantCircuitUntouched(t *testing.T, w *harness.RemoteWorld, key string) {
	t.Helper()
	state, err := w.Runtime.Store.RemoteCache().State(t.Context(), key)
	if err != nil || state.ConnectionFailures != 0 || !state.OfflineUntil.IsZero() {
		t.Fatalf(
			"circuit %d, offline until %v: %v",
			state.ConnectionFailures,
			state.OfflineUntil,
			err,
		)
	}
}

// TestAccGenericRemoteFailedAvailabilityWriteIsAServerFault: a generic remote
// read, on a remote that stores artifacts and one that does not, whose
// upstream answers 200 or 404 and whose record of that answer in the
// repository's availability state meets a statement the database fails to
// run, answers 500 "internal error", logs the store's error once and leaves
// the circuit untouched. It answered 502 "Bad Gateway", unlogged, as the
// upstream's failure. 7.x keeps this state in memory, so no source says
// what a failed write of it answers [I] (#1489). Once the database runs the
// statement again the read answers as the upstream does. A source replaced
// while the upstream answered still supersedes the read, 503.
func TestAccGenericRemoteFailedAvailabilityWriteIsAServerFault(t *testing.T) {
	for _, store := range []bool{true, false} {
		for _, status := range []int{http.StatusOK, http.StatusNotFound} {
			fw, logs := fenceWorld(t, repo.Generic)
			fw.c.StoreArtifactsLocally = new(store)
			fw.update()
			w := fw.world()
			var drop func()
			got := heldAnswer(t, w, "/artifactory/remote/state.bin", "/state.bin",
				fakeupstream.Resource{Status: status, Body: []byte("state")},
				func() { drop = refuseStateWrites(t, w) })
			if got.Code != http.StatusInternalServerError ||
				got.Body.String() != `{"errors":[{"message":"internal error","status":500}]}`+"\n" {
				t.Fatalf("store=%v, upstream %d: %d %q", store, status, got.Code, got.Body)
			}
			if n := loggedRecords(logs.String(), "statement refused"); n != 1 {
				t.Fatalf("store=%v, upstream %d: the fault was logged %d times, want once:\n%s",
					store, status, n, logs)
			}
			drop()
			wantCircuitUntouched(t, w, "remote")
			w.Upstream.Set(
				"/state.bin",
				fakeupstream.Resource{Status: status, Body: []byte("state")},
			)
			harness.WantStatus(
				t,
				w.Request(http.MethodGet, "/artifactory/remote/state.bin", "reader", ""),
				status,
			)
		}
	}

	fw, _ := fenceWorld(t, repo.Generic)
	w := fw.world()
	got := heldAnswer(t, w, "/artifactory/remote/gone.bin", "/gone.bin",
		fakeupstream.Resource{Status: http.StatusNotFound},
		func() {
			fw.c.URL = fw.up.URL + "/replaced"
			fw.update()
		})
	if got.Code != http.StatusServiceUnavailable || got.Body.String() != supersededBody {
		t.Fatalf("source replaced: %d %q", got.Code, got.Body)
	}
}
