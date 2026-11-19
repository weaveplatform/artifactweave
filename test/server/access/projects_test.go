package access_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func projectRequest(t *testing.T, h http.Handler, method, path, as, body string, status int) *httptest.ResponseRecorder {
	t.Helper()
	headers := basic(as, "pw")
	if body != "" {
		headers["Content-Type"] = "application/json"
	}
	rec := do(t, h, method, "/access/api/v1/projects"+path, headers, body)
	wantStatus(t, rec, status)
	return rec
}

func TestAccProjectRoleProjectPages(t *testing.T) {
	h, db := seededHandler(t)
	if err := db.Users().Create(t.Context(), accesstest.User{Name: "member", PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"alpha", "beta", "unused"} {
		projectRequest(t, h, "POST", "", "admin", `{"project_key":"`+key+`","display_name":"`+key+`"}`, 201)
	}
	for _, key := range []string{"alpha", "beta"} {
		projectRequest(t, h, "PUT", "/"+key+"/users/member", "admin", `{"roles":["Viewer"]}`, 200)
	}
	headers := basic("admin", "pw")
	const base = "/access/api/v2/projects?role_name=Viewer&limit=1"
	read := func(path string, key string, hasNext bool) string {
		t.Helper()
		rec := do(t, h, "GET", path, headers, "")
		wantStatus(t, rec, 200)
		body := decodeObject(t, rec.Body.String())
		projects, ok := body["projects"].([]any)
		if !ok || len(projects) != 1 || projects[0].(map[string]any)["projectKey"] != key {
			t.Fatalf("project page: %s", rec.Body)
		}
		next, _ := body["cursor"].(string)
		if (next != "") != hasNext {
			t.Fatalf("unexpected cursor: %s", rec.Body)
		}
		return next
	}
	next := read(base, "alpha", true)
	read(base+"&cursor="+url.QueryEscape(next), "beta", false)
	nextDesc := read(base+"&descending_order=true", "beta", true)
	read(base+"&descending_order=true&cursor="+url.QueryEscape(nextDesc), "alpha", false)
	for _, path := range []string{
		base + "&cursor=invalid!",
		base + "&descending_order=true&cursor=" + url.QueryEscape(next),
		"/access/api/v2/projects?role_name=Developer&cursor=" + url.QueryEscape(next),
		"/access/api/v2/projects?role_name=Viewer&limit=0",
	} {
		wantStatus(t, do(t, h, "GET", path, headers, ""), 400)
	}
	wantStatus(t, do(t, h, "GET", "/access/api/v2/projects?role_name=missing", headers, ""), 404)
	wantStatus(t, do(t, h, "GET", base, basic("member", "pw"), ""), 403)
}

func TestAccProjectLifecycleAndEnvironmentBodies(t *testing.T) {
	h, db := seededHandler(t)
	ctx := t.Context()
	projectRequest(t, h, "POST", "", "admin", `{"project_key":"Bad","display_name":"Bad"}`, 400)
	if _, err := db.Repos().Get(ctx, "Bad-build-info"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("invalid project allocated a repository: %v", err)
	}
	rec := projectRequest(t, h, "POST", "", "admin", `{"project_key":"demo","display_name":"Demo"}`, 201)
	created := decodeObject(t, rec.Body.String())
	if created["project_key"] != "demo" || created["storage_quota_bytes"] != float64(-1) {
		t.Fatalf("create body: %s", rec.Body)
	}
	projectRequest(t, h, "POST", "", "admin", `{"project_key":"demo","display_name":"Another"}`, 409)
	build, err := db.Repos().Get(ctx, "demo-build-info")
	if err != nil || build.ProjectKey != "demo" {
		t.Fatalf("server-owned build-info repository: %+v %v", build, err)
	}
	projectRequest(t, h, "GET", "/demo", "admin", "", 200)
	rec = projectRequest(t, h, "PUT", "/demo", "admin", `{"project_key":"renamed","display_name":"Renamed display","description":"updated"}`, 200)
	updated := decodeObject(t, rec.Body.String())
	if updated["project_key"] != "demo" || updated["display_name"] != "Renamed display" || strings.Contains(rec.Body.String(), "created_at") {
		t.Fatalf("update leaked storage state or renamed the key: %s", rec.Body)
	}
	projectRequest(t, h, "GET", "/renamed", "admin", "", 404)
	projectRequest(t, h, "POST", "/demo/environments", "admin", `{"name":"demo-qa"}`, 201)
	projectRequest(t, h, "POST", "/demo/environments", "admin", `{"name":"demo-QA"}`, 409)
	projectRequest(t, h, "POST", "/demo/environments/demo-qa/rename", "admin", `{"new_name":"demo-stage"}`, 200)
	rec = projectRequest(t, h, "GET", "/demo/environments", "admin", "", 200)
	if !strings.Contains(rec.Body.String(), `"name":"demo-stage"`) || strings.Contains(rec.Body.String(), `"DEV"`) {
		t.Fatalf("project environments: %s", rec.Body)
	}
	projectRequest(t, h, "DELETE", "/demo/environments/demo-stage", "admin", "", 204)
	projectRequest(t, h, "DELETE", "/demo/environments/DEV", "admin", "", 404)
	projectRequest(t, h, "DELETE", "/demo", "admin", "", 204)
	projectRequest(t, h, "GET", "/demo", "admin", "", 404)
	build, err = db.Repos().Get(ctx, "demo-build-info")
	if err != nil || build.ProjectKey != "" || len(build.Environments) != 0 {
		t.Fatalf("project deletion did not retain and unassign build-info: %+v %v", build, err)
	}
}

func TestAccProjectRoleAndMemberBodies(t *testing.T) {
	h, db := seededHandler(t)
	ctx := t.Context()
	if err := db.Users().Create(ctx, accesstest.User{Name: "member", PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := db.Groups().Create(ctx, accesstest.Group{Name: "team"}); err != nil {
		t.Fatal(err)
	}
	projectRequest(t, h, "POST", "", "admin", `{"project_key":"demo","display_name":"Demo"}`, 201)
	rec := projectRequest(t, h, "GET", "/demo/roles/Project%20Admin", "admin", "", 200)
	if decodeObject(t, rec.Body.String())["type"] != "PREDEFINED" {
		t.Fatalf("project admin storage type escaped onto the wire: %s", rec.Body)
	}
	projectRequest(t, h, "GET", "/demo/roles", "admin", "", 200)
	role := `{"name":"ci","type":"CUSTOM","actions":["READ_REPOSITORY","READ_REPOSITORY"],"environments":["dev"]}`
	rec = projectRequest(t, h, "POST", "/demo/roles", "admin", role, 201)
	if strings.Contains(rec.Body.String(), "project_key") || !strings.Contains(rec.Body.String(), `"environments":["DEV"]`) {
		t.Fatalf("role was not canonicalized/projected: %s", rec.Body)
	}
	projectRequest(t, h, "POST", "/demo/roles", "admin", role, 400)
	projectRequest(t, h, "PUT", "/demo/roles/ci", "admin", `{"type":"CUSTOM","description":"updated","actions":["READ_BUILD"],"environments":["DEV"]}`, 200)
	projectRequest(t, h, "PUT", "/demo/roles/Project%20Admin", "admin", `{"type":"CUSTOM"}`, 400)
	projectRequest(t, h, "DELETE", "/demo/roles/Developer", "admin", "", 400)
	projectRequest(t, h, "PUT", "/demo/users/MEMBER", "admin", `{"roles":["ci"]}`, 200)
	projectRequest(t, h, "PUT", "/demo/groups/team", "admin", `{"roles":["ci"]}`, 200)
	for _, path := range []string{"/demo/users/member", "/demo/groups/team", "/demo/roles/ci"} {
		rec = projectRequest(t, h, "GET", path, "admin", "", 200)
		for _, private := range []string{"project_key", "created_at", "revision", "principal_type"} {
			if strings.Contains(rec.Body.String(), private) {
				t.Fatalf("%s leaked %s: %s", path, private, rec.Body)
			}
		}
	}
	for _, path := range []string{"/demo/users", "/demo/groups"} {
		rec = projectRequest(t, h, "GET", path, "admin", "", 200)
		if _, ok := decodeObject(t, rec.Body.String())["members"].([]any); !ok {
			t.Fatalf("member list lost its envelope: %s", rec.Body)
		}
	}
	projectRequest(t, h, "PUT", "/demo/users/member", "admin", `{"roles":[]}`, 400)
	projectRequest(t, h, "PUT", "/demo/users/missing", "admin", `{"roles":["ci"]}`, 404)
	projectRequest(t, h, "DELETE", "/demo/roles/ci", "admin", "", 204)
	projectRequest(t, h, "GET", "/demo/users/member", "admin", "", 404)
	projectRequest(t, h, "GET", "/demo/groups/team", "admin", "", 404)
	projectRequest(t, h, "POST", "/demo/roles", "admin", role, 201)
	projectRequest(t, h, "GET", "/demo/users/member", "admin", "", 404)
	projectRequest(t, h, "PUT", "/demo/users/member", "admin", `{"roles":["ci"]}`, 200)
	projectRequest(t, h, "PUT", "/demo/groups/team", "admin", `{"roles":["ci"]}`, 200)
	projectRequest(t, h, "DELETE", "/demo/users/member", "admin", "", 204)
	projectRequest(t, h, "DELETE", "/demo/groups/team", "admin", "", 204)
}

func TestAccProjectDeletionRetainsDownloadableContents(t *testing.T) {
	h := statusFixture(t, false)
	projectRequest(t, h, "POST", "", "admin", `{"project_key":"retain","display_name":"Retain"}`, 201)
	headers := jsonHeaders(basic("admin", "pw"))
	rec := do(t, h, "PUT", "/artifactory/api/repositories/retain-local", headers,
		`{"rclass":"local","packageType":"generic","projectKey":"retain"}`)
	wantStatus(t, rec, 200)
	const contents = "retained project content"
	rec = do(t, h, "PUT", "/artifactory/retain-local/kept.txt", basic("admin", "pw"), contents)
	wantStatus(t, rec, 201)
	projectRequest(t, h, "DELETE", "/retain", "admin", "", 204)
	rec = do(t, h, "GET", "/artifactory/retain-local/kept.txt", basic("admin", "pw"), "")
	wantStatus(t, rec, 200)
	if rec.Body.String() != contents {
		t.Fatalf("project deletion changed repository bytes: %q", rec.Body.String())
	}
	rec = do(t, h, "GET", "/artifactory/api/repositories/retain-local", basic("admin", "pw"), "")
	wantStatus(t, rec, 200)
	if key, _ := decodeObject(t, rec.Body.String())["projectKey"].(string); key != "" {
		t.Fatalf("retained repository is still assigned: %s", rec.Body)
	}
}
