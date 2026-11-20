package federation_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/access/core/token"
	"github.com/weaveplatform/artifactweave/internal/app"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

const replicationPath = "/artifactory/api/replications/"

type replicationWorld struct {
	t    *testing.T
	db   app.RouterStore
	h    http.Handler
	logs bytes.Buffer
}

func newReplicationWorld(t *testing.T) *replicationWorld {
	t.Helper()
	return newReplicationWorldWithStore(t, inmem.New())
}

func newReplicationWorldWithStore(t *testing.T, db app.RouterStore) *replicationWorld {
	t.Helper()
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
	return w.requestAs("admin", method, path, body, want)
}

func (w *replicationWorld) requestAs(actor, method, path, body string, want int) *httptest.ResponseRecorder {
	w.t.Helper()
	headers := make(map[string]string)
	if strings.HasPrefix(actor, "Bearer ") {
		headers["Authorization"] = actor
	} else if actor != "" {
		headers = support.Basic(actor, "pw")
	}
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
	if !strings.HasPrefix(r.Header().Get("Content-Type"), "application/json") {
		w.t.Fatal("configuration read did not return the authored/provider JSON media type")
	}
	var config map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &config); err != nil {
		w.t.Fatal(err)
	}
	return config
}

func TestAccSingleReplicationConfigurationLifecycle(t *testing.T) {
	w := newReplicationWorld(t)
	replicationConfigurationLifecycle(t, w)
	replicationRepositoryLifetime(t, w)
}

