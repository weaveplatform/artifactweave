package access_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func TestAccProjectRepositoryBindingsSharesAndRecreation(t *testing.T) {
	projectRepositoryBindings(t, inmem.New())
}

func projectRepositoryHandler(t *testing.T, db harness.Store) http.Handler {
	t.Helper()
	auth, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{Hash: testHash, Groups: db.Groups()})
	if err != nil {
		t.Fatal(err)
	}
	return New(Config{Store: db, Auth: auth, PasswordHash: testHash})
}

func projectRepositoryBindings(t *testing.T, db harness.Store) {
	t.Helper()
	support.Seed(t, db)
	h := projectRepositoryHandler(t, db)
	for _, key := range []string{"alpha", "beta"} {
		projectRequest(t, h, "POST", "", "admin", `{"project_key":"`+key+`","display_name":"`+key+`"}`, 201)
	}
	artifact := func(method, path, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		headers := basic("admin", "pw")
		if body != "" {
			headers["Content-Type"] = "application/json"
		}
		rec := do(t, h, method, "/artifactory/api/repositories"+path, headers, body)
		wantStatus(t, rec, status)
		return rec
	}
	const key = "existing-arbitrary-key"
	artifact("PUT", "/"+key, `{"rclass":"local","packageType":"generic"}`, 200)
	attach := "/_/attach/repositories/" + key
	status := "/_/repositories/" + key
	share := "/_/share/repositories/" + key
	projectRequest(t, h, "PUT", attach+"/alpha", "admin", "", 204)
	configuration := decodeObject(t, artifact("GET", "/"+key, "", 200).Body.String())
	if configuration["projectKey"] != "alpha" || configuration["environments"].([]any)[0] != "DEV" {
		t.Fatalf("attach 204 did not synchronously publish target configuration: %+v", configuration)
	}
	projectRequest(t, h, "PUT", attach+"/beta", "admin", "", 400)
	projectRequest(t, h, "PUT", attach+"/beta?force=true", "admin", "", 204)
	configuration = decodeObject(t, artifact("GET", "/"+key, "", 200).Body.String())
	if configuration["projectKey"] != "beta" {
		t.Fatal("force attach answered 204 with the wrong owner")
	}
	artifact("POST", "/"+key, `{"environments":["missing"]}`, 400)
	configuration = decodeObject(t, artifact("GET", "/"+key, "", 200).Body.String())
	if configuration["projectKey"] != "beta" || configuration["environments"].([]any)[0] != "DEV" {
		t.Fatalf("invalid environment changed canonical binding: %+v", configuration)
	}
	projectRequest(t, h, "PUT", share+"/alpha?readOnly=true", "admin", "", 204)
	projectRequest(t, h, "PUT", share+"?readOnly=true", "admin", "", 204)
	projectRequest(t, h, "DELETE", attach, "admin", "", 204)
	a := decodeObject(t, projectRequest(t, h, "GET", status, "admin", "", 200).Body.String())
	if _, assigned := a["assigned_to"]; assigned || a["shared_read_only"] != true || a["shared_with_all_projects"] != true || len(a["shared_with_projects"].([]any)) != 1 {
		t.Fatalf("detach did not preserve standard shares: %+v", a)
	}
	configuration = decodeObject(t, artifact("GET", "/"+key, "", 200).Body.String())
	if owner, ok := configuration["projectKey"]; ok && owner != "" {
		t.Fatalf("detach left an owner in the repository API: %+v", configuration)
	}
	projectRequest(t, h, "DELETE", share, "admin", "", 204)
	a = decodeObject(t, projectRequest(t, h, "GET", status, "admin", "", 200).Body.String())
	if a["shared_with_all_projects"] != false || len(a["shared_with_projects"].([]any)) != 1 {
		t.Fatal("unshare-all removed the individual target")
	}
	// The repository lifecycle must remove shares, not let a new configuration
	// at the same public key inherit the old consumer grants.
	artifact("DELETE", "/"+key, "", 200)
	artifact("PUT", "/"+key, `{"rclass":"local","packageType":"generic"}`, 200)
	a = decodeObject(t, projectRequest(t, h, "GET", status, "admin", "", 200).Body.String())
	if len(a["shared_with_projects"].([]any)) != 0 || a["shared_with_all_projects"] != false {
		t.Fatalf("recreated repository inherited obsolete sharing: %+v", a)
	}
	projectRequest(t, h, "PUT", attach+"/missing", "admin", "", 404)
	projectRequest(t, h, "GET", "/_/repositories/missing", "admin", "", 404)
	artifact("PUT", "/wrong-prefix", `{"rclass":"local","packageType":"generic","projectKey":"alpha"}`, 400)
	artifact("PUT", "/alpha-new", `{"rclass":"local","packageType":"generic","projectKey":"alpha"}`, 200)
	a = decodeObject(t, projectRequest(t, h, "GET", "/_/repositories/alpha-new", "admin", "", 200).Body.String())
	if a["assigned_to"] != "alpha" {
		t.Fatal("direct project repository creation omitted its Access binding")
	}
	projectRepositoryBatchRollback(t, db, h)
}

type bindingFailureStore struct{ harness.Store }

func (s bindingFailureStore) Projects() accesstest.ProjectStore {
	return bindingFailureProjects{s.Store.Projects()}
}

type bindingFailureProjects struct{ accesstest.ProjectStore }

