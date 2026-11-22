package access_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/access/core/token"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func TestAccProjectAdministratorMintsCurrentOwnProjectRoles(t *testing.T) {
	var db *inmem.Store
	var tokens *token.Service
	h := tokenFixture(t, allowBasicTokens, func(c *Config) {
		var ok bool
		db, ok = c.Store.(*inmem.Store)
		if !ok {
			t.Fatal("token fixture lost its in-memory store")
		}
		tokens = c.Tokens
	})
	ctx := t.Context()
	if err := db.Users().Create(ctx, accesstest.User{Name: "owner", PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"alpha", "beta"} {
		projectRequest(t, h, http.MethodPost, "", "admin", `{"project_key":"`+key+`","display_name":"`+key+`"}`, http.StatusCreated)
		wantStatus(
			t,
			do(
				t,
				h,
				http.MethodPut,
				"/artifactory/api/repositories/"+key+"-local",
				jsonAs(basic("admin", "pw")),
				`{"rclass":"local","packageType":"generic","projectKey":"`+key+`"}`,
			),
			http.StatusOK,
		)
	}
	projectRequest(t, h, http.MethodPut, "/alpha/users/owner", "admin", `{"roles":["Project Admin"]}`, http.StatusOK)
	projectRequest(t, h, http.MethodPost, "/alpha/roles", "admin", `{"name":"reader","type":"CUSTOM","actions":["READ_REPOSITORY"],"environments":["DEV"]}`, http.StatusCreated)
	mint := func(headers map[string]string, raw, username string, expires int, want int) map[string]any {
		t.Helper()
		body, err := json.Marshal(map[string]any{"scope": raw, "username": username, "expires_in": expires, "include_reference_token": true})
		if err != nil {
			t.Fatal(err)
		}
		r := do(t, h, http.MethodPost, "/access/api/v1/tokens", jsonAs(headers), string(body))
		wantStatus(t, r, want)
		if want != http.StatusOK {
			return nil
		}
		return decodeObject(t, r.Body.String())
	}
	selection := "applied-permissions/roles:alpha:reader"
	issued := mint(basic("owner", "pw"), selection, "", 600, http.StatusOK)
	access, _ := issued["access_token"].(string)
	reference, _ := issued["reference_token"].(string)
	for _, credential := range []string{access, reference} {
		wantStatus(t, do(t, h, http.MethodGet, "/artifactory/api/repositories/alpha-local", bearerAuth(credential), ""), http.StatusOK)
		wantStatus(t, do(t, h, http.MethodGet, "/artifactory/api/repositories/beta-local", bearerAuth(credential), ""), http.StatusForbidden)
	}
	for _, tc := range []struct {
		raw, user     string
		expires, want int
	}{
		{raw: "applied-permissions/roles:beta:Viewer", expires: 600, want: http.StatusForbidden},
		{raw: "applied-permissions/roles:alpha:missing", expires: 600, want: http.StatusBadRequest},
		{raw: selection + " applied-permissions/user", expires: 600, want: http.StatusForbidden},
		{raw: selection, user: "admin", expires: 600, want: http.StatusForbidden},
		{raw: selection, expires: 0, want: http.StatusForbidden},
	} {
		mint(basic("owner", "pw"), tc.raw, tc.user, tc.expires, tc.want)
	}
	// A transient identity that currently resolves to this stored project
	// administrator retains its explicit selection and parent's expiry ceiling.
	minute := time.Minute
	parent, err := tokens.Issue(ctx, token.Request{Username: "owner", Identity: kernel.IdentityTransient, ExpiresIn: &minute})
	if err != nil {
		t.Fatal(err)
	}
	child := mint(bearerAuth(parent.AccessToken), selection, "", 3600, http.StatusOK)
	childRaw, _ := child["access_token"].(string)
	record, err := tokens.Verify(ctx, childRaw)
	if err != nil || record.Scope.String() != selection || record.ExpiresAt.After(parent.Record.ExpiresAt) {
		t.Fatal("project-role child lost its selection or inherited lifetime ceiling", err)
	}
	// Native content ceilings cannot become a new project-role delegation.
	ceiling := mintAdmin(t, h, `{"scope":"applied-permissions/user artifact:*:r","expires_in":600}`)
	ceilingRaw, _ := ceiling["access_token"].(string)
	mint(bearerAuth(ceilingRaw), selection, "", 600, http.StatusForbidden)
	projectRequest(t, h, http.MethodPut, "/alpha/roles/reader", "admin", `{"name":"reader","type":"CUSTOM","actions":[],"environments":["DEV"]}`, http.StatusOK)
	wantStatus(t, do(t, h, http.MethodGet, "/artifactory/api/repositories/alpha-local", bearerAuth(access), ""), http.StatusForbidden)
	projectRequest(t, h, http.MethodDelete, "/alpha/users/owner", "admin", "", http.StatusNoContent)
	mint(basic("owner", "pw"), selection, "", 600, http.StatusForbidden)
	projectRequest(t, h, http.MethodDelete, "/alpha", "admin", "", http.StatusNoContent)
	// Lifecycle deliberately retains the old build-info repository. Dispose of
	// this empty fixture repository explicitly before recreating its project;
	// PRJ-02 must not silently adopt retained content under a new lifetime.
	projectRequest(t, h, http.MethodPost, "", "admin", `{"project_key":"alpha","display_name":"alpha"}`, http.StatusConflict)
	if err := db.Repos().Delete(ctx, "alpha-build-info"); err != nil {
		t.Fatal(err)
	}
	projectRequest(t, h, http.MethodPost, "", "admin", `{"project_key":"alpha","display_name":"alpha"}`, http.StatusCreated)
	for _, credential := range []string{access, reference} {
		r := do(t, h, http.MethodGet, "/artifactory/api/repositories/alpha-local", bearerAuth(credential), "")
		if r.Code != http.StatusUnauthorized || strings.Contains(r.Body.String(), "incarnation") {
			t.Fatal("recreated project revived old credential or disclosed private lifetime", r.Code)
		}
	}
}

