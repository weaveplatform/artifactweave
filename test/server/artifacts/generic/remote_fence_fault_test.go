package generic_test

// This file holds the authority fence suite of #1489: a remote read checks,
// after its upstream exchange, that the repository it started with is
// still current, and a statement the database fails to run there is the
// server's fault, never a superseded read or the upstream's.

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// fenceWorld is a remote world of package type pt whose router logs to
// logs.
func fenceWorld(t *testing.T, pt repo.PackageType) (*remoteWorld, *bytes.Buffer) {
	t.Helper()
	w := newRemoteWorld(t)
	w.c.PackageType = pt
	w.update()
	var logs bytes.Buffer
	w.h = harness.Router(harness.Config{
		Store: w.rt.Store, Blobs: w.rt.Blobs, Auth: w.rt.Auth, Tokens: w.rt.Tokens, Keys: w.rt.Keys,
		RemoteSources: w.rt.Artifacts.RemoteSources(), RemoteHTTP: w.rt.Artifacts.RemoteHTTP(),
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	return w, &logs
}

// heldRead sends a GET of file, which the upstream serves as "held", runs
// during while the upstream holds its body, and returns the answer.
func heldRead(t *testing.T, w *remoteWorld, file string, during func()) *httptest.ResponseRecorder {
	t.Helper()
	barrier := &fakeupstream.Barrier{}
	t.Cleanup(barrier.Release)
	w.up.Set("/"+file, fakeupstream.Resource{
		Body: []byte("held"), Fault: fakeupstream.Fault{BeforeBody: barrier},
	})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- w.request(http.MethodGet, "/artifactory/remote/"+file, "reader", "") }()
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

// renameTable renames the runtime's SQLite table from to to.
func renameTable(t *testing.T, w *remoteWorld, from, to string) {
	t.Helper()
	if _, err := w.rt.Store.DB().ExecContext(t.Context(),
		`ALTER TABLE `+from+` RENAME TO `+to); err != nil {
		t.Fatal(err)
	}
}

// TestAccGenericRemoteFenceAnswersAStoreFailureAsAServerFault: a remote
// read whose authority fence meets a statement the database fails to run,
// reading the repository or the proxy configuration its options are built
// from, answers 500 "internal error", logs the store's error once and
// caches nothing; once the database runs the statement again, the next
// read fetches the file. It answered 503 as a superseded read. No 7.x
// source has this fence; 7.161.26 answers a failure of its own database as
// a server fault [I] (#1489). The runtime's own SQLite database loses the
// table while the upstream holds the file, after the read started, so the
// failure comes up through the SQL store as a driver's does.
func TestAccGenericRemoteFenceAnswersAStoreFailureAsAServerFault(t *testing.T) {
	for _, c := range []struct {
		pt    repo.PackageType
		file  string
		table string
	}{
		{repo.Generic, "held.bin", "repository_secrets"},
		{repo.Generic, "held.bin", "config_documents"},
		{repo.Maven, mavenJar, "repository_secrets"},
		{repo.Maven, mavenJar, "config_documents"},
	} {
		t.Run(string(c.pt)+"/"+c.table, func(t *testing.T) {
			table := c.table
			w, logs := fenceWorld(t, c.pt)
			got := heldRead(t, w, c.file, func() { renameTable(t, w, table, table+"_held") })
			if got.Code != http.StatusInternalServerError ||
				got.Body.String() != `{"errors":[{"message":"internal error","status":500}]}`+"\n" {
				t.Fatalf("%d %q", got.Code, got.Body)
			}
			if n := strings.Count(logs.String(), "no such table: "+table); n != 1 {
				t.Fatalf("the fault was logged %d times, want once:\n%s", n, logs)
			}
			if _, err := w.rt.Store.Items().Get(t.Context(), "remote", true, c.file); !errors.Is(
				err, kernel.ErrNotFound,
			) {
				t.Fatalf("the file was cached: %v", err)
			}
			renameTable(t, w, table+"_held", table)
			w.up.Set("/"+c.file, fakeupstream.Resource{Body: []byte("held")})
			got = w.request(http.MethodGet, "/artifactory/remote/"+c.file, "reader", "")
			if got.Code != http.StatusOK || got.Body.String() != "held" {
				t.Fatalf("cured: %d %q", got.Code, got.Body)
			}
		})
	}
}

// TestAccGenericRemoteFenceKeepsItsOtherAnswers: options that read but
// changed while the read ran still supersede it, 503; and a statement the
// database fails to run as the options are read when a read starts, before
// any upstream exchange, still answers what it did, the 502 "Bad Gateway"
// of remote.PolicyError, unlogged. Only the fence's answer changes (#1489).
func TestAccGenericRemoteFenceKeepsItsOtherAnswers(t *testing.T) {
	for _, c := range []struct {
		pt   repo.PackageType
		file string
	}{{repo.Generic, "held.bin"}, {repo.Maven, mavenJar}} {
		w, _ := fenceWorld(t, c.pt)
		got := heldRead(t, w, c.file, func() {
			w.c.Password = "new-upstream-password"
			w.update()
		})
		if got.Code != http.StatusServiceUnavailable || got.Body.String() != supersededBody {
			t.Fatalf("%s changed options: %d %q", c.pt, got.Code, got.Body)
		}
	}

	w, logs := fenceWorld(t, repo.Generic)
	w.up.Set("/start.bin", fakeupstream.Resource{Body: []byte("start")})
	renameTable(t, w, "config_documents", "config_documents_held")
	got := w.request(http.MethodGet, "/artifactory/remote/start.bin", "reader", "")
	if got.Code != http.StatusBadGateway || got.Body.String() != badGatewayBody {
		t.Fatalf("failed options at the start: %d %q", got.Code, got.Body)
	}
	if strings.Contains(logs.String(), "no such table") {
		t.Fatalf("logged the start's failure:\n%s", logs)
	}
	if n := w.up.Count(http.MethodGet, "/start.bin"); n != 0 {
		t.Fatalf("upstream fetched the file %d times, want none", n)
	}
}

// The remote route's answers to a superseded read and to failed options,
// and a Maven artifact's path.
const (
	mavenJar       = "org/acme/lib/1.0/lib-1.0.jar"
	supersededBody = `{"errors":[{"message":"Service Unavailable","status":503}]}` + "\n"
	badGatewayBody = `{"errors":[{"message":"Bad Gateway","status":502}]}` + "\n"
)
