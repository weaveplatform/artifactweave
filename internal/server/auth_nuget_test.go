package server

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// nugetKeyFixture is the router over a seeded store with the P8 reindex
// repositories and users without rights, "dev" and "u1" to "u4", whose
// password is "pw" as every test principal's is. A failed login blocks its
// user for a while, so each refusal a test makes on purpose names a user of
// its own. Its access log goes to log.
type nugetKeyFixture struct {
	h      http.Handler
	db     *inmem.Store
	tokens *token.Service
	log    *bytes.Buffer
}

func newNugetKeyFixture(t *testing.T) *nugetKeyFixture {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	cheapSeed(t, db)
	createReindexRepos(t, db)
	for _, name := range []string{"dev", "u1", "u2", "u3", "u4"} {
		if err := db.Users().
			Create(ctx, types.User{Name: name, PasswordHash: cheapHash}); err != nil {
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
	cfg := Config{
		CompatVersion: "7.155.0", Store: db, Auth: a,
		Logger: slog.New(slog.NewJSONHandler(&log, nil)),
	}
	withReindexFakes(&cfg)
	return &nugetKeyFixture{h: New(cfg), db: db, tokens: tokens, log: &log}
}

// X-NuGet-ApiKey is Basic credentials, "<user>:<password or token>", on the
// NuGet routes, read only without an Authorization header (decision 0038).
// The NuGet reindex needs Manage, which admin holds and dev does not, so
// its answer tells who the key named.
func TestNuGetAPIKey(t *testing.T) {
	f := newNugetKeyFixture(t)
	issued, err := f.tokens.Issue(context.Background(), token.Request{
		Username: "admin", IncludeReference: true, ForceRevocable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	jwt, ref := issued.AccessToken, issued.ReferenceToken
	const reindex = "/artifactory/api/nuget/reindex-nuget/reindex"
	key := func(v string) map[string]string { return map[string]string{"X-NuGet-ApiKey": v} }
	with := func(h map[string]string, k, v string) map[string]string {
		out := map[string]string{k: v}
		for name, value := range h {
			out[name] = value
		}
		return out
	}
	for _, c := range []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"password", key("admin:pw"), http.StatusOK},
		{"access token", key("admin:" + jwt), http.StatusOK},
		{"reference token", key("admin:" + ref), http.StatusOK},
		{"a user without it", key("dev:pw"), http.StatusForbidden},
		{"wrong password", key("u1:nope"), http.StatusUnauthorized},
		{"token of another", key("u2:" + jwt), http.StatusUnauthorized},
		// Split at the first colon, so the secret is "pw:x".
		{"a secret with a colon", key("u3:pw:x"), http.StatusUnauthorized},
		{"no colon", key("adminpw"), http.StatusUnauthorized},
		{"no user", key(":pw"), http.StatusUnauthorized},
		{"no secret", key("admin:"), http.StatusUnauthorized},
		// The Authorization header wins, and the key is not read at all.
		{"basic before a bad key", with(basic("admin", "pw"), "X-NuGet-ApiKey", "ghost:x"), http.StatusOK},
		{"bearer before a bad key", with(key("ghost:x"), "Authorization", "Bearer "+jwt), http.StatusOK},
		{"bad basic before a key", with(basic("u4", "nope"), "X-NuGet-ApiKey", "admin:pw"), http.StatusUnauthorized},
		{"basic dev before a key", with(basic("dev", "pw"), "X-NuGet-ApiKey", "admin:pw"), http.StatusForbidden},
		{"nothing", nil, http.StatusUnauthorized},
	} {
		rec := do(t, f.h, http.MethodPost, reindex, c.headers, "")
		if rec.Code != c.want {
			t.Errorf("%s: %d, want %d: %s", c.name, rec.Code, c.want, rec.Body)
			continue
		}
		if c.want == http.StatusUnauthorized &&
			rec.Header().Get("WWW-Authenticate") != apierror.BasicChallenge {
			t.Errorf("%s: 401 without the Basic challenge", c.name)
		}
	}
	// The access log names the user the key named, never the key.
	if !strings.Contains(f.log.String(), `"user":"admin"`) ||
		strings.Contains(f.log.String(), jwt) || strings.Contains(f.log.String(), "nope") {
		t.Fatalf("access log %s", f.log)
	}
}

// Elsewhere the key is not read: the request is anonymous, or refused when
// anonymous access is off.
func TestNuGetAPIKeyElsewhere(t *testing.T) {
	f := newNugetKeyFixture(t)
	admin := map[string]string{"X-NuGet-ApiKey": "admin:pw"}
	for _, path := range []string{
		"/artifactory/api/helm/reindex-helm/reindex",
		"/artifactory/api/nugetx/reindex-nuget/reindex",
		"/artifactory/reindex-nuget/a.nupkg",
	} {
		rec := do(t, f.h, http.MethodPost, path, admin, "")
		if rec.Code != http.StatusUnauthorized ||
			!strings.Contains(rec.Body.String(), "Authentication is required") {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	if err := f.db.Settings().
		Put(context.Background(), types.SettingAnonymousAccess, true); err != nil {
		t.Fatal(err)
	}
	f.log.Reset()
	rec := do(t, f.h, http.MethodGet, "/artifactory/api/repositories", admin, "")
	if rec.Code != http.StatusOK || rec.Body.String() != "[]\n" {
		t.Fatalf("anonymous list: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(f.log.String(), `"user":"anonymous"`) {
		t.Fatalf("access log %s", f.log)
	}
	// A key that would fail is not read there either.
	if rec := do(t, f.h, http.MethodGet, "/artifactory/api/repositories",
		map[string]string{"X-NuGet-ApiKey": "admin:nope"}, ""); rec.Code != http.StatusOK {
		t.Fatalf("bad key elsewhere: %d %s", rec.Code, rec.Body)
	}
}
