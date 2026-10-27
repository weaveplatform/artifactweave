package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// tokenWorld is a registry with a real token service over the fixture,
// where reader reads docker-local's pub/ tree, writer deploys, deletes and
// annotates in all of docker-local, and anonymous reads pub/.
type tokenWorld struct {
	t      *testing.T
	db     store.Store
	tokens *token.Service
	h      http.Handler
}

func newTokenWorld(t *testing.T) *tokenWorld {
	t.Helper()
	ctx := context.Background()
	db := fixture(t)
	if err := db.Instance().Init(ctx, types.InstanceIdentity{
		InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	tokens, err := token.New(ctx, db, token.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []types.Permission{
		{Name: "readers", Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{
				"reader": types.ActionRead, auth.AnonymousUser: types.ActionRead,
			}},
			Targets: map[string]types.Target{"docker-local": {Include: []string{"pub/**"}}},
		}}},
		{Name: "writers", Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{
				"writer": types.ActionRead | types.ActionWrite | types.ActionDelete |
					types.ActionAnnotate,
			}},
			Targets: map[string]types.Target{"docker-local": {Include: []string{"**"}}},
		}}},
	} {
		if err := db.Permissions().Create(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Users().
		Create(ctx, types.User{Name: "writer", Email: "w@example.com"}); err != nil {
		t.Fatal(err)
	}
	g := New(Options{
		Store: db, Tokens: tokens, SubdomainMatch: true, BaseURL: "https://reg.example.com",
	})
	return &tokenWorld{
		t:      t,
		db:     db,
		tokens: tokens,
		h:      baseurl.Middleware("")(g.Challenge(router(g))),
	}
}

// as authorizes p as the server's middleware would.
func (w *tokenWorld) as(p auth.Principal) auth.Principal {
	w.t.Helper()
	p, err := auth.Authorize(context.Background(), authz.Sources{
		Permissions: w.db.Permissions(), Groups: w.db.Groups(), AnonymousEnabled: p.Anonymous,
	}, p)
	if err != nil {
		w.t.Fatal(err)
	}
	return p
}

// get makes a request as p, nil for none.
func (w *tokenWorld) get(target string, p *auth.Principal, host string) *httptest.ResponseRecorder {
	w.t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if host != "" {
		req.Host = host
	}
	if p != nil {
		req = req.WithContext(auth.WithPrincipal(req.Context(), w.as(*p)))
	}
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, req)
	return rec
}

// grants requests a token and returns what it grants.
func (w *tokenWorld) grants(target string, p auth.Principal, host string) *types.RegistryAccess {
	w.t.Helper()
	rec := w.get(target, &p, host)
	if rec.Code != http.StatusOK {
		w.t.Fatalf("%s: %d %s", target, rec.Code, rec.Body)
	}
	var body tokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		w.t.Fatal(err)
	}
	if body.Token == "" || body.Token != body.AccessToken || body.IssuedAt == "" {
		w.t.Fatalf("%s: %s", target, rec.Body)
	}
	rec2, err := w.tokens.Verify(context.Background(), body.Token)
	if err != nil {
		w.t.Fatal(err)
	}
	return rec2.Registry
}

