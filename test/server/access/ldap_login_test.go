package access_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/pkg/containertest"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

const ldapTestPath = "/access/api/v1/ldap/settings/test"

// ldapWorldConfig is the runtime configuration of one backend: SQLite in
// the test's directory, or a scratch PostgreSQL database, reused when the
// runtime restarts.
func ldapWorldConfig(t *testing.T, backend string) config.Config {
	t.Helper()
	cfg := harness.AppConfig(t)
	cfg.Bootstrap.AdminPassword = "pw"
	if backend == "postgres" {
		cfg.Database = config.Database{Driver: "postgres", DSN: sqltest.ScratchDSN(t, sqltest.PostgresDSN(t))}
	}
	return cfg
}

// ldapRouter is the router over rt, with the runtime's directory client and
// token.allow_basic_auth on, so a directory login can create a token.
func ldapRouter(rt *harness.Runtime) http.Handler {
	return harness.Router(harness.Config{
		Store: rt.Store, Auth: rt.Auth, Tokens: rt.Tokens, Blobs: rt.Blobs, Keys: rt.Keys, LDAP: rt.LDAP,
		AllowBasicAuthTokens: true, Logger: slog.New(slog.DiscardHandler),
	})
}

// ldapBody is a connection in the Access API's JSON: a DN pattern and a
// search through both bases as the directory's reader, over LDAPS.
func ldapBody(ep containertest.LDAPEndpoint, key, url string, extra map[string]any) string {
	b := map[string]any{
		"key": key, "ldap_url": url, "user_dn_pattern": "uid={0},ou=people",
		"auto_create_user": true, "allow_user_to_access_profile": false, "email_attribute": "mail",
		"search": map[string]any{
			"search_filter": "(uid={0})", "search_base": "ou=people|ou=contractors", "search_sub_tree": true,
			"manager_dn": ep.ReaderDN, "manager_password": ep.ReaderPassword,
		},
	}
	for k, v := range extra {
		b[k] = v
	}
	out, _ := json.Marshal(b)
	return string(out)
}

func ldapCall(t *testing.T, h http.Handler, method, path string, creds map[string]string, body string, want int) string {
	t.Helper()
	headers := map[string]string{}
	for k, v := range creds {
		headers[k] = v
	}
	if body != "" && headers["Content-Type"] == "" {
		headers["Content-Type"] = "application/json"
	}
	rec := do(t, h, method, path, headers, body)
	if rec.Code != want {
		t.Fatalf("%s %s: %d, want %d: %s", method, path, rec.Code, want, rec.Body)
	}
	return rec.Body.String()
}

// seedLDAPAccess gives the auto-join group directory-users read and write
// on libs-local, the access a directory user's first login brings.
func seedLDAPAccess(t *testing.T, rt *harness.Runtime) {
	t.Helper()
	ctx := t.Context()
	if err := rt.Store.Groups().Create(ctx, accesstest.Group{Name: "directory-users", AutoJoin: true}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Store.Repos().Create(ctx, *repo.Defaults(&repo.Config{Key: "libs-local", Class: repo.Local})); err != nil {
		t.Fatal(err)
	}
	if err := rt.Store.Permissions().Create(ctx, accesstest.Permission{
		Name: "directory-libs",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Groups: map[string]api.Actions{
				"directory-users": api.ActionRead | api.ActionWrite,
			}},
			Targets: map[string]accesstest.Target{"libs-local": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
}

// TestAccLDAPLogin drives LDAP-02 (#1833) end to end against a real
// OpenLDAP directory over LDAPS, on SQLite and PostgreSQL: the connection
// test's answers, logins through the DN pattern and through the search,
// refusals, the local identity a login creates, a token and artifact access
// after it, disabled and changed connections, the local administrator while
// the directory is unreachable, and a restart. Logins racing a connection
// change are internal/access/adapters/ldap's publication_test.go.
func TestAccLDAPLogin(t *testing.T) {
	ep := containertest.OpenLDAP(t)
	accesstest.SeedDirectory(t, ep)
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) { ldapLogin(t, ep, backend) })
	}
}

