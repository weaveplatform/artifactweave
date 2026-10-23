package npm_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// npmAuthWorld is the server over a seeded store with npm-local and
// libs-local, a token service, and its access log in log. admin's password
// is "pw"; anonymous access is off.
type npmAuthWorld struct {
	h      http.Handler
	db     *inmem.Store
	tokens *token.Service
	log    *bytes.Buffer
}

func newNPMAuthWorld(t *testing.T) *npmAuthWorld {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	cheapSeed(t, db)
	for _, c := range []repo.Config{
		{Key: "npm-local", Class: repo.Local, PackageType: repo.NPM},
		{Key: "libs-local", Class: repo.Local},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Instance().Init(ctx, types.InstanceIdentity{
		InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	tokens, err := token.New(ctx, db, token.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{
		Hash: testHash, Tokens: tokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	return &npmAuthWorld{
		h: server.New(server.Config{
			BaseURL: "https://reg.example", CompatVersion: "7.155.0", Store: db, Auth: a,
			Tokens: tokens, Logger: slog.New(slog.NewJSONHandler(&log, nil)),
		}),
		db: db, tokens: tokens, log: &log,
	}
}

// decoded is the user:password a _auth line carries.
func decoded(t *testing.T, line string) string {
	t.Helper()
	v, ok := strings.CutPrefix(strings.TrimSpace(line), "_auth = ")
	if !ok {
		t.Fatalf("not an _auth line: %q", line)
	}
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// GET /api/npm/auth writes back no password: a caller that sent its
// password gets _auth for user:<encrypted password>, the value
// GET /api/security/encryptedPassword gives, as 7.x's documentation shows
// it; a caller that sent a token gets that same token, as _auth beside the
// user name it sent with Basic, or as _authToken. Neither always-auth nor
// email is sent (7.161.26).
func TestNPMAuthLines(t *testing.T) {
	w := newNPMAuthWorld(t)
	ctx := context.Background()
	rec := do(t, w.h, "GET", "/artifactory/api/npm/auth", basic("admin", "pw"), "")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/plain" {
		t.Fatalf("%d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	encrypted, err := w.tokens.EncryptedPassword(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if got := decoded(t, rec.Body.String()); got != "admin:"+encrypted {
		t.Errorf("_auth decodes to %q, want admin:<encrypted password>", got)
	}
	if strings.Count(rec.Body.String(), "\n") != 1 ||
		strings.Contains(rec.Body.String(), "always-auth") {
		t.Errorf("extra lines: %q", rec.Body)
	}
	// The encrypted password works as the password, as the line needs.
	got := do(
		t,
		w.h,
		"GET",
		"/artifactory/api/npm/npm-local/-/whoami",
		basic("admin", encrypted),
		"",
	)
	if got.Code != http.StatusOK {
		t.Errorf("encrypted password refused: %d %s", got.Code, got.Body)
	}

	issued, err := w.tokens.Issue(ctx, token.Request{Username: "admin", IncludeReference: true})
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		headers map[string]string
		want    string
	}{
		"bearer":     {bearerAuth(issued.AccessToken), "_authToken = " + issued.AccessToken + "\n"},
		"bearer ref": {bearerAuth(issued.ReferenceToken), "_authToken = " + issued.ReferenceToken + "\n"},
		"api header": {
			map[string]string{"X-JFrog-Art-Api": issued.ReferenceToken},
			"_authToken = " + issued.ReferenceToken + "\n",
		},
	} {
		rec := do(t, w.h, "GET", "/artifactory/api/npm/auth", c.headers, "")
		if rec.Code != http.StatusOK || rec.Body.String() != c.want {
			t.Errorf("%s: %d %q", name, rec.Code, rec.Body)
		}
	}
	rec = do(t, w.h, "GET", "/artifactory/api/npm/auth", basic("admin", issued.AccessToken), "")
	if rec.Code != http.StatusOK || decoded(t, rec.Body.String()) != "admin:"+issued.AccessToken {
		t.Errorf("basic with a token: %d %q", rec.Code, rec.Body)
	}
}

// The scoped form maps the scope to the repository's registry under the
// base URL, then keys the credential to it: _password, the base64 of the
// encrypted password, and username, for a Basic caller.
func TestNPMScopeAuthLines(t *testing.T) {
	w := newNPMAuthWorld(t)
	encrypted, err := w.tokens.EncryptedPassword(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	const reg = "//reg.example/artifactory/api/npm/npm-local/"
	want := "@acme:registry=https:" + reg + "\n" +
		reg + ":_password=" + base64.StdEncoding.EncodeToString([]byte(encrypted)) + "\n" +
		reg + ":username=admin\n"
	// The scope comes without its @, and one sent with it is taken as meant.
	for _, scope := range []string{"acme", "@acme"} {
		rec := do(
			t,
			w.h,
			"GET",
			"/artifactory/api/npm/npm-local/auth/"+scope,
			basic("admin", "pw"),
			"",
		)
		if rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("%s: %d\n%s\nwant\n%s", scope, rec.Code, rec.Body, want)
		}
	}
}

// Both operations answer errors in the Artifactory envelope, as 7.x does
// (RTFACT-30181), where the registry beside them answers npm's.
func TestNPMAuthEnvelope(t *testing.T) {
	w := newNPMAuthWorld(t)
	for _, c := range []struct {
		method, path string
		headers      map[string]string
		want         int
	}{
		{"GET", "/artifactory/api/npm/auth", nil, http.StatusUnauthorized},
		{"GET", "/artifactory/api/npm/auth", basic("admin", "wrong"), http.StatusUnauthorized},
		{"PUT", "/artifactory/api/npm/auth", basic("admin", "pw"), http.StatusMethodNotAllowed},
		{"GET", "/artifactory/api/npm/npm-local/auth/acme", nil, http.StatusUnauthorized},
		{"GET", "/artifactory/api/npm/libs-local/auth/acme", basic("admin", "pw"), http.StatusBadRequest},
		{"GET", "/artifactory/api/npm/missing/auth/acme", basic("admin", "pw"), http.StatusNotFound},
		{"GET", "/artifactory/api/npm/npm-local/auth/a%0Ab", basic("admin", "pw"), http.StatusBadRequest},
		{"DELETE", "/artifactory/api/npm/npm-local/auth/acme", basic("admin", "pw"), http.StatusMethodNotAllowed},
	} {
		rec := do(t, w.h, c.method, c.path, c.headers, "")
		if rec.Code != c.want || !strings.HasPrefix(rec.Body.String(), `{"errors":[{`) {
			t.Errorf("%s %s: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
	// The registry keeps npm's.
	rec := do(t, w.h, "GET", "/artifactory/api/npm/npm-local/-/whoami", nil, "")
	if rec.Code != http.StatusUnauthorized || !strings.HasPrefix(rec.Body.String(), `{"error":`) {
		t.Errorf("registry: %d %s", rec.Code, rec.Body)
	}
}

// npm's login and logout need no credentials while anonymous access is
// off, and read none, so a stale token in .npmrc does not stop npm login
// and a stale password is no failed login. Every other npm path refuses
// both.
func TestNPMUserPathsTakeNoCredentials(t *testing.T) {
	w := newNPMAuthWorld(t)
	login := "/artifactory/api/npm/npm-local/-/user/org.couchdb.user:admin"
	body := `{"name":"admin","password":"pw"}`
	for name, headers := range map[string]map[string]string{
		"none":         nil,
		"stale bearer": bearerAuth("eyJstale.token.value"),
		"wrong basic":  basic("admin", "wrong"),
		"bad scheme":   {"Authorization": "Digest x"},
		"bad api key":  {"X-JFrog-Art-Api": "not-a-reference-token"},
	} {
		rec := do(t, w.h, "PUT", login, jsonAs(headers), body)
		if rec.Code != http.StatusCreated {
			t.Errorf("login, %s: %d %s", name, rec.Code, rec.Body)
		}
		rec = do(t, w.h, "DELETE", "/artifactory/api/npm/npm-local/-/user/token/x", headers, "")
		if rec.Code != http.StatusOK {
			t.Errorf("logout, %s: %d %s", name, rec.Code, rec.Body)
		}
		rec = do(t, w.h, "GET", "/artifactory/api/npm/npm-local/-/v1/search", headers, "")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("search, %s: %d %s", name, rec.Code, rec.Body)
		}
	}
	// The wrong password sent with them suspended nobody: admin's one
	// failure is the search's.
	for range 3 {
		do(t, w.h, "DELETE", "/artifactory/api/npm/npm-local/-/user/token/x",
			basic("admin", "wrong"), "")
	}
	rec := do(t, w.h, "GET", "/artifactory/api/npm/npm-local/-/whoami", basic("admin", "pw"), "")
	if rec.Code != http.StatusOK {
		t.Errorf("admin after wrong passwords sent with logouts: %d %s", rec.Code, rec.Body)
	}
	// A registry token, no credential outside the registry, is not read
	// either.
	reg, err := w.tokens.IssueRegistry(context.Background(), token.RegistryRequest{
		Username: "admin", ExpiresIn: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	rec = do(t, w.h, "PUT", login, jsonAs(bearerAuth(reg.AccessToken)), body)
	if rec.Code != http.StatusCreated {
		t.Errorf("login with a registry token: %d %s", rec.Code, rec.Body)
	}
}

// The token a logout names never reaches the access log.
func TestNPMLogoutTokenNotLogged(t *testing.T) {
	w := newNPMAuthWorld(t)
	rec := do(t, w.h, "PUT", "/artifactory/api/npm/npm-local/-/user/org.couchdb.user:admin",
		jsonAs(nil), `{"name":"admin","password":"pw"}`)
	tok, _ := decodeObject(t, rec.Body.String())["token"].(string)
	if tok == "" {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, w.h, "DELETE", "/artifactory/api/npm/npm-local/-/user/token/"+tok, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("logout: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(w.log.String(), tok) ||
		!strings.Contains(w.log.String(), "/-/user/token/[redacted]") {
		t.Errorf("access log: %s", w.log)
	}
	// The token is gone.
	rec = do(t, w.h, "GET", "/artifactory/api/npm/npm-local/-/whoami", bearerAuth(tok), "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("token after logout: %d %s", rec.Code, rec.Body)
	}
}

func TestNPMPaths(t *testing.T) {
	w := newNPMAuthWorld(t)
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{"DELETE", "/artifactory/api/npm/npm-local/-/user/token/x", 200},
		{"GET", "/artifactory/api/npm/auth", 401},
		{"GET", "/artifactory/api/npm/npm-local/auth/acme", 401},
		{"GET", "/artifactory/api/npm/npm-local/-/v1/login", 401},
		{"GET", "/artifactory/api/npm/npm-local", 401},
	} {
		if rec := do(t, w.h, c.method, c.path, nil, ""); rec.Code != c.want {
			t.Errorf("%s %s: status %d, want %d", c.method, c.path, rec.Code, c.want)
		}
	}
}

// Without a token service, npm cannot supply an encrypted password; an
// anonymous request still receives the normal authentication refusal.
func TestNPMCredentialWithoutOne(t *testing.T) {
	db := inmem.New()
	cheapSeed(t, db)
	c := repo.Config{Key: "npm-local", Class: repo.Local, PackageType: repo.NPM}
	if err := db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{Hash: testHash})
	if err != nil {
		t.Fatal(err)
	}
	h := server.New(server.Config{Store: db, Auth: a, CompatVersion: "7.155.0"})
	for _, tc := range []struct {
		path    string
		headers map[string]string
		want    int
	}{
		{"/artifactory/api/npm/auth", nil, 401},
		{"/artifactory/api/npm/auth", basic("admin", "pw"), 501},
		{"/artifactory/api/npm/npm-local/auth/acme", basic("admin", "pw"), 501},
	} {
		rec := do(t, h, "GET", tc.path, tc.headers, "")
		if rec.Code != tc.want {
			t.Errorf("%s: %d, want %d: %s", tc.path, rec.Code, tc.want, rec.Body)
		}
	}
}