func (s bindingFailureProjects) PutAssignment(ctx context.Context, a accesstest.RepositoryAssignment) error {
	if err := s.ProjectStore.PutAssignment(ctx, a); err != nil {
		return err
	}
	if a.RepoKey == "alpha-batch-second" {
		return errors.New("injected binding publication failure")
	}
	return nil
}

func projectRepositoryBatchRollback(t *testing.T, db harness.Store, h http.Handler) {
	t.Helper()
	failing := projectRepositoryHandler(t, bindingFailureStore{db})
	const batch = "/artifactory/api/v2/repositories/batch"
	const first = "alpha-batch-first"
	const second = "alpha-batch-second"
	bindings := map[string]accesstest.RepositoryAssignment{}
	authorities := map[string]accesstest.SharingAuthority{}
	body := `[{"key":"` + first + `","rclass":"local","packageType":"generic","projectKey":"alpha"},{"key":"` + second + `","rclass":"local","packageType":"generic","projectKey":"alpha"}]`
	wantStatus(t, do(t, failing, "PUT", batch, jsonAs(basic("admin", "pw")), body), 500)
	for _, key := range []string{first, second} {
		wantStatus(t, do(t, h, "GET", "/artifactory/api/repositories/"+key, basic("admin", "pw"), ""), 404)
		projectRequest(t, h, "GET", "/_/repositories/"+key, "admin", "", 404)
		if _, err := db.Projects().Assignment(t.Context(), key); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("failed batch create left an assignment for %s: %v", key, err)
		}
		wantStatus(t, do(t, h, "PUT", "/artifactory/api/repositories/"+key, jsonAs(basic("admin", "pw")), `{"rclass":"local","packageType":"generic","projectKey":"alpha"}`), 200)
		binding, err := db.Projects().Assignment(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		bindings[key] = binding
		authority, err := db.Projects().SharingAuthority(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		authorities[key] = authority
	}
	update := `[{"key":"` + first + `","projectKey":"beta"},{"key":"` + second + `","projectKey":"beta"}]`
	wantStatus(t, do(t, failing, "POST", batch, jsonAs(basic("admin", "pw")), update), 500)
	for _, key := range []string{first, second} {
		configuration := decodeObject(t, do(t, h, "GET", "/artifactory/api/repositories/"+key, basic("admin", "pw"), "").Body.String())
		assignment := decodeObject(t, projectRequest(t, h, "GET", "/_/repositories/"+key, "admin", "", 200).Body.String())
		if configuration["projectKey"] != "alpha" || assignment["assigned_to"] != "alpha" {
			t.Fatalf("failed batch update split canonical ownership: configuration=%+v assignment=%+v", configuration, assignment)
		}
		stored, err := db.Projects().Assignment(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(stored, bindings[key]) {
			t.Fatalf("failed batch update changed the stored Access binding for %s: before=%+v after=%+v", key, bindings[key], stored)
		}
		authority, err := db.Projects().SharingAuthority(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		if authority != authorities[key] {
			t.Fatalf("failed batch update changed the sharing revision for %s", key)
		}
	}
}

func TestAccProjectRepositoryBindingSQLRoutes(t *testing.T) {
	projectRepositorySQL(t, projectRepositoryBindings)
}

func TestAccProjectRepositorySharesUseCurrentConsumerRole(t *testing.T) {
	var db *inmem.Store
	h := tokenFixture(t, func(c *Config) { db = c.Store.(*inmem.Store) })
	if err := db.Users().Create(t.Context(), accesstest.User{Name: "consumer", PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"alpha", "beta"} {
		projectRequest(t, h, "POST", "", "admin", `{"project_key":"`+key+`","display_name":"`+key+`"}`, 201)
	}
	projectRequest(t, h, "PUT", "/alpha/users/consumer", "admin", `{"roles":["Developer"]}`, 200)
	putRepo := do(t, h, "PUT", "/artifactory/api/repositories/shared-content", jsonAs(basic("admin", "pw")), `{"rclass":"local","packageType":"generic"}`)
	wantStatus(t, putRepo, 200)
	projectRequest(t, h, "PUT", "/_/attach/repositories/shared-content/beta", "admin", "", 204)
	request := func(method, path, actor, body string, want int) {
		t.Helper()
		wantStatus(t, do(t, h, method, "/artifactory/shared-content/"+path, basic(actor, "pw"), body), want)
	}
	request("PUT", "initial.txt", "admin", "initial", 201)
	const share = "/_/share/repositories/shared-content"
	projectRequest(t, h, "PUT", share+"/alpha?readOnly=true", "admin", "", 204)
	request("GET", "initial.txt", "consumer", "", 200)
	request("PUT", "read-only.txt", "consumer", "denied", 403)
	projectRequest(t, h, "PUT", share+"/alpha?readOnly=false", "admin", "", 204)
	request("PUT", "writable.txt", "consumer", "accepted", 201)
	projectRequest(t, h, "DELETE", share+"/alpha", "admin", "", 204)
	request("GET", "initial.txt", "consumer", "", 403)
	projectRequest(t, h, "PUT", share+"?readOnly=true", "admin", "", 204)
	request("GET", "initial.txt", "consumer", "", 200)
	request("PUT", "all-read-only.txt", "consumer", "denied", 403)
	projectRequest(t, h, "DELETE", share, "admin", "", 204)
	request("GET", "initial.txt", "consumer", "", 403)
}
