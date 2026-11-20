package access_test

import (
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
)

func TestAccProjectRepositoryBindingsSharesAndRecreation(t *testing.T) {
	h, _ := seededHandler(t)
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