func TestAccTokenGrants(t *testing.T) {
	w := newTokenWorld(t)
	reader := auth.Principal{Name: "reader"}
	writer := auth.Principal{Name: "writer"}
	repoGrant := func(name string, actions ...string) types.RegistryGrant {
		return types.RegistryGrant{Type: scopeRepository, Name: name, Actions: actions}
	}
	cases := []struct {
		name, target, host string
		p                  auth.Principal
		expected           types.RegistryAccess
	}{
		{
			"no scope",
			"/v2/token",
			"",
			reader,
			types.RegistryAccess{Grants: []types.RegistryGrant{}},
		},
		{
			"null realm, repository path name",
			"/artifactory/api/docker/null/v2/token" +
				"?scope=repository:docker-local/pub/app:pull,push&service=reg.example.com",
			"",
			reader,
			types.RegistryAccess{
				Grants: []types.RegistryGrant{repoGrant("docker-local/pub/app", "pull")},
			},
		},
		{
			"realm with the image alone", "/artifactory/api/docker/docker-local/v2/token" +
				"?scope=repository:pub/app:pull", "", reader,
			types.RegistryAccess{
				Realm: "docker-local", Grants: []types.RegistryGrant{repoGrant("pub/app", "pull")},
			},
		},
		{
			// The repository path's own challenge names the realm too.
			"realm with the repository path name", "/artifactory/api/docker/Docker-Local/v2/token" +
				"?scope=repository:docker-local/pub/app:pull", "", reader,
			types.RegistryAccess{
				Realm:  "Docker-Local",
				Grants: []types.RegistryGrant{repoGrant("docker-local/pub/app", "pull")},
			},
		},
		{
			"outside the reader's tree",
			"/v2/token?scope=repository:docker-local/priv/app:pull",
			"",
			reader,
			types.RegistryAccess{Grants: []types.RegistryGrant{}},
		},
		{
			"everything, scopes in one parameter and in two", "/v2/token?scope=" +
				"repository:docker-local/app:*+registry:catalog:*&scope=repository:oci-local/app:pull" +
				"&scope=repository:missing/app:pull&scope=repository:docker-local/Bad:pull" +
				"&scope=repository:generic-local/app:pull&scope=repository:docker-local/app:pull,bogus" +
				"&scope=repository(plugin):docker-local/app:pull&scope=nonsense&scope=a:b" +
				"&scope=repository:docker-local:pull",
			"", writer,
			types.RegistryAccess{Grants: []types.RegistryGrant{
				repoGrant("docker-local/app", "pull", "push", "delete"),
				{Type: scopeRegistry, Name: "catalog", Actions: []string{"*"}},
				repoGrant("docker-local/app", "pull"),
			}},
		},
		{
			"subdomain",
			"/v2/token?scope=repository:app:pull,push",
			"docker-local.reg.example.com",
			writer,
			types.RegistryAccess{
				Realm:  "docker-local",
				Grants: []types.RegistryGrant{repoGrant("app", "pull", "push")},
			},
		},
		{
			"anonymous",
			"/v2/token?scope=repository:docker-local/pub/x:pull,push",
			"",
			auth.Anonymous,
			types.RegistryAccess{
				Grants: []types.RegistryGrant{repoGrant("docker-local/pub/x", "pull")},
			},
		},
	}
	for _, c := range cases {
		got := w.grants(c.target, c.p, c.host)
		if diff := cmp.Diff(&c.expected, got); diff != "" {
			t.Errorf("%s: %s", c.name, diff)
		}
	}
}

// A registry token that obtains another holds no more than it had, under
// the realm it was issued for; and the new one lives no longer than it.
func TestAccTokenFromARegistryToken(t *testing.T) {
	w := newTokenWorld(t)
	have := &types.RegistryAccess{Grants: []types.RegistryGrant{
		{Type: scopeRepository, Name: "docker-local/app", Actions: []string{"pull"}},
		{Type: scopeRepository, Name: "docker-local/all", Actions: []string{"*"}},
	}}
	p := auth.Principal{
		Name: "writer", Registry: have, TokenExpires: time.Now().Add(10 * time.Minute),
	}
	got := w.grants("/v2/token?scope=repository:docker-local/app:pull,push"+
		"&scope=repository:docker-local/all:push&scope=repository:docker-local/other:pull", p, "")
	want := &types.RegistryAccess{Grants: []types.RegistryGrant{
		{Type: scopeRepository, Name: "docker-local/app", Actions: []string{"pull"}},
		{Type: scopeRepository, Name: "docker-local/all", Actions: []string{"push"}},
	}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatal(diff)
	}
	if got := w.grants("/artifactory/api/docker/docker-local/v2/token?scope="+
		"repository:docker-local/app:pull", p, ""); len(got.Grants) != 0 {
		t.Fatalf("another realm: %+v", got)
	}
	rec := w.get("/v2/token", &p, "")
	var body tokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil ||
		body.ExpiresIn > 600 || body.ExpiresIn < 590 {
		t.Fatalf("lifetime: %s, %v", rec.Body, err)
	}
}