func ldapLogin(t *testing.T, ep containertest.LDAPEndpoint, backend string) {
	cfg := ldapWorldConfig(t, backend)
	opts := harness.AppOptions()
	opts.LDAPRootCAs = ep.CA
	rt := harness.OpenApp(t, cfg, opts)
	open := true
	t.Cleanup(func() {
		if open {
			_ = rt.Close(context.Background())
		}
	})
	seedLDAPAccess(t, rt)
	h := ldapRouter(rt)
	admin := basic("admin", "pw")
	settings := "/access/api/v1/ldap/settings"
	url := ep.TLSURL(ep.BaseDN)

	// The connection test asks the directory and stores nothing.
	ldapCall(t, h, http.MethodPost, ldapTestPath, admin, ldapBody(ep, "corp", url, nil), http.StatusNoContent)
	wrong := strings.Replace(ldapBody(ep, "corp", url, nil), ep.ReaderPassword, "wrong", 1)
	if body := ldapCall(t, h, http.MethodPost, ldapTestPath, admin, wrong, http.StatusBadRequest); !strings.Contains(
		body, "manager bind: the directory refused search.manager_dn or its password") {
		t.Fatalf("wrong manager password: %s", body)
	}
	plain := strings.Replace(url, ep.TLSURL(""), "ldaps://"+ep.Host+":"+portOf(ep.URL("")), 1)
	if body := ldapCall(t, h, http.MethodPost, ldapTestPath, admin, ldapBody(ep, "corp", plain, nil),
		http.StatusBadRequest); !strings.Contains(body, "LDAP connection test failed: tls: ") {
		t.Fatalf("LDAPS to the plain port: %s", body)
	}
	missing := ldapBody(ep, "corp", url, nil)
	missing = strings.Replace(missing, "ou=people|ou=contractors", "ou=missing", 1)
	if body := ldapCall(t, h, http.MethodPost, ldapTestPath, admin, missing, http.StatusBadRequest); !strings.Contains(
		body, "does not exist") {
		t.Fatalf("missing search base: %s", body)
	}
	if body := ldapCall(t, h, http.MethodGet, settings, admin, "", http.StatusOK); strings.TrimSpace(body) != "[]" {
		t.Fatalf("a test stored a connection: %s", body)
	}

	// 7.x turns internal password login off when the first connection is
	// enabled, unless it was ever set; setting it on first keeps the local
	// administrator's password, the fallback this test exercises
	// (TestAccInternalPasswordLogin covers the switch).
	ldapCall(t, h, http.MethodPut, internalSwitch, admin, `{"enabled":true}`, http.StatusOK)
	// Before any connection, a directory password is refused.
	ldapCall(t, h, http.MethodGet, "/artifactory/libs-local/", basic("alice", accesstest.Alice.Password), "",
		http.StatusUnauthorized)
	ldapCall(t, h, http.MethodPost, settings, admin, ldapBody(ep, "corp", url, nil), http.StatusCreated)
	// A stored connection's test takes its sealed password when the body
	// leaves it out.
	stored := strings.Replace(ldapBody(ep, "corp", url, nil), `"manager_password":"`+ep.ReaderPassword+`",`, "", 1)
	ldapCall(t, h, http.MethodPost, ldapTestPath, admin, stored, http.StatusNoContent)

	// Alice logs in through the DN pattern, Bob and Dave through the
	// search, each in the auto-join group's access.
	alice := basic("alice", accesstest.Alice.Password)
	ldapCall(t, h, http.MethodPut, "/artifactory/libs-local/alice.txt", alice, "from alice", http.StatusCreated)
	ldapCall(t, h, http.MethodGet, "/artifactory/libs-local/alice.txt", basic("BOB", accesstest.Bob.Password), "",
		http.StatusOK)
	ldapCall(t, h, http.MethodGet, "/artifactory/libs-local/alice.txt", basic("dave", accesstest.Dave.Password), "",
		http.StatusOK)
	for _, refused := range []map[string]string{
		// Each refusal suspends its name for a moment (login suspension), so
		// these spend names nothing below logs in with.
		basic("dave", "wrong"), basic("nobody", "pw"), basic("carol", "any"), basic("frank", ""),
	} {
		ldapCall(t, h, http.MethodGet, "/artifactory/libs-local/alice.txt", refused, "", http.StatusUnauthorized)
	}

	// The first login created each user in the ldap realm, with the
	// directory's email and the connection's profile setting.
	for name, email := range map[string]string{"alice": accesstest.Alice.Email, "dave": ""} {
		var u struct {
			Realm            string `json:"realm"`
			Email            string `json:"email"`
			ProfileUpdatable bool   `json:"profile_updatable"`
			Groups           []string
		}
		if err := json.Unmarshal([]byte(ldapCall(t, h, http.MethodGet, "/access/api/v2/users/"+name, admin, "",
			http.StatusOK)), &u); err != nil {
			t.Fatal(err)
		}
		if u.Realm != "ldap" || u.Email != email || u.ProfileUpdatable {
			t.Fatalf("%s: %+v", name, u)
		}
	}

	// A token the directory login creates reaches the artifact.
	var issued struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(ldapCall(t, h, http.MethodPost, "/access/api/v1/tokens", alice, `{}`,
		http.StatusOK)), &issued); err != nil || issued.AccessToken == "" {
		t.Fatalf("token: %v %+v", err, issued)
	}
	ldapCall(t, h, http.MethodGet, "/artifactory/libs-local/alice.txt",
		map[string]string{"Authorization": "Bearer " + issued.AccessToken}, "", http.StatusOK)

	// Disabled: no login, remembered or not. The local administrator stays.
	ldapCall(t, h, http.MethodPut, settings, admin, ldapBody(ep, "corp", url, map[string]any{"enabled": false}),
		http.StatusOK)
	ldapCall(t, h, http.MethodGet, "/artifactory/libs-local/alice.txt", alice, "", http.StatusUnauthorized)
	ldapCall(t, h, http.MethodGet, settings, admin, "", http.StatusOK)

	// Without auto-create, a new user acts in the auto-join groups without
	// being stored; profile access is the connection's.
	ldapCall(t, h, http.MethodPut, settings, admin, ldapBody(ep, "corp", url, map[string]any{
		"auto_create_user": false, "allow_user_to_access_profile": true,
	}), http.StatusOK)
	erin := basic(accesstest.Erin.UID, accesstest.Erin.Password)
	ldapCall(t, h, http.MethodGet, "/artifactory/libs-local/alice.txt", erin, "", http.StatusOK)
	erinPath := "/access/api/v2/users/" + strings.ReplaceAll(accesstest.Erin.UID, ",", "%2C")
	ldapCall(t, h, http.MethodGet, erinPath, admin, "", http.StatusNotFound)
	ldapCall(t, h, http.MethodPut, settings, admin, ldapBody(ep, "corp", url, map[string]any{
		"allow_user_to_access_profile": true,
	}), http.StatusOK)
	// Auto-create again: Erin's next login stores her, with profile access.
	ldapCall(t, h, http.MethodGet, "/artifactory/libs-local/alice.txt", erin, "", http.StatusOK)
	if body := ldapCall(t, h, http.MethodGet, erinPath, admin, "", http.StatusOK); !strings.Contains(
		body, `"profile_updatable":true`) || !strings.Contains(body, `"realm":"ldap"`) {
		t.Fatalf("erin: %s", body)
	}

	// An unreachable directory refuses its users and leaves the local
	// administrator's login alone.
	ldapCall(t, h, http.MethodPut, settings, admin, ldapBody(ep, "corp", "ldap://127.0.0.1:1/"+ep.BaseDN, nil),
		http.StatusOK)
	ldapCall(t, h, http.MethodGet, "/artifactory/libs-local/alice.txt", alice, "", http.StatusUnauthorized)
	ldapCall(t, h, http.MethodGet, settings, admin, "", http.StatusOK)

	// A restart keeps the connection and its sealed manager password, and
	// nothing the last process remembered.
	ldapCall(t, h, http.MethodPut, settings, admin,
		strings.Replace(ldapBody(ep, "corp", url, nil), `"manager_password":"`+ep.ReaderPassword+`",`, "", 1),
		http.StatusOK)
	if err := rt.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	open = false
	rt = harness.OpenApp(t, cfg, opts)
	open = true
	h = ldapRouter(rt)
	ldapCall(t, h, http.MethodGet, "/artifactory/libs-local/alice.txt", basic("bob", accesstest.Bob.Password), "",
		http.StatusOK)
}

// portOf is the port of an ldap:// URL without a base DN.
func portOf(u string) string { return u[strings.LastIndex(u, ":")+1:] }
