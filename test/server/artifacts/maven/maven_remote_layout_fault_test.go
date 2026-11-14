package maven_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// layoutFaultWorld is a Maven-family remote of package type pt and layout
// name whose router logs to logs.
func layoutFaultWorld(
	t *testing.T,
	pt repo.PackageType,
	name string,
) (*remoteWorld, *bytes.Buffer) {
	t.Helper()
	w := newMavenRemoteWorld(t)
	w.c.PackageType, w.c.RepoLayoutRef = pt, name
	w.update()
	var logs bytes.Buffer
	w.h = harness.Router(harness.Config{
		Store: w.rt.Store, Blobs: w.rt.Blobs, Auth: w.rt.Auth, Tokens: w.rt.Tokens, Keys: w.rt.Keys,
		RemoteSources: w.rt.Artifacts.RemoteSources(), RemoteHTTP: w.rt.Artifacts.RemoteHTTP(),
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	return w, &logs
}

// renameTable renames the runtime's SQLite table from to to.
func renameTable(t *testing.T, w *remoteWorld, from, to string) {
	t.Helper()
	if _, err := w.rt.Store.DB().ExecContext(t.Context(),
		`ALTER TABLE `+from+` RENAME TO `+to); err != nil {
		t.Fatal(err)
	}
}

// TestAccMavenRemoteLayoutStoreFault: a Maven-family remote read whose
// repository layout cannot be read, because the database failed to run
// the statement, answers the server's storage fault, 500 "internal
// error", logs the store's error once and asks the upstream nothing. An
// artifact, whose source read maps its path through the layout, answered
// 400 "Bad Request", as if the client had asked for a path the layout
// refuses; a non-unique snapshot, which the layout resolves first, already
// answered 500 and is kept as a guard. 7.x's answer to a failure of its own database is not
// documented, so 500 is [I], as #1470's is. The runtime's own SQLite
// database loses the table, so the failure comes up through the SQL store
// as a driver's does.
func TestAccMavenRemoteLayoutStoreFault(t *testing.T) {
	for _, c := range []struct {
		name   string
		pt     repo.PackageType
		layout string
		path   string
	}{
		{"maven", repo.Maven, "maven-2-default", "org/acme/app/1.0/app-1.0.jar"},
		{"gradle", repo.Gradle, "gradle-default", "org.acme/app/1.0/app-1.0.jar"},
		{"ivy", repo.Ivy, "ivy-default", "org.acme/app/1.0/jars/app-1.0.jar"},
		{"snapshot", repo.Maven, "maven-2-default", "org/acme/app/1.1-SNAPSHOT/app-1.1-SNAPSHOT.jar"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, logs := layoutFaultWorld(t, c.pt, c.layout)
			mavenSource(w, c.path, sourceJAR(t, "layout"), "application/java-archive")
			renameTable(t, w, "config_documents", "config_documents_held")
			got := w.request(http.MethodGet, "/artifactory/remote/"+c.path, "reader", "")
			if got.Code != http.StatusInternalServerError ||
				got.Body.String() != `{"errors":[{"message":"internal error","status":500}]}`+"\n" {
				t.Fatalf("%d %q", got.Code, got.Body)
			}
			if n := strings.Count(logs.String(), "no such table: config_documents"); n != 1 {
				t.Fatalf("the fault was logged %d times, want once:\n%s", n, logs)
			}
			if n := w.up.Count("", ""); n != 0 {
				t.Fatal("asked the upstream", w.up.Requests())
			}
			renameTable(t, w, "config_documents_held", "config_documents")
			if c.name == "snapshot" {
				return
			}
			remoteStatus(
				t,
				w.request(http.MethodGet, "/artifactory/remote/"+c.path, "reader", ""),
				200,
			)
		})
	}
}

// TestAccMavenRemoteLayoutRefusalKeepsItsAnswer: a path the layout refuses
// still answers 400 "Bad Request" before any upstream request: a remote
// whose upstream has another layout cannot map it.
func TestAccMavenRemoteLayoutRefusalKeepsItsAnswer(t *testing.T) {
	w, logs := layoutFaultWorld(t, repo.Gradle, "gradle-default")
	w.c.RemoteRepoLayoutRef = "maven-2-default"
	w.update()
	got := w.request(http.MethodGet, "/artifactory/remote/no-layout/file.bin", "reader", "")
	if got.Code != http.StatusBadRequest ||
		got.Body.String() != `{"errors":[{"message":"Bad Request","status":400}]}`+"\n" {
		t.Fatalf("%d %q", got.Code, got.Body)
	}
	if n := w.up.Count("", ""); n != 0 || strings.Contains(logs.String(), "level=ERROR") {
		t.Fatal("asked the upstream or logged a refusal", w.up.Requests(), logs)
	}
}

// TestAccMavenRemoteUndefinedLayoutRefuses: a read of a remote whose
// layout is not defined still answers 400 "Bad Request", unlogged, before
// any upstream request; only a statement the store failed to run is the
// server's fault.
func TestAccMavenRemoteUndefinedLayoutRefuses(t *testing.T) {
	w, logs := layoutFaultWorld(t, repo.Maven, "no-such-layout")
	got := w.request(
		http.MethodGet,
		"/artifactory/remote/org/acme/app/1.0/app-1.0.jar",
		"reader",
		"",
	)
	if got.Code != http.StatusBadRequest ||
		got.Body.String() != `{"errors":[{"message":"Bad Request","status":400}]}`+"\n" {
		t.Fatalf("%d %q", got.Code, got.Body)
	}
	if n := w.up.Count("", ""); n != 0 || strings.Contains(logs.String(), "level=ERROR") {
		t.Fatal("asked the upstream or logged a refusal", w.up.Requests(), logs)
	}
}
