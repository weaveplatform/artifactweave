package access_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
)

func TestAccProjectAdministrationPrincipalMatrix(t *testing.T) {
	h, db := seededHandler(t)
	ctx := t.Context()
	if err := db.Groups().Create(ctx, accesstest.Group{Name: "project-admins"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"own", "foreign", "member", "delegated"} {
		groups := []string(nil)
		if name == "delegated" {
			groups = []string{"project-admins"}
		}
		if err := db.Users().Create(ctx, accesstest.User{Name: name, PasswordHash: cheapHash, Groups: groups}); err != nil {
			t.Fatal(err)
		}
	}
	projectRequest(t, h, "POST", "", "admin", `{"project_key":"alpha","display_name":"Alpha"}`, 201)
	projectRequest(t, h, "POST", "", "admin", `{"project_key":"beta","display_name":"Beta"}`, 201)
	projectRequest(t, h, "PUT", "/alpha/users/own", "admin", `{"roles":["Project Admin"]}`, 200)
	projectRequest(t, h, "PUT", "/beta/users/foreign", "admin", `{"roles":["Project Admin"]}`, 200)
	projectRequest(t, h, "PUT", "/alpha/users/member", "admin", `{"roles":["Viewer"]}`, 200)
	projectRequest(t, h, "PUT", "/alpha/groups/project-admins", "admin", `{"roles":["Project Admin"]}`, 200)
	for _, tc := range []struct {
		name, path, user string
		status           int
	}{
		{"platform admin", "/alpha", "admin", 200},
		{"own admin", "/alpha", "own", 200},
		{"group admin", "/alpha", "delegated", 200},
		{"foreign admin", "/alpha", "foreign", 403},
		{"ordinary member", "/alpha", "member", 403},
		{"own roles", "/alpha/roles", "own", 200},
		{"foreign roles", "/alpha/roles", "foreign", 403},
		{"own members", "/alpha/users", "own", 200},
		{"foreign members", "/alpha/groups", "foreign", 403},
		{"member cannot read own assignment", "/alpha/users/member", "member", 403},
		{"own environments", "/alpha/environments", "own", 200},
		{"foreign environments", "/alpha/environments", "foreign", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := projectRequest(t, h, "GET", tc.path, tc.user, "", tc.status)
			if tc.status == 403 {
				var body struct {
					Errors []struct{ Code string }
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Errors) != 1 || body.Errors[0].Code != "FORBIDDEN" {
					t.Fatalf("Projects error lost its string code: %s", rec.Body)
				}
			}
		})
	}
	for _, headers := range []map[string]string{nil, {"Authorization": "Bearer invalid"}} {
		rec := do(t, h, "GET", "/access/api/v1/projects/alpha", headers, "")
		wantStatus(t, rec, http.StatusUnauthorized)
	}
	projectRequest(t, h, "POST", "", "own", `{"project_key":"forbidden","display_name":"Forbidden"}`, 403)
	projectRequest(t, h, "PUT", "/alpha", "own", `{"display_name":"Forbidden"}`, 403)
	projectRequest(t, h, "DELETE", "/alpha", "own", "", 403)
	projectRequest(t, h, "PUT", "/alpha", "admin", `{"admin_privileges":{"manage_members":false}}`, 200)
	projectRequest(t, h, "GET", "/alpha", "own", "", 200)
	projectRequest(t, h, "GET", "/alpha/roles", "own", "", 200)
	projectRequest(t, h, "GET", "/alpha/users", "own", "", 403)
	projectRequest(t, h, "GET", "/alpha/groups", "delegated", "", 403)
	projectRequest(t, h, "POST", "/alpha/roles", "own", `{"name":"blocked","type":"CUSTOM"}`, 403)
	projectRequest(t, h, "POST", "/alpha/environments", "own", `{"name":"alpha-qa"}`, 201)
	projectRequest(t, h, "DELETE", "/alpha/users/own", "admin", "", 204)
	projectRequest(t, h, "GET", "/alpha", "own", "", 403)
	projectRequest(t, h, "PUT", "/alpha/users/own", "admin", `{"roles":["Viewer"]}`, 200)
	projectRequest(t, h, "GET", "/alpha", "own", "", 403)
}

func TestAccProjectManagementBearerScopeCeilings(t *testing.T) {
	h := tokenFixture(t, allowBasicTokens)
	projectRequest(t, h, "POST", "", "admin", `{"project_key":"alpha","display_name":"Alpha"}`, 201)
	projectRequest(t, h, "POST", "", "admin", `{"project_key":"beta","display_name":"Beta"}`, 201)
	projectRequest(t, h, "PUT", "/alpha/users/user", "admin", `{"roles":["Project Admin"]}`, 200)
	for _, tc := range []struct {
		scope                         string
		roles, users, groups, project int
	}{
		{"project:alpha/roles:r", 200, 403, 403, 403},
		{"project:beta/roles:r", 403, 403, 403, 403},
		{"project:alpha/members/**:r", 403, 200, 200, 403},
		{"artifact:*:r", 403, 403, 403, 403},
		{"applied-permissions/user", 200, 200, 200, 200},
	} {
		t.Run(tc.scope, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"username": "user", "scope": tc.scope, "expires_in": 86400, "force_revocable": true})
			if err != nil {
				t.Fatal(err)
			}
			issued := mintAdmin(t, h, string(body))
			value, _ := issued["access_token"].(string)
			headers := bearerAuth(value)
			for _, read := range []struct {
				path   string
				status int
			}{
				{"/roles", tc.roles}, {"/users", tc.users}, {"/groups", tc.groups}, {"", tc.project},
			} {
				wantStatus(t, do(t, h, "GET", "/access/api/v1/projects/alpha"+read.path, headers, ""), read.status)
			}
			if tc.scope != "applied-permissions/user" {
				wantStatus(t, do(t, h, "POST", "/access/api/v1/projects/alpha/roles", jsonAs(headers), `{"name":"denied","type":"CUSTOM"}`), 403)
			}
			id, _ := issued["token_id"].(string)
			wantStatus(t, do(t, h, "DELETE", "/access/api/v1/tokens/"+id, basic("admin", "pw"), ""), 200)
			wantStatus(t, do(t, h, "GET", "/access/api/v1/projects/alpha/roles", headers, ""), 401)
		})
	}
}
