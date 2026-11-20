package federation_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

const replicationPath = "/artifactory/api/replications/"

type replicationWorld struct {
	t    *testing.T
	db   *inmem.Store
	h    http.Handler
	logs bytes.Buffer
}

func newReplicationWorld(t *testing.T) *replicationWorld {
	t.Helper()
	db := inmem.New()
	support.Seed(t, db)
	for _, config := range []repo.Config{
		{Key: "local", Class: repo.Local, PackageType: repo.Generic},
		{Key: "remote", Class: repo.Remote, PackageType: repo.Generic, URL: "https://upstream.example/artifactory/source"},
		{Key: "virtual", Class: repo.Virtual, PackageType: repo.Generic, Repositories: []string{"local"}},
	} {
		if err := db.Repos().Create(t.Context(), *repo.Defaults(&config)); err != nil {
			t.Fatal(err)
		}
	}
	auth, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{Hash: support.TestHash, Groups: db.Groups()})
	if err != nil {
		t.Fatal(err)
	}
	w := &replicationWorld{t: t, db: db}
	w.h = harness.Router(harness.Config{Store: db, Auth: auth, PasswordHash: support.TestHash, Logger: slog.New(slog.NewTextHandler(&w.logs, nil))})
	return w
}

func (w *replicationWorld) request(method, path, body string, want int) *httptest.ResponseRecorder {
	w.t.Helper()
	headers := support.Basic("admin", "pw")
	if body != "" {
		headers["Content-Type"] = "application/json"
	}
	response := support.Do(w.t, w.h, method, path, headers, body)
	if response.Code != want {
		w.t.Fatalf("%s %s: got %d want %d: %s", method, path, response.Code, want, response.Body.String())
	}
	return response
}

func (w *replicationWorld) configuration(key string) map[string]any {
	w.t.Helper()
	r := w.request("GET", replicationPath+key, "", 200)
	var config map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &config); err != nil {
		w.t.Fatal(err)
	}
	return config
}

func TestAccSingleReplicationConfigurationLifecycle(t *testing.T) {
	w := newReplicationWorld(t)
	const secret = "fake-replication-password"
	const create = `{"url":"https://target.example/artifactory/destination","username":"replicator","password":"` + secret + `","enabled":false,"syncDeletes":true,"repoKey":"spoofed","replicationKey":"caller-owned"}`
	w.request("PUT", replicationPath+"local", create, 201)
	first := w.configuration("local")
	key, ok := first["replicationKey"].(string)
	if !ok || key == "" || key == "caller-owned" || first["repoKey"] != "local" {
		t.Fatalf("server identity not assigned: %+v", first)
	}
	if first["enabled"] != false || first["syncProperties"] != true || first["socketTimeoutMillis"] != float64(15000) {
		t.Fatalf("native configuration defaults lost: %+v", first)
	}
	if password, ok := first["password"].(string); !ok || password == secret || password == "" {
		t.Fatal("configured password was not masked")
	}
	w.request("POST", replicationPath+"local", `{"enabled":true}`, 200)
	updated := w.configuration("local")
	if updated["replicationKey"] != key || updated["enabled"] != true || updated["syncDeletes"] != true {
		t.Fatal("partial update replaced identity or omitted configuration")
	}
	targets, err := w.db.Replications().ForRepo(t.Context(), "local")
	if err != nil || len(targets) != 1 || targets[0].Password != secret {
		t.Fatal("omitted password update lost its stored credential", err)
	}
	w.request("PUT", replicationPath+"local", `{"url":"https://target.example/artifactory/destination"}`, 201)
	replaced := w.configuration("local")
	if replaced["replicationKey"] != key || replaced["syncDeletes"] != false {
		t.Fatal("whole configuration replacement behaved as a patch or changed stable identity")
	}
	targets, err = w.db.Replications().ForRepo(t.Context(), "local")
	if err != nil || len(targets) != 1 || targets[0].Password != secret {
		t.Fatal("omitted replacement credential was erased", err)
	}
	w.request("DELETE", replicationPath+"local", "", 200)
	w.request("GET", replicationPath+"local", "", 404)
	w.request("PUT", replicationPath+"local", create, 201)
	if w.configuration("local")["replicationKey"] == key {
		t.Fatal("recreated configuration reused its old identity")
	}
	w.request("PUT", replicationPath+"remote", `{"enabled":true,"syncProperties":true}`, 201)
	if w.configuration("remote")["repoKey"] != "remote" {
		t.Fatal("remote pull configuration used another repository")
	}
	if strings.Contains(w.logs.String(), secret) {
		t.Fatal("replication request logs exposed the password")
	}
}