// A token from an OIDC exchange may name a user who is not stored; the
// registry token it obtains keeps its identity and scope (decision 0005),
// so it later resolves to the same transient or synthetic user.
func TestAccTokenKeepsTheCallersIdentityAndScope(t *testing.T) {
	w := newTokenWorld(t)
	groups, err := types.ParseScope("applied-permissions/groups:ci")
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		p           auth.Principal
		expectedScp string
	}{
		// reader holds Read on docker-local/pub/ but is not a stored user.
		"transient": {
			auth.Principal{Name: "reader", Identity: types.IdentityTransient},
			"applied-permissions/user",
		},
		"synthetic": {
			auth.Principal{Name: "oidc-github-ci", Identity: types.IdentitySynthetic, Scope: groups},
			"applied-permissions/groups:ci",
		},
	} {
		rec := w.get("/v2/token?scope=repository:docker-local/pub/app:pull", &c.p, "")
		var body tokenResponse
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
		got, err := w.tokens.Verify(context.Background(), body.Token)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.Identity != c.p.Identity || got.Scope.String() != c.expectedScp {
			t.Errorf("%s: identity %q scope %q", name, got.Identity, got.Scope)
		}
	}
}

// A registry token is bound to the stored token behind its caller, and a
// registry token caller passes on the one it was bound to, so revoking
// that token ends every registry token descended from it.
func TestAccTokenBindsToTheParentToken(t *testing.T) {
	w := newTokenWorld(t)
	ctx := context.Background()
	hour := time.Hour
	parent, err := w.tokens.Issue(ctx, token.Request{
		Username: "writer", ExpiresIn: &hour, ByAdmin: true, ForceRevocable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	have := &types.RegistryAccess{Grants: []types.RegistryGrant{
		{Type: scopeRepository, Name: "docker-local/app", Actions: []string{"pull"}},
	}}
	for name, p := range map[string]auth.Principal{
		"token":          {Name: "writer", TokenID: parent.Record.ID},
		"registry token": {Name: "writer", TokenID: "not-stored", Registry: have, TokenParent: parent.Record.ID},
	} {
		rec := w.get("/v2/token?scope=repository:docker-local/app:pull", &p, "")
		var body tokenResponse
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
		got, err := w.tokens.Verify(ctx, body.Token)
		if err != nil || got.Parent != parent.Record.ID {
			t.Errorf("%s: parent %q, %v", name, got.Parent, err)
		}
	}
	// A password caller's token has no parent.
	rec := w.get("/v2/token", &auth.Principal{Name: "writer"}, "")
	var body tokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if got, err := w.tokens.Verify(ctx, body.Token); err != nil || got.Parent != "" {
		t.Errorf("password: parent %q, %v", got.Parent, err)
	}
}

func TestAccLifetime(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	for name, c := range map[string]struct {
		expires  time.Time
		expected time.Duration
	}{
		"password":     {time.Time{}, time.Hour},
		"later":        {now.Add(5 * time.Hour), time.Hour},
		"sooner":       {now.Add(90*time.Second + 500*time.Millisecond), 90 * time.Second},
		"at the brink": {now.Add(300 * time.Millisecond), time.Second},
	} {
		if got := lifetime(auth.Principal{TokenExpires: c.expires}, now); got != c.expected {
			t.Errorf("%s: %s, want %s", name, got, c.expected)
		}
	}
}

func TestAccTokenEndpointErrors(t *testing.T) {
	w := newTokenWorld(t)
	// No principal: a server without authentication.
	if rec := w.get("/v2/token", nil, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no principal: %d %s", rec.Code, rec.Body)
	}
	// POST answers 405 (research 17).
	req := httptest.NewRequest(http.MethodPost, "/v2/token", nil)
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("POST: %d %v", rec.Code, rec.Header())
	}
	// Without an issuer the endpoint is not implemented.
	none := router(New(Options{Store: w.db}))
	if rec := serve(
		t,
		none,
		http.MethodGet,
		"/v2/token",
		nil,
	); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("no issuer: %d %s", rec.Code, rec.Body)
	}
	// An issuer or store failure is a 500, noted for the log.
	var noted []error
	note := func(_ *http.Request, err error) { noted = append(noted, err) }
	for name, g := range map[string]*Registry{
		"issuer": New(Options{Store: w.db, Tokens: failingIssuer{}, Fault: note}),
		"store": New(Options{
			Store:  faultyStore{w.db, faultyRepos{RepoStore: w.db.Repos(), get: true}},
			Tokens: w.tokens, Fault: note,
		}),
	} {
		req := httptest.NewRequest(http.MethodGet,
			"/v2/token?scope=repository:docker-local/app:pull", nil)
		req = req.WithContext(
			auth.WithPrincipal(req.Context(), w.as(auth.Principal{Name: "writer"})),
		)
		rec := httptest.NewRecorder()
		router(g).ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if len(noted) != 2 {
		t.Fatalf("noted %v", noted)
	}
}

type failingIssuer struct{}

func (failingIssuer) IssueRegistry(context.Context, token.RegistryRequest) (token.Issued, error) {
	return token.Issued{}, errors.New("no key")
}

func TestAccRequested(t *testing.T) {
	for in, want := range map[string][]string{
		"pull":               {"pull"},
		"push,pull":          {"push", "pull"},
		"pull,pull,x,delete": {"pull", "delete"},
		"pull,*":             {"pull", "push", "delete"},
		"":                   nil,
	} {
		if got := requested(in); !cmp.Equal(got, want) {
			t.Errorf("requested(%q) = %v, want %v", in, got, want)
		}
	}
}

// The challenge every registry 401 carries, from whatever wrote it.
func TestAccChallenge(t *testing.T) {
	g := New(Options{SubdomainMatch: true, BaseURL: "https://reg.example.com"})
	const realm = `Bearer realm="https://reg.example.com/artifactory/api/docker/`
	unauthorized := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		apierror.Write(
			w,
			apierror.SurfaceOCI,
			Errorf(http.StatusUnauthorized, CodeUnauthorized, "no"),
		)
	})
	cases := []struct {
		name, method, target, host, expected string
	}{
		{"probe", "GET", "/v2/", "", realm + `null/v2/token",service="reg.example.com"`},
		{"bare", "GET", "/v2", "", realm + `null/v2/token",service="reg.example.com"`},
		{"token", "GET", "/v2/token", "", realm + `null/v2/token",service="reg.example.com"`},
		{
			"pull on the repository path", "GET", "/v2/docker-local/team/app/manifests/v1", "",
			realm + `docker-local/v2/token",service="reg.example.com",` +
				`scope="repository:docker-local/team/app:pull"`,
		},
		{
			"push", "PUT", "/v2/docker-local/app/manifests/v1", "",
			realm + `docker-local/v2/token",service="reg.example.com",` +
				`scope="repository:docker-local/app:pull,push"`,
		},
		{
			"delete", "DELETE", "/v2/docker-local/app/manifests/v1", "",
			realm + `docker-local/v2/token",service="reg.example.com",` +
				`scope="repository:docker-local/app:delete"`,
		},
		{
			"prefix", "HEAD", "/artifactory/api/docker/docker-local/v2/app/blobs/sha256:ab", "",
			realm + `docker-local/v2/token",service="reg.example.com",` +
				`scope="repository:app:pull"`,
		},
		{
			"prefix probe", "GET", "/artifactory/api/docker/docker-local/v2/", "",
			realm + `docker-local/v2/token",service="reg.example.com"`,
		},
		{
			"subdomain", "POST", "/v2/app/blobs/uploads/", "docker-local.reg.example.com",
			realm + `docker-local/v2/token",service="reg.example.com",` +
				`scope="repository:app:pull,push"`,
		},
		{
			"upload status", "GET", "/v2/docker-local/app/blobs/uploads/abc", "",
			realm + `docker-local/v2/token",service="reg.example.com",` +
				`scope="repository:docker-local/app:pull,push"`,
		},
		{
			"upload cancel", "DELETE", "/v2/docker-local/app/blobs/uploads/abc", "",
			realm + `docker-local/v2/token",service="reg.example.com",` +
				`scope="repository:docker-local/app:pull,push"`,
		},
		{
			"subdomain probe", "GET", "/v2/", "docker-local.reg.example.com",
			realm + `docker-local/v2/token",service="reg.example.com"`,
		},
		{
			"catalog", "GET", "/v2/_catalog", "",
			realm + `null/v2/token",service="reg.example.com",scope="registry:catalog:*"`,
		},
		{
			"repository catalog", "GET", "/v2/docker-local/_catalog", "",
			realm + `docker-local/v2/token",service="reg.example.com",scope="registry:catalog:*"`,
		},
		{
			"no operation", "GET", "/v2/docker-local/app", "",
			realm + `null/v2/token",service="reg.example.com"`,
		},
		{
			// A quote in the path never reaches the header.
			"quote", "GET", "/v2/docker-local/a%22b/manifests/v1", "",
			realm + `docker-local/v2/token",service="reg.example.com"`,
		},
		{
			"quoted realm", "GET", "/artifactory/api/docker/a%22b/v2/", "",
			realm + `null/v2/token",service="reg.example.com"`,
		},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.target, nil)
		if c.host != "" {
			req.Host = c.host
		}
		rec := httptest.NewRecorder()
		baseurl.Middleware("https://reg.example.com")(g.Challenge(unauthorized)).ServeHTTP(rec, req)
		if got := rec.Header().Get("WWW-Authenticate"); got != c.expected {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.expected)
		}
	}
	// Without subdomain matching a host never names a repository, and the
	// base URL comes from the request.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v2/", nil)
	req.Host = "docker-local.reg.example.com:8080"
	baseurl.Middleware("")(New(Options{}).Challenge(unauthorized)).ServeHTTP(rec, req)
	want := `Bearer realm="http://docker-local.reg.example.com:8080/artifactory/api/docker/null` +
		`/v2/token",service="docker-local.reg.example.com:8080"`
	if got := rec.Header().Get("WWW-Authenticate"); got != want {
		t.Errorf("no subdomains: %s", got)
	}
}