func TestAccProjectContentTokenCeilingsAndRevocation(t *testing.T) {
	projectContentTokenCeilings(t, inmem.New())
}

func TestAccProjectContentTokenSQLCeilingsAndRevocation(t *testing.T) {
	projectRepositorySQL(t, projectContentTokenCeilings)
}

func projectContentTokenCeilings(t *testing.T, db harness.Store) {
	t.Helper()
	ctx := t.Context()
	support.Seed(t, db)
	if err := instance.EnsureInstance(ctx, db); err != nil {
		t.Fatal(err)
	}
	tokens, err := token.New(ctx, db, token.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	auth, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{
		Hash: testHash, Tokens: tokens, Groups: db.Groups(), APIRestrictions: db.APIRestrictions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := New(Config{Store: db, Blobs: blobs, Auth: auth, Tokens: tokens, AllowBasicAuthTokens: true, PasswordHash: testHash})
	for _, name := range []string{"content-owner", "content-consumer"} {
		if err := db.Users().Create(ctx, accesstest.User{Name: name, PasswordHash: cheapHash}); err != nil {
			t.Fatal(err)
		}
	}
	const writer = `{"name":"content","type":"CUSTOM","actions":["READ_REPOSITORY","DEPLOY_CACHE_REPOSITORY"],"environments":["DEV"]}`
	for _, project := range []string{"alpha", "beta"} {
		projectRequest(t, h, http.MethodPost, "", "admin", `{"project_key":"`+project+`","display_name":"`+project+`"}`, http.StatusCreated)
		projectRequest(t, h, http.MethodPost, "/"+project+"/roles", "admin", writer, http.StatusCreated)
		wantStatus(t, do(t, h, http.MethodPut, "/artifactory/api/repositories/"+project+"-local", jsonAs(basic("admin", "pw")),
			`{"rclass":"local","packageType":"generic","projectKey":"`+project+`"}`,
		), http.StatusOK)
	}
	projectRequest(t, h, http.MethodPut, "/alpha/users/content-owner", "admin", `{"roles":["content"]}`, http.StatusOK)
	projectRequest(t, h, http.MethodPut, "/beta/users/content-consumer", "admin", `{"roles":["content"]}`, http.StatusOK)
	content := func(method, path, body string, headers map[string]string, want int) {
		t.Helper()
		response := do(t, h, method, "/artifactory/"+path, headers, body)
		wantStatus(t, response, want)
		if method == http.MethodGet && want == http.StatusOK && response.Body.String() != "current project content" {
			t.Fatal("authorized native read returned different bytes")
		}
	}
	for _, path := range []string{"alpha-local/allowed/file.bin", "alpha-local/private/file.bin", "beta-local/allowed/file.bin"} {
		content(http.MethodPut, path, "current project content", basic("admin", "pw"), http.StatusCreated)
	}
	mint := func(name string) []string {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"username": name, "scope": "applied-permissions/user artifact:alpha-local/allowed/**:r", "expires_in": 600, "include_reference_token": true,
		})
		if err != nil {
			t.Fatal(err)
		}
		issued := mintAdmin(t, h, string(body))
		access, _ := issued["access_token"].(string)
		reference, _ := issued["reference_token"].(string)
		if access == "" || reference == "" {
			t.Fatal("content token fixture did not receive both credential forms")
		}
		return []string{access, reference}
	}
	owner, consumer := mint("content-owner"), mint("content-consumer")
	assertCeilings := func(credentials []string, allowed int) {
		t.Helper()
		for _, credential := range credentials {
			headers := bearerAuth(credential)
			content(http.MethodGet, "alpha-local/allowed/file.bin", "", headers, allowed)
			content(http.MethodGet, "alpha-local/private/file.bin", "", headers, http.StatusForbidden)
			content(http.MethodGet, "beta-local/allowed/file.bin", "", headers, http.StatusForbidden)
			content(http.MethodPut, "alpha-local/allowed/denied.bin", "refused", headers, http.StatusForbidden)
		}
		content(http.MethodGet, "alpha-local/allowed/denied.bin", "", basic("admin", "pw"), http.StatusNotFound)
	}
	// Positive Basic controls prove path/action restrictions come from the
	// issued token ceiling, not a missing underlying project grant.
	content(http.MethodGet, "alpha-local/private/file.bin", "", basic("content-owner", "pw"), http.StatusOK)
	content(http.MethodPut, "alpha-local/allowed/write-control.bin", "current project content", basic("content-owner", "pw"), http.StatusCreated)
	content(http.MethodGet, "beta-local/allowed/file.bin", "", basic("content-consumer", "pw"), http.StatusOK)
	assertCeilings(owner, http.StatusOK)
	assertCeilings(consumer, http.StatusForbidden)
	share := "/_/share/repositories/alpha-local/beta"
	projectRequest(t, h, http.MethodPut, share+"?readOnly=true", "admin", "", http.StatusNoContent)
	content(http.MethodGet, "alpha-local/allowed/file.bin", "", basic("content-consumer", "pw"), http.StatusOK)
	assertCeilings(consumer, http.StatusOK)
	projectRequest(t, h, http.MethodDelete, share, "admin", "", http.StatusNoContent)
	assertCeilings(consumer, http.StatusForbidden)
	content(http.MethodGet, "alpha-local/allowed/file.bin", "", basic("content-consumer", "pw"), http.StatusForbidden)
	projectRequest(t, h, http.MethodPut, "/alpha/roles/content", "admin", `{"name":"content","type":"CUSTOM","actions":[],"environments":["DEV"]}`, http.StatusOK)
	assertCeilings(owner, http.StatusForbidden)
	projectRequest(t, h, http.MethodPut, "/alpha/roles/content", "admin", writer, http.StatusOK)
	assertCeilings(owner, http.StatusOK)
	projectRequest(t, h, http.MethodDelete, "/alpha/users/content-owner", "admin", "", http.StatusNoContent)
	assertCeilings(owner, http.StatusForbidden)
	content(http.MethodGet, "alpha-local/allowed/file.bin", "", basic("content-owner", "pw"), http.StatusForbidden)
}