func replicationConfigurationLifecycle(t *testing.T, w *replicationWorld) {
	t.Helper()
	headers := support.Basic("admin", "pw")
	headers["Content-Type"] = "application/yaml"
	proxy := support.Do(t, w.h, "PATCH", "/artifactory/api/system/configuration", headers,
		`{"proxies":{"fixture-proxy":{"host":"127.0.0.1","port":3128}}}`)
	if proxy.Code != http.StatusOK {
		t.Fatal("fixture proxy was not configured", proxy.Code, proxy.Body.String())
	}
	const secret = "fake-replication-password"
	const create = `{"url":"https://target.example/artifactory/destination","username":"replicator","password":"` + secret + `","enabled":false,"syncDeletes":true,"proxy":"fixture-proxy","proxyRef":"ignored-input","repoKey":"spoofed","replicationKey":"caller-owned"}`
	w.request("PUT", replicationPath+"local", create, 201)
	first := w.configuration("local")
	key, ok := first["replicationKey"].(string)
	if !ok || key == "" || key == "caller-owned" || first["repoKey"] != "local" {
		t.Fatalf("server identity not assigned: %+v", first)
	}
	if first["enabled"] != false || first["syncProperties"] != true || first["socketTimeoutMillis"] != float64(15000) {
		t.Fatalf("native configuration defaults lost: %+v", first)
	}
	if first["proxy"] != "fixture-proxy" || first["proxyRef"] != "fixture-proxy" {
		t.Fatal("provider proxy refresh field did not preserve the selected proxy")
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
	if replaced["replicationKey"] != key || replaced["syncDeletes"] != false || replaced["proxyRef"] != "" {
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

func replicationRepositoryLifetime(t *testing.T, w *replicationWorld) {
	t.Helper()
	rows, err := w.db.Replications().ForRepo(t.Context(), "local")
	if err != nil || len(rows) != 1 {
		t.Fatal("lifetime fixture is missing its replication", err)
	}
	old := rows[0]
	run := model.ReplicationRun{ReplicationKey: old.Key, Started: time.Now().UTC(), Status: model.ReplicationNeverRun}
	if err := w.db.Replications().PutRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	w.request("DELETE", "/artifactory/api/repositories/local", "", 200)
	rows, err = w.db.Replications().ForRepo(t.Context(), "local")
	if err != nil || len(rows) != 0 {
		t.Fatal("repository deletion retained replication configuration", err)
	}
	if _, err := w.db.Replications().LastRun(t.Context(), old.Key); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("repository deletion retained replication run history", err)
	}
	// Legacy rows can exist without a repository. A fresh repository lifetime
	// must discard those rows before another worker can reuse their authority.
	if err := w.db.Replications().Put(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	w.request("PUT", "/artifactory/api/repositories/local", `{"rclass":"local","packageType":"generic"}`, 200)
	w.request("GET", replicationPath+"local", "", 404)
	w.request("PUT", replicationPath+"local", `{"url":"https://target.example/new-lifetime"}`, 201)
	current, err := w.db.Replications().ForRepo(t.Context(), "local")
	if err != nil || len(current) != 1 || current[0].Key == old.Key || current[0].Revision == old.Revision {
		t.Fatal("fresh repository revived an earlier target or private worker revision", err)
	}
}

func TestAccSingleReplicationSQLConfigurationAndRepositoryLifetime(t *testing.T) {
	keys, err := crypt.New(bytes.Repeat([]byte{11}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sqltest.RunModule(t, appsql.Modules(), func(s *dialect.Store) *appsql.Store { return appsql.New(s.DB(), s.Dialect(), keys) },
		func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
			w := newReplicationWorldWithStore(t, newStore(t))
			replicationConfigurationLifecycle(t, w)
			replicationRepositoryLifetime(t, w)
			replicationLifetimeFailure(t, newStore(t))
			replicationRemoteSourceUpdate(t, newStore(t))
			replicationCurrentPrincipals(t, newStore(t))
		}, sqltest.WithPostgres(), sqltest.WithKeyring(keys))
}

var errLifetimeFailure = errors.New("injected replication lifetime failure")

type lifetimeFailureStore struct {
	app.RouterStore
	fail, failPut bool
}

func (s *lifetimeFailureStore) Replications() port.ReplicationStore {
	return lifetimeFailureReplications{ReplicationStore: s.RouterStore.Replications(), owner: s}
}

type lifetimeFailureReplications struct {
	port.ReplicationStore
	owner *lifetimeFailureStore
}

func (s lifetimeFailureReplications) Put(ctx context.Context, target model.Replication) error {
	if err := s.ReplicationStore.Put(ctx, target); err != nil {
		return err //nolint:wrapcheck // preserve the actual store failure for the HTTP assertion
	}
	if s.owner.failPut {
		return errLifetimeFailure
	}
	return nil
}

func TestAccSingleReplicationRemoteSourceUpdateIsAtomic(t *testing.T) {
	replicationRemoteSourceUpdate(t, inmem.New())
}

func replicationRemoteSourceUpdate(t *testing.T, db app.RouterStore) {
	t.Helper()
	st := &lifetimeFailureStore{RouterStore: db}
	w := newReplicationWorldWithStore(t, st)
	w.request("PUT", replicationPath+"remote", `{"password":"pull-private-password"}`, 201)
	before, err := db.Replications().ForRepo(t.Context(), "remote")
	if err != nil || len(before) != 1 {
		t.Fatal("missing remote source fixture", err)
	}
	const nextURL = "https://replacement.example/artifactory/source"
	w.request("POST", "/artifactory/api/repositories/remote", `{"url":"`+nextURL+`"}`, 200)
	if w.configuration("remote")["url"] != nextURL {
		t.Fatal("pull configuration retained the former canonical upstream")
	}
	after, err := db.Replications().ForRepo(t.Context(), "remote")
	if err != nil || len(after) != 1 || after[0].Key != before[0].Key || after[0].Revision == before[0].Revision || after[0].Password != before[0].Password {
		t.Fatal("source update lost stable identity/secret or retained old worker revision", err)
	}
	worker := replication.Service{Store: st, Settings: st.Settings(), Ownership: st.Repos(), Guard: func(context.Context) error { return nil }}
	called := false
	admit := func(_ context.Context, target model.Replication) error {
		called = true
		if target.URL != nextURL {
			t.Fatal("worker received an obsolete pull source")
		}
		return nil
	}
	if err := worker.WithTarget(t.Context(), before[0].Key, before[0].Revision, admit); !errors.Is(err, kernel.ErrConflict) || called {
		t.Fatal("old pull revision retained worker admission", err)
	}
	if err := worker.WithTarget(t.Context(), after[0].Key, after[0].Revision, admit); err != nil || !called {
		t.Fatal("current pull revision was not admitted", err)
	}
	w.request("POST", "/artifactory/api/repositories/remote", `{"url":"`+nextURL+`"}`, 200)
	unchanged, err := db.Replications().ForRepo(t.Context(), "remote")
	if err != nil || !reflect.DeepEqual(unchanged, after) {
		t.Fatal("identical source retry invalidated current replication", err)
	}
	st.failPut = true
	w.request("POST", "/artifactory/api/repositories/remote", `{"url":"https://failed.example/source"}`, 500)
	unchanged, err = db.Replications().ForRepo(t.Context(), "remote")
	if err != nil || !reflect.DeepEqual(unchanged, after) {
		t.Fatal("failed source refresh changed target configuration or secret", err)
	}
	source, err := db.Repos().Get(t.Context(), "remote")
	if err != nil || source.URL != nextURL {
		t.Fatal("failed target refresh published the canonical source update", err)
	}
}

func (s lifetimeFailureReplications) DeleteForRepo(ctx context.Context, key string) error {
	if err := s.ReplicationStore.DeleteForRepo(ctx, key); err != nil {
		return err //nolint:wrapcheck // preserve the actual store failure for the HTTP assertion
	}
	if s.owner.fail {
		return errLifetimeFailure
	}
	return nil
}

func TestAccSingleReplicationRepositoryLifetimeFailureIsAtomic(t *testing.T) {
	replicationLifetimeFailure(t, inmem.New())
}

func replicationLifetimeFailure(t *testing.T, db app.RouterStore) {
	t.Helper()
	st := &lifetimeFailureStore{RouterStore: db}
	w := newReplicationWorldWithStore(t, st)
	w.request("PUT", replicationPath+"local", `{"url":"https://target.example/fault","password":"lifetime-private-password"}`, 201)
	before, err := db.Replications().ForRepo(t.Context(), "local")
	if err != nil || len(before) != 1 {
		t.Fatal("missing lifetime rollback fixture", err)
	}
	run := model.ReplicationRun{ReplicationKey: before[0].Key, Started: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Status: model.ReplicationOK}
	if err := db.Replications().PutRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	st.fail = true
	w.request("DELETE", "/artifactory/api/repositories/local", "", 500)
	after, err := db.Replications().ForRepo(t.Context(), "local")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("failed repository delete changed replication identity/configuration/secret", err)
	}
	if got, err := db.Replications().LastRun(t.Context(), before[0].Key); err != nil || !reflect.DeepEqual(got, run) {
		t.Fatal("failed repository delete lost its run history", err)
	}
	if _, err := db.Repos().Get(t.Context(), "local"); err != nil {
		t.Fatal("failed repository delete removed the canonical repository", err)
	}
	stale := before[0]
	stale.RepoKey, stale.Key = "fresh", "legacy-orphan-target"
	if err := db.Replications().Put(t.Context(), stale); err != nil {
		t.Fatal(err)
	}
	w.request("PUT", "/artifactory/api/repositories/fresh", `{"rclass":"local","packageType":"generic"}`, 500)
	if _, err := db.Repos().Get(t.Context(), "fresh"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("failed lifecycle reset published a fresh repository", err)
	}
	if got, err := db.Replications().Get(t.Context(), stale.Key); err != nil || !reflect.DeepEqual(got, stale) {
		t.Fatal("failed fresh creation erased legacy configuration", err)
	}
	st.fail = false
	w.request("PUT", "/artifactory/api/repositories/fresh", `{"rclass":"local","packageType":"generic"}`, 200)
	w.request("GET", replicationPath+"fresh", "", 404)
}

func TestAccSingleReplicationValidationControlsAndDeferredOwners(t *testing.T) {
	w := newReplicationWorld(t)
	w.request("PUT", replicationPath+"local", `[{"url":"https://target.example/selected","cronExp":"0 0/5 * * * ?","socketTimeoutMillis":2500}]`, 201)
	w.request("PUT", replicationPath+"remote", `{}`, 201)
	initial, err := w.db.Replications().ForRepo(t.Context(), "local")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, key, body string }{
		{"empty array", "local", `[]`},
		{"multiple targets", "local", `[{"url":"https://one.example"},{"url":"https://two.example"}]`},
		{"null", "local", `null`},
		{"null array element", "local", `[null]`},
		{"virtual repository", "virtual", `{"url":"https://target.example"}`},
		{"invalid URL", "local", `{"url":"relative/path"}`},
		{"embedded credential", "local", `{"url":"https://inline-private-credential@target.example"}`},
		{"URL query", "local", `{"url":"https://target.example?credential=inline-private-credential"}`},
		{"bad cron", "local", `{"url":"https://target.example","cronExp":"not a Quartz expression"}`},
		{"missing proxy", "local", `{"url":"https://target.example","proxy":"missing"}`},
		{"zero timeout", "local", `{"url":"https://target.example","socketTimeoutMillis":0}`},
		{"overflow timeout", "local", `{"url":"https://target.example","socketTimeoutMillis":2147483648}`},
		{"advanced event", "local", `{"url":"https://target.example","enableEventReplication":true}`},
		{"advanced checksum", "local", `{"url":"https://target.example","checkBinaryExistenceInFilestore":true}`},
		{"wrong pull upstream", "remote", `{"url":"https://other.example"}`},
		{"misplaced bulk selection", "local", `{"include":["*"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := w.request("PUT", replicationPath+tc.key, tc.body, 400)
			if strings.Contains(r.Body.String(), "inline-private-credential") {
				t.Fatal("validation response echoed an embedded credential")
			}
		})
	}
	unchanged, err := w.db.Replications().ForRepo(t.Context(), "local")
	if err != nil || !reflect.DeepEqual(initial, unchanged) {
		t.Fatal("invalid configuration changed the stored target or private revision", err)
	}
	w.request("POST", replicationPath+"disable", `{"include":["https://target.example/*"],"exclude":["https://excluded.example/*"]}`, 200)
	if w.configuration("local")["enabled"] != false || w.configuration("remote")["enabled"] != true {
		t.Fatal("bulk URL selection changed an excluded direction")
	}
	w.request("POST", replicationPath+"enable", `{}`, 200)
	if w.configuration("local")["enabled"] != true {
		t.Fatal("bulk enable did not refresh enabled configuration")
	}
	w.request("POST", replicationPath+"disable", `{"enabled":false}`, 400)
	const global = "/artifactory/api/system/replications"
	w.request("POST", global+"/block?pull=false", "", 200)
	var blocks map[string]bool
	if err := json.Unmarshal(w.request("GET", global, "", 200).Body.Bytes(), &blocks); err != nil {
		t.Fatal(err)
	}
	if !blocks["blockPushReplications"] || blocks["blockPullReplications"] {
		t.Fatal("direction selectors changed an unselected global switch")
	}
	w.request("POST", global+"/block?push=false&pull=false", "", 200)
	if w.configuration("local")["enabled"] != true {
		t.Fatal("global pause overwrote the per-target enabled flag")
	}
	w.request("POST", global+"/unblock?pull=false", "", 200)
	w.request("POST", global+"/block?push=invalid", "", 400)
	w.request("PUT", "/artifactory/api/replications/multiple/local", `{}`, 501)
	w.request("GET", "/artifactory/api/replication/local", "", 501)
	w.request("POST", "/artifactory/api/replication/execute/local", `{}`, 501)
	if strings.Contains(w.logs.String(), "inline-private-credential") {
		t.Fatal("validation logs echoed an embedded credential")
	}
}

func TestAccSingleReplicationCurrentPrincipalsAndTokenCeilings(t *testing.T) {
	replicationCurrentPrincipals(t, inmem.New())
}

func replicationCurrentPrincipals(t *testing.T, db app.RouterStore) {
	t.Helper()
	w := newReplicationWorldWithStore(t, db)
	if err := instance.EnsureInstance(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	tokens, err := token.New(t.Context(), db, token.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	auth, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{
		Hash: support.TestHash, Tokens: tokens, Groups: db.Groups(), APIRestrictions: db.APIRestrictions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	w.h = harness.Router(harness.Config{
		Store: db, Auth: auth, Tokens: tokens, AllowBasicAuthTokens: true,
		BaseURL: "https://platform.example", PasswordHash: support.TestHash, Logger: slog.New(slog.NewTextHandler(&w.logs, nil)),
	})
	if err := db.Users().Create(t.Context(), accesstest.User{Name: "owner", PasswordHash: support.CheapHash}); err != nil {
		t.Fatal(err)
	}
	const projects = "/access/api/v1/projects"
	w.request("POST", projects, `{"project_key":"alpha","display_name":"Alpha","admin_privileges":{"manage_resources":true,"manage_remote_repository":false}}`, 201)
	w.request("POST", projects, `{"project_key":"beta","display_name":"Beta"}`, 201)
	w.request("PUT", projects+"/alpha/users/owner", `{"roles":["Project Admin"]}`, 200)
	for _, key := range []string{"alpha-local", "beta-local"} {
		project := strings.Split(key, "-")[0]
		w.request("PUT", "/artifactory/api/repositories/"+key, `{"rclass":"local","packageType":"generic","projectKey":"`+project+`"}`, 200)
	}
	w.request("PUT", "/artifactory/api/repositories/alpha-remote", `{"rclass":"remote","packageType":"generic","projectKey":"alpha","url":"https://upstream.example/source"}`, 200)
	const configuration = `{"url":"https://target.example/owned","password":"principal-private-password"}`
	own, foreign := replicationPath+"alpha-local", replicationPath+"beta-local"
	w.request("PUT", foreign, configuration, 201)
	w.requestAs("owner", "PUT", own, configuration, 201)
	w.requestAs("owner", "GET", own, "", 200)
	for _, action := range []struct{ method, body string }{{"GET", ""}, {"PUT", configuration}, {"POST", `{"enabled":false}`}, {"DELETE", ""}} {
		w.requestAs("owner", action.method, foreign, action.body, 403)
		w.requestAs("", action.method, own, action.body, 401)
	}
	w.requestAs("owner", "PUT", replicationPath+"alpha-remote", `{}`, 403)
	const global = "/artifactory/api/system/replications"
	w.requestAs("owner", "GET", global, "", 403)
	w.requestAs("owner", "POST", global+"/block", "", 403)
	w.requestAs("owner", "POST", replicationPath+"disable", `{}`, 403)
	w.request("PUT", projects+"/alpha", `{"project_key":"alpha","display_name":"Alpha","admin_privileges":{"manage_resources":true,"manage_remote_repository":true}}`, 200)
	w.requestAs("owner", "PUT", replicationPath+"alpha-remote", `{}`, 201)
	mint := func(body string) map[string]string {
		t.Helper()
		r := w.request("POST", "/access/api/v1/tokens", body, 200)
		var issued map[string]any
		if err := json.Unmarshal(r.Body.Bytes(), &issued); err != nil {
			t.Fatal(err)
		}
		out := make(map[string]string)
		for _, key := range []string{"access_token", "reference_token"} {
			if value, ok := issued[key].(string); ok && value != "" {
				out[key] = "Bearer " + value
			}
		}
		return out
	}
	limited := mint(`{"username":"admin","scope":"applied-permissions/user artifact:*:r","expires_in":600}`)
	w.requestAs(limited["access_token"], "GET", own, "", 403)
	w.requestAs(limited["access_token"], "PUT", own, configuration, 403)
	w.requestAs(limited["access_token"], "POST", global+"/unblock", "", 403)
	scoped := mint(`{"username":"owner","scope":"applied-permissions/user","project_key":"alpha","include_reference_token":true,"expires_in":600}`)
	if len(scoped) != 2 {
		t.Fatal("scoped token fixture did not issue both JWT and reference credentials")
	}
	for _, credential := range scoped {
		w.requestAs(credential, "GET", own, "", 200)
		w.requestAs(credential, "GET", foreign, "", 403)
		w.requestAs(credential, "POST", replicationPath+"disable", `{}`, 403)
	}
	w.request("PUT", projects+"/alpha", `{"project_key":"alpha","display_name":"Alpha","admin_privileges":{"manage_resources":false,"manage_remote_repository":false}}`, 200)
	w.requestAs("owner", "GET", own, "", 403)
	for _, credential := range scoped {
		w.requestAs(credential, "GET", own, "", 403)
	}
	w.request("PUT", projects+"/alpha", `{"project_key":"alpha","display_name":"Alpha","admin_privileges":{"manage_resources":true,"manage_remote_repository":true}}`, 200)
	w.request("DELETE", projects+"/alpha/users/owner", "", 204)
	for _, credential := range scoped {
		w.requestAs(credential, "GET", own, "", 403)
	}
	if got := w.configuration("beta-local"); got["url"] != "https://target.example/owned" {
		t.Fatal("denied project administration changed the foreign target")
	}
}