// The challenge replaces only the default; other answers and other paths
// keep what they wrote.
func TestAccChallengeLeavesOthers(t *testing.T) {
	g := New(Options{})
	wrap := func(h http.HandlerFunc) http.Handler { return baseurl.Middleware("")(g.Challenge(h)) }
	custom := `Bearer realm="https://x/token",scope="repository:a:pull repository:b:pull"`
	for name, c := range map[string]struct {
		target   string
		h        http.HandlerFunc
		expected string
	}{
		"ok": {"/v2/", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}, ""},
		"own challenge": {"/v2/", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("WWW-Authenticate", custom)
			w.WriteHeader(http.StatusUnauthorized)
		}, custom},
		"not the registry": {"/artifactory/api/repositories", func(w http.ResponseWriter, _ *http.Request) {
			apierror.Write(w, apierror.SurfaceArtifactory, auth.ErrUnauthenticated)
		}, apierror.BasicChallenge},
	} {
		rec := httptest.NewRecorder()
		wrap(c.h).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.target, nil))
		if got := rec.Header().Get("WWW-Authenticate"); got != c.expected {
			t.Errorf("%s: %q", name, got)
		}
	}
}

// The writer underneath keeps streaming: Flush, ReadFrom and Unwrap reach it.
func TestAccChallengerPassesThrough(t *testing.T) {
	rec := httptest.NewRecorder()
	c := &challenger{ResponseWriter: rec}
	if n, err := c.ReadFrom(strings.NewReader("layer")); err != nil || n != 5 {
		t.Fatalf("ReadFrom: %d, %v", n, err)
	}
	c.Flush()
	if !rec.Flushed || rec.Body.String() != "layer" || c.Unwrap() != rec {
		t.Fatalf("flushed %v, body %q", rec.Flushed, rec.Body)
	}
	// A writer that cannot flush is left alone.
	(&challenger{ResponseWriter: plainWriter{rec}}).Flush()
}

