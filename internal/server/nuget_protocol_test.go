package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// nugetServer is the router over a store with nuget-local and
// nuget-forced, the seeded admin, and dev, who holds nothing; both
// passwords are "pw". It returns an access token of admin's.
func nugetServer(t *testing.T, tune ...func(*Config)) (http.Handler, *inmem.Store, string) {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	cheapSeed(t, db)
	if err := db.Users().Create(ctx, types.User{Name: "dev", PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	force := true
	for _, c := range []repo.Config{
		{Key: "nuget-local", Class: repo.Local, PackageType: repo.NuGet},
		{
			Key: "nuget-forced", Class: repo.Local, PackageType: repo.NuGet,
			ForceNugetAuthentication: &force,
		},
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
	issued, err := tokens.Issue(ctx, token.Request{Username: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{
		Hash: testHash, Tokens: tokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		CompatVersion: "7.155.0", BaseURL: "https://nuget.example", Store: db, Blobs: blobs,
		Auth: a, Tokens: tokens,
	}
	for _, fn := range tune {
		fn(&cfg)
	}
	h := New(cfg)
	return h, db, issued.AccessToken
}

// What dotnet nuget push, delete and restore send, through the whole
// server: the API key as Basic credentials on the NuGet routes, a re-push
// answered 409 for --skip-duplicate, the symbols beside the package, and
// a bearer token, which 7.50.3 refused there (RTFACT-29362).
func TestNuGetClientWrites(t *testing.T) {
	h, _, jwt := nugetServer(t)
	key := func(v string) map[string]string { return map[string]string{"X-NuGet-ApiKey": v} }
	pushAs := func(headers map[string]string, url string, pkg []byte) int {
		t.Helper()
		body := nugetForm(pkg)
		if headers == nil {
			headers = map[string]string{}
		}
		headers["Content-Type"], body = contentTypeOf(body)
		return do(t, h, http.MethodPut, url, headers, body).Code
	}
	const (
		v2 = "/artifactory/api/nuget/nuget-local/"
		v3 = "/artifactory/api/nuget/v3/nuget-local/"
	)
	// The client reads the service index with the source's credentials,
	// then pushes to PackagePublish with its key alone.
	rec := do(t, h, http.MethodGet, v3+"index.json", basic("admin", "pw"), "")
	if rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"@id":"https://nuget.example`+
			`/artifactory/api/nuget/nuget-local","@type":"PackagePublish/2.0.0"`) {
		t.Fatalf("service index %d %s", rec.Code, rec.Body)
	}
	pkg := nugetPackage("Acme.Lib", "1.0.0", "")
	for _, c := range []struct {
		name    string
		headers map[string]string
		pkg     []byte
		want    int
	}{
		{"key with an access token", key("admin:" + jwt), pkg, http.StatusCreated},
		{"skip-duplicate's 409", key("admin:pw"), pkg, http.StatusConflict},
		{"key with a password", key("admin:pw"), nugetPackage("Acme.Two", "1.0.0", ""), 201},
		{"bearer token", bearerAuth(jwt), nugetPackage("Acme.Three", "1.0.0", ""), 201},
		{"a user who may not deploy", key("dev:pw"), nugetPackage("Acme.Four", "1.0.0", ""), 403},
		// Last of dev's: a failed login may block the user for a while.
		{"a wrong key", key("dev:nope"), nugetPackage("Acme.Four", "1.0.0", ""), 401},
		{"a key alone", key(jwt), nugetPackage("Acme.Four", "1.0.0", ""), 401},
		{"no credentials", nil, nugetPackage("Acme.Four", "1.0.0", ""), 401},
	} {
		if got := pushAs(c.headers, v2, c.pkg); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
	if got := pushAs(key("admin:pw"), v3+"symbols/",
		nugetSymbolsPackage("Acme.Lib", "1.0.0")); got != http.StatusCreated {
		t.Fatalf("symbols push %d", got)
	}
	// A restore through the flat container, with the source's credentials.
	rec = do(t, h, http.MethodGet, v3+"flatcontainer/acme.lib/index.json", basic("admin", "pw"), "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"1.0.0"`) {
		t.Fatalf("versions %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodGet, v3+"flatcontainer/acme.lib/1.0.0/acme.lib.1.0.0.nupkg",
		basic("admin", "pw"), "")
	if rec.Code != http.StatusOK || rec.Body.String() != string(pkg) {
		t.Fatalf("download %d", rec.Code)
	}
	// A relist, then dotnet nuget delete, each with the key.
	if rec := do(t, h, http.MethodPost, v2+"Acme.Lib/1.0.0", key("admin:pw"), ""); rec.Code != 200 {
		t.Fatalf("relist %d %s", rec.Code, rec.Body)
	}
	if rec := do(
		t,
		h,
		http.MethodDelete,
		v2+"Acme.Lib/1.0.0",
		key("admin:pw"),
		"",
	); rec.Code != 204 {
		t.Fatalf("delete %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodGet, v3+"flatcontainer/acme.lib/index.json", basic("admin", "pw"), "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("after delete %d %s", rec.Code, rec.Body)
	}
}

// Force authentication challenges an anonymous request before it is
// routed, even with anonymous access on, and lets credentials through; a
// repository without it answers anonymous what anonymous may read.
func TestNuGetForceAuthentication(t *testing.T) {
	h, db, _ := nugetServer(t)
	if err := db.Settings().
		Put(context.Background(), types.SettingAnonymousAccess, true); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/artifactory/api/nuget/v3/nuget-forced/index.json",
		"/artifactory/api/nuget/v3/nuget-forced",
		"/artifactory/api/nuget/v3/nuget-forced/flatcontainer/x/index.json",
		"/artifactory/api/nuget/v3/nuget-forced/app.pdb/0123/app.pdb",
	} {
		rec := do(t, h, http.MethodGet, path, nil, "")
		if rec.Code != http.StatusUnauthorized ||
			rec.Header().Get("WWW-Authenticate") != apierror.BasicChallenge {
			t.Errorf("%s: %d %v", path, rec.Code, rec.Header())
		}
		if status, _ := artifactoryError(t, rec); status != http.StatusUnauthorized {
			t.Errorf("%s: envelope %d", path, status)
		}
	}
	for _, c := range []struct {
		path    string
		headers map[string]string
		want    int
	}{
		{"/artifactory/api/nuget/v3/nuget-forced/index.json", basic("dev", "pw"), 200},
		{"/artifactory/api/nuget/v3/nuget-forced/index.json", map[string]string{
			"X-NuGet-ApiKey": "admin:pw",
		}, 200},
		{"/artifactory/api/nuget/v3/nuget-local/index.json", nil, 200},
		{"/artifactory/api/nuget/v3/nuget-local/flatcontainer/x/index.json", nil, 404},
	} {
		if rec := do(t, h, http.MethodGet, c.path, c.headers, ""); rec.Code != c.want {
			t.Errorf("%s: %d, want %d: %s", c.path, rec.Code, c.want, rec.Body)
		}
	}
}