type plainWriter struct{ http.ResponseWriter }

func TestAccService(t *testing.T) {
	for base, want := range map[string]string{
		"https://reg.example.com":       "reg.example.com",
		"https://reg.example.com:443":   "reg.example.com",
		"http://reg.example.com:80":     "reg.example.com",
		"https://reg.example.com:8443":  "reg.example.com:8443",
		"http://127.0.0.1:5000/prefix":  "127.0.0.1:5000",
		"http://[::1]:80":               "::1",
		"://bad":                        "",
		"https://reg.example.com:80/x/": "reg.example.com:80",
	} {
		if got := service(base); got != want {
			t.Errorf("service(%q) = %q, want %q", base, got, want)
		}
	}
}

// A registry token grants what Require asks only for the image as the
// request names it, under its realm.
func TestAccRequireRegistryToken(t *testing.T) {
	w := newTokenWorld(t)
	c, err := w.db.Repos().Get(context.Background(), "docker-local")
	if err != nil {
		t.Fatal(err)
	}
	pathMethod := Request{RepoKey: "docker-local", Repo: c, Name: "app", base: "/v2/docker-local"}
	prefixed := Request{
		RepoKey: "Docker-Local",
		Repo:    c,
		Name:    "app",
		base:    prefix + "Docker-Local/v2",
	}
	grants := func(realm string, gs ...types.RegistryGrant) *types.RegistryAccess {
		return &types.RegistryAccess{Realm: realm, Grants: gs}
	}
	pull := func(name string) types.RegistryGrant {
		return types.RegistryGrant{Type: scopeRepository, Name: name, Actions: []string{"pull"}}
	}
	all := types.RegistryGrant{
		Type:    scopeRepository,
		Name:    "docker-local/app",
		Actions: []string{"*"},
	}
	cases := []struct {
		name     string
		q        Request
		have     *types.RegistryAccess
		expected types.Actions
		ok       bool
	}{
		{
			"null realm on the path",
			pathMethod,
			grants("", pull("docker-local/app")),
			types.ActionRead,
			true,
		},
		{"null realm on the prefix", prefixed, grants("", pull("app")), types.ActionRead, false},
		{
			"realm on the prefix",
			prefixed,
			grants("docker-local", pull("app")),
			types.ActionRead,
			true,
		},
		{
			"realm on the path", pathMethod, grants("docker-local", pull("docker-local/app")),
			types.ActionRead, true,
		},
		{"another realm", prefixed, grants("oci-local", pull("app")), types.ActionRead, false},
		{
			"another image",
			pathMethod,
			grants("", pull("docker-local/other")),
			types.ActionRead,
			false,
		},
		{
			"pull is not push",
			pathMethod,
			grants("", pull("docker-local/app")),
			types.ActionWrite,
			false,
		},
		{
			"star",
			pathMethod,
			grants("", all),
			types.ActionRead | types.ActionWrite | types.ActionDelete,
			true,
		},
		{"annotate is push", pathMethod, grants("", all), types.ActionAnnotate, true},
		{"delete in a push is push", pathMethod, grants("", types.RegistryGrant{
			Type: scopeRepository, Name: "docker-local/app", Actions: []string{"push"},
		}), types.ActionDelete, true},
		{"manage never", pathMethod, grants("", all), types.ActionManage, false},
		{
			"catalog grant is no image",
			pathMethod,
			grants(
				"",
				types.RegistryGrant{
					Type:    scopeRegistry,
					Name:    "docker-local/app",
					Actions: []string{"*"},
				},
			),
			types.ActionRead,
			false,
		},
	}
	for _, tc := range cases {
		p := w.as(auth.Principal{Name: "writer", Registry: tc.have})
		r := httptest.NewRequest(http.MethodPut, "/", nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		err := tc.q.Require(r, "app/v1/manifest.json", tc.expected)
		var e *apierror.Error
		switch {
		case tc.ok && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case !tc.ok && (!errors.As(err, &e) || e.Status != http.StatusUnauthorized):
			t.Errorf("%s: %v, want 401", tc.name, err)
		}
	}
	// A DELETE needs delete itself; push does not cover it.
	for actions, ok := range map[string]bool{"push": false, "delete": true} {
		p := w.as(auth.Principal{Name: "writer", Registry: grants("", types.RegistryGrant{
			Type: scopeRepository, Name: "docker-local/app", Actions: []string{actions},
		})})
		r := httptest.NewRequest(http.MethodDelete, "/", nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		if err := pathMethod.Require(r, "app/v1/", types.ActionDelete); (err == nil) != ok {
			t.Errorf("DELETE with %s: %v", actions, err)
		}
	}
	// The grant does not override the permissions: reader's token for
	// priv/ still answers DENIED.
	p := w.as(auth.Principal{Name: "reader", Registry: grants("", pull("docker-local/priv"))})
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r = r.WithContext(auth.WithPrincipal(r.Context(), p))
	q := Request{RepoKey: "docker-local", Repo: c, Name: "priv", base: "/v2/docker-local"}
	var e *apierror.Error
	if err := q.Require(r, "priv", types.ActionRead); !errors.As(err, &e) || e.Code != CodeDenied {
		t.Fatalf("permissions: %v", err)
	}
}

func TestAccGrantsCatalog(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	with := func(p auth.Principal) *http.Request {
		return r.WithContext(auth.WithPrincipal(r.Context(), p))
	}
	catalog := types.RegistryGrant{Type: scopeRegistry, Name: "catalog", Actions: []string{"*"}}
	for name, c := range map[string]struct {
		r        *http.Request
		expected bool
	}{
		"no principal":  {r, true},
		"password":      {with(auth.Principal{Name: "a"}), true},
		"granted":       {with(auth.Principal{Registry: &types.RegistryAccess{Grants: []types.RegistryGrant{catalog}}}), true},
		"not granted":   {with(auth.Principal{Registry: &types.RegistryAccess{}}), false},
		"empty actions": {with(auth.Principal{Registry: &types.RegistryAccess{Grants: []types.RegistryGrant{{Type: scopeRegistry, Name: "catalog"}}}}), false},
	} {
		if got := GrantsCatalog(c.r); got != c.expected {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestAccAuthEndpoints(t *testing.T) {
	w := newTokenWorld(t)
	writer := auth.Principal{Name: "writer"}
	// The entry is keyed by the registry's URL, as 7.x answers it for
	// ~/.docker/config.json: here the base URL resolved from the request.
	entry := func(user, secret, email string) string {
		b, _ := json.Marshal(map[string]authEntry{"http://example.com": {
			Auth: base64.StdEncoding.EncodeToString([]byte(user + ":" + secret)), Email: email,
		}})
		return string(b) + "\n"
	}
	for _, target := range []string{
		"/artifactory/api/docker/docker-local/v2/auth", "/artifactory/api/docker/docker-local/v1/auth",
	} {
		for name, c := range map[string]struct {
			headers map[string]string
			p       *auth.Principal
			status  int
			body    string
		}{
			"password": {
				map[string]string{"Authorization": "Basic " +
					base64.StdEncoding.EncodeToString([]byte("writer:pw"))},
				&writer, 200, entry("writer", "pw", "w@example.com"),
			},
			"bearer": {
				map[string]string{"Authorization": "Bearer tok"},
				&writer, 200,
				entry("writer", "tok", "w@example.com"),
			},
			"api key header": {
				map[string]string{"X-JFrog-Art-Api": "ref"},
				&auth.Principal{Name: "ghost"}, 200,
				entry("ghost", "ref", ""),
			},
			"anonymous": {nil, &auth.Anonymous, 401, ""},
			"nobody":    {nil, nil, 401, ""},
			"no secret": {nil, &writer, 401, ""},
		} {
			req := httptest.NewRequest(http.MethodGet, target, nil)
			for k, v := range c.headers {
				req.Header.Set(k, v)
			}
			if c.p != nil {
				req = req.WithContext(auth.WithPrincipal(req.Context(), *c.p))
			}
			rec := httptest.NewRecorder()
			w.h.ServeHTTP(rec, req)
			if rec.Code != c.status || c.body != "" && rec.Body.String() != c.body {
				t.Errorf("%s %s: %d %s", target, name, rec.Code, rec.Body)
			}
		}
	}
	// The base URL names the registry by its scheme and host alone: a path
	// is dropped and a port kept.
	for base, key := range map[string]string{
		"https://artprod.company.com":                  "https://artprod.company.com",
		"https://artprod.company.com:8443/artifactory": "https://artprod.company.com:8443",
	} {
		req := httptest.NewRequest(
			http.MethodGet,
			"/artifactory/api/docker/docker-local/v2/auth",
			nil,
		)
		req.Header.Set("Authorization", "Bearer tok")
		req.Header.Set(baseurl.OverrideHeader, base)
		req = req.WithContext(auth.WithPrincipal(req.Context(), writer))
		rec := httptest.NewRecorder()
		w.h.ServeHTTP(rec, req)
		var got map[string]authEntry
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got) != 1 ||
			got[key].Email != "w@example.com" {
			t.Errorf("%s: %d %s", base, rec.Code, rec.Body)
		}
	}
	// A request the base-URL middleware never saw is keyed by its own host,
	// over TLS with https.
	tlsReq := httptest.NewRequest(http.MethodGet, "https://reg.example/v1/auth", nil)
	if got := registryURL(tlsReq); got != "https://reg.example" {
		t.Errorf("TLS: %q", got)
	}
	// A store failure answers 500 and is noted.
	var noted error
	g := New(Options{
		Store: failingUsers{w.db}, Fault: func(_ *http.Request, err error) { noted = err },
	})
	req := httptest.NewRequest(http.MethodGet, "/artifactory/api/docker/r/v1/auth", nil)
	req.Header.Set("Authorization", "Bearer tok")
	req = req.WithContext(auth.WithPrincipal(req.Context(), writer))
	rec := httptest.NewRecorder()
	router(g).ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || noted == nil {
		t.Fatalf("store failure: %d %v", rec.Code, noted)
	}
	body, _ := io.ReadAll(rec.Body)
	if strings.Contains(string(body), "database") {
		t.Fatalf("leaked: %s", body)
	}
}

// failingUsers is a store whose users cannot be read.
type failingUsers struct{ store.Store }

func (failingUsers) Users() store.UserStore { return brokenUsers{} }

type brokenUsers struct{ store.UserStore }

func (brokenUsers) Get(context.Context, string) (types.User, error) {
	return types.User{}, errors.New("database down")
}
