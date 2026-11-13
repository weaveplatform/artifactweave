package itemkit

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

// errBoom is the failure the scripted authorizer returns.
var errBoom = errors.New("boom")

// TestUnitAccessForEvaluatesWithoutDecider: AccessFor keeps the decider it is
// given and asks Authorizer.For only when it has none, returning For's error
// unwrapped. Over the access module that evaluation reads the anonymous
// access setting, which counts as off while it was never written, so the
// anonymous user's grants apply only once it is on.
func TestUnitAccessForEvaluatesWithoutDecider(t *testing.T) {
	ctx := t.Context()
	asked := false
	scripted := &accessapitest.Authorizer{DecideFunc: func(
		context.Context, accessapi.Subject, bool,
	) (accessapi.Decider, error) {
		asked = true
		return nil, errBoom
	}}
	given := &accessapitest.Decider{Admin: true}
	acc, err := AccessFor(ctx, scripted, kernel.Principal{Name: "ann"}, given)
	if err != nil || acc.Decider != given || asked {
		t.Errorf(
			"with a decider: %v, %v, evaluated %t; want the decider kept",
			acc.Decider,
			err,
			asked,
		)
	}
	acc, err = AccessFor(ctx, scripted, kernel.Principal{Name: "ann"}, nil)
	if acc.Decider != nil || !errors.Is(err, errBoom) ||
		err.Error() != "evaluate permissions: boom" {
		t.Errorf("without a decider: %v, %v; want For's error as it is", acc.Decider, err)
	}

	st := inmem.New()
	c := repo.Config{Key: "libs-local", Class: repo.Local}
	if err := st.Permissions().Create(ctx, accesstest.Permission{
		Name: "anonymous-reads",
		Resources: map[accessapi.ResourceType]accesstest.Resource{accessapi.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]accessapi.Actions{
				kernel.AnonymousUser: accessapi.ActionRead,
			}},
			Targets: map[string]accesstest.Target{c.Key: {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	reads := func() bool {
		t.Helper()
		acc, err := AccessFor(ctx, accesstest.Over(t, st).API().Authorizer, kernel.Anonymous, nil)
		if err != nil {
			t.Fatal(err)
		}
		return acc.File(c, "a.txt", accessapi.ActionRead)
	}
	if reads() {
		t.Error("anonymous reads while the anonymous access setting was never written")
	}
	if err := st.Settings().Put(ctx, settings.SettingAnonymousAccess, true); err != nil {
		t.Fatal(err)
	}
	if !reads() {
		t.Error("anonymous cannot read once anonymous access is on")
	}
}

// TestUnitForcedIsAMavenOrP2VirtualRepositorysFlag: only a Maven virtual
// repository's forceMavenAuthentication and a P2 virtual repository's
// forceP2Authentication force authentication, as in 7.x's filter. The
// other Maven-family types, a generic virtual repository, the other
// classes and the other type's flag change nothing.
func TestUnitForcedIsAMavenOrP2VirtualRepositorysFlag(t *testing.T) {
	on, off := true, false
	for _, tc := range []struct {
		name string
		c    repo.Config
		want bool
	}{
		{"maven virtual", repo.Config{Class: repo.Virtual, PackageType: repo.Maven, ForceMavenAuthentication: &on}, true},
		{"p2 virtual", repo.Config{Class: repo.Virtual, PackageType: repo.P2, ForceP2Authentication: &on}, true},
		{"maven virtual, off", repo.Config{Class: repo.Virtual, PackageType: repo.Maven, ForceMavenAuthentication: &off}, false},
		{"maven virtual, unset", repo.Config{Class: repo.Virtual, PackageType: repo.Maven}, false},
		{"p2 virtual, unset", repo.Config{Class: repo.Virtual, PackageType: repo.P2}, false},
		{"maven virtual, p2 flag", repo.Config{Class: repo.Virtual, PackageType: repo.Maven, ForceP2Authentication: &on}, false},
		{"p2 virtual, maven flag", repo.Config{Class: repo.Virtual, PackageType: repo.P2, ForceMavenAuthentication: &on}, false},
		{"gradle virtual", repo.Config{Class: repo.Virtual, PackageType: repo.Gradle, ForceMavenAuthentication: &on}, false},
		{"ivy virtual", repo.Config{Class: repo.Virtual, PackageType: repo.Ivy, ForceMavenAuthentication: &on}, false},
		{"sbt virtual", repo.Config{Class: repo.Virtual, PackageType: repo.SBT, ForceMavenAuthentication: &on}, false},
		{"generic virtual", repo.Config{Class: repo.Virtual, PackageType: repo.Generic, ForceMavenAuthentication: &on}, false},
		{"maven local", repo.Config{Class: repo.Local, PackageType: repo.Maven, ForceMavenAuthentication: &on}, false},
		{"p2 remote", repo.Config{Class: repo.Remote, PackageType: repo.P2, ForceP2Authentication: &on}, false},
		{"p2 federated", repo.Config{Class: repo.Federated, PackageType: repo.P2, ForceP2Authentication: &on}, false},
		// NuGet's and Conan's forced checks are their protocols' own.
		{"nuget virtual", repo.Config{Class: repo.Virtual, PackageType: repo.NuGet, ForceNugetAuthentication: &on}, false},
		{"conan virtual", repo.Config{Class: repo.Virtual, PackageType: repo.Conan, ForceConanAuthentication: &on}, false},
	} {
		if got := Forced(tc.c); got != tc.want {
			t.Errorf("%s: Forced %v, want %v", tc.name, got, tc.want)
		}
	}
}

// brokenRepos is a store whose repository reads fail.
type brokenRepos struct{ *inmem.Store }

func (s brokenRepos) Repos() port.RepoStore { return failingRepoStore{s.Store.Repos()} }

type failingRepoStore struct{ port.RepoStore }

func (failingRepoStore) Get(context.Context, string) (repo.Config, error) {
	return repo.Config{}, errBoom
}

// TestUnitForceAuthenticationChallengesBeforeRouting: the middleware
// answers 401 "Unauthorized" with the Basic challenge to a request without
// credentials whose path names a forced repository, whatever the method,
// the query, matrix parameters on the key or an escaped key, and with no
// trailing slash; the handler behind it never runs. Credentials of either
// form, a path it does not class as a repository's, a remote's cache key,
// an unknown or unforced repository and a kit without a store pass the
// request on. A store that fails answers 500.
func TestUnitForceAuthenticationChallengesBeforeRouting(t *testing.T) {
	on := true
	db := inmem.New()
	for _, c := range []repo.Config{
		{Key: "mvn-forced", Class: repo.Virtual, PackageType: repo.Maven, ForceMavenAuthentication: &on},
		{Key: "p2-forced", Class: repo.Virtual, PackageType: repo.P2, ForceP2Authentication: &on},
		{Key: "mvn-open", Class: repo.Virtual, PackageType: repo.Maven},
		{Key: "api", Class: repo.Virtual, PackageType: repo.Maven, ForceMavenAuthentication: &on},
		{Key: "ui", Class: repo.Virtual, PackageType: repo.Maven, ForceMavenAuthentication: &on},
		{Key: "webapp", Class: repo.Virtual, PackageType: repo.Maven, ForceMavenAuthentication: &on},
	} {
		if err := db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	discard := slog.New(slog.DiscardHandler)
	kit := New(Deps{Store: db, Logger: discard})
	serve := func(k *Kit, method, target string, headers ...string) (*httptest.ResponseRecorder, bool) {
		reached := false
		h := k.ForceAuthentication(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reached = true
			w.WriteHeader(http.StatusTeapot)
		}))
		r := httptest.NewRequestWithContext(t.Context(), method, target, nil)
		for i := 0; i+1 < len(headers); i += 2 {
			r.Header.Set(headers[i], headers[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec, reached
	}
	const forcedBody = `{"errors":[{"message":"Unauthorized","status":401}]}` + "\n"
	for _, tc := range []struct{ method, target string }{
		{http.MethodGet, "/artifactory/mvn-forced/org/a/1.0/a-1.0.jar"},
		{http.MethodHead, "/artifactory/mvn-forced/org/a/1.0/a-1.0.jar"},
		{http.MethodPut, "/artifactory/mvn-forced/org/a/1.0/a-1.0.jar"},
		{http.MethodDelete, "/artifactory/mvn-forced/org/a/1.0/a-1.0.jar"},
		{http.MethodPost, "/artifactory/mvn-forced/org/a/1.0/a-1.0.jar"},
		{http.MethodGet, "/artifactory/mvn-forced/org/a/1.0/a-1.0.jar?trace"},
		{http.MethodGet, "/artifactory/mvn-forced"},
		{http.MethodGet, "/artifactory/mvn-forced/"},
		{http.MethodGet, "/artifactory/mvn-forced;k=v/org/a.jar"},
		{http.MethodGet, "/artifactory/mvn%2Dforced/org/a.jar"},
		{http.MethodGet, "/artifactory/p2-forced/p2.index"},
	} {
		rec, reached := serve(kit, tc.method, tc.target)
		if reached || rec.Code != http.StatusUnauthorized ||
			rec.Header().Get("WWW-Authenticate") != apierror.BasicChallenge ||
			rec.Body.String() != forcedBody {
			t.Errorf("%s %s: reached %v, %d %v %q; want the forced 401", tc.method, tc.target,
				reached, rec.Code, rec.Header(), rec.Body)
		}
	}
	for _, tc := range []struct {
		name, target string
		headers      []string
	}{
		{"basic", "/artifactory/mvn-forced/a.jar", []string{"Authorization", "Basic YWRtaW46cHc="}},
		{"bearer", "/artifactory/mvn-forced/a.jar", []string{"Authorization", "Bearer token"}},
		{"reference token", "/artifactory/mvn-forced/a.jar", []string{"X-JFrog-Art-Api", "cmVmdGtu"}},
		{"api", "/artifactory/api/storage/mvn-forced/a.jar", nil},
		{"ui", "/artifactory/ui/a.jar", nil},
		{"webapp", "/artifactory/webapp/a.jar", nil},
		{"no key", "/artifactory/", nil},
		{"outside artifactory", "/v2/mvn-forced/manifests/1", nil},
		{"remote cache key", "/artifactory/mvn-forced-cache/a.jar", nil},
		{"unknown", "/artifactory/missing/a.jar", nil},
		{"unforced", "/artifactory/mvn-open/a.jar", nil},
	} {
		if rec, reached := serve(kit, http.MethodGet, tc.target, tc.headers...); !reached ||
			rec.Code != http.StatusTeapot {
			t.Errorf(
				"%s: reached %v, %d %q; want it passed on",
				tc.name,
				reached,
				rec.Code,
				rec.Body,
			)
		}
	}
	if rec, reached := serve(
		New(Deps{}),
		http.MethodGet,
		"/artifactory/mvn-forced/a.jar",
	); !reached {
		t.Errorf("without a store: %d %q; want it passed on", rec.Code, rec.Body)
	}
	broken := New(Deps{Store: brokenRepos{db}, Logger: discard})
	if rec, reached := serve(broken, http.MethodGet, "/artifactory/mvn-forced/a.jar"); reached ||
		rec.Code != http.StatusInternalServerError {
		t.Errorf("failing store: reached %v, %d %q; want 500", reached, rec.Code, rec.Body)
	}
	// A key that does not decode is passed on, as 7.x's filter, which
	// cannot read a key from it, passes it on; net/http refuses such a
	// request line before any handler, so pathRepoKey is asked directly.
	if key, ok := pathRepoKey("/artifactory/mvn%zzforced/a.jar"); ok {
		t.Errorf("undecodable key: %q reported", key)
	}
}

// TestUnitForceAuthenticationCountsAnonymousCredentialsAsNone: while
// anonymous access is on, Basic credentials whose user name is blank or
// anonymous or that do not decode, and a Bearer token that is blank, does
// not verify or verifies as the anonymous user, count as none on a forced
// repository, as in 7.x's filter (OssForceAuthenticationFilter.
// checkIfAuthenticationShouldBeIgnored, 7.161.26). Another user's Basic
// credentials, a token of another user or whose check fails for another
// reason, another scheme, a reference token, and every credential while
// anonymous access is off pass the request on. An anonymous access setting
// that cannot be read answers 500 to a credential it decides.
func TestUnitForceAuthenticationCountsAnonymousCredentialsAsNone(t *testing.T) {
	on := true
	db := inmem.New()
	forced := repo.Config{Key: "mvn-forced", Class: repo.Virtual, PackageType: repo.Maven}
	forced.ForceMavenAuthentication = &on
	if err := db.Repos().Create(t.Context(), *repo.Defaults(&forced)); err != nil {
		t.Fatal(err)
	}
	anonymous, setting := true, error(nil)
	auth := &accessapitest.Authenticator{
		AnonymousAllowedFunc: func(context.Context) (bool, error) { return anonymous, setting },
		TokenFunc: func(_ context.Context, raw, _ string) (kernel.Principal, error) {
			switch raw {
			case "user-token":
				return kernel.Principal{Name: "deployer"}, nil
			case "anonymous-token":
				return kernel.Anonymous, nil
			case "restricted-token":
				return kernel.Principal{}, kernel.ErrAPIRestricted
			case "unreadable-token":
				return kernel.Principal{}, errBoom
			}
			return kernel.Principal{}, kernel.ErrBadCredentials
		},
	}
	kit := New(Deps{
		Store: db, Logger: slog.New(slog.DiscardHandler),
		Access: accessapi.API{Authenticator: auth},
	})
	const target = "/artifactory/mvn-forced/a.jar"
	serve := func(name, value string) (*httptest.ResponseRecorder, bool) {
		reached := false
		h := kit.ForceAuthentication(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reached = true
			w.WriteHeader(http.StatusTeapot)
		}))
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
		if name != "" {
			r.Header.Set(name, value)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec, reached
	}
	b64 := base64.StdEncoding.EncodeToString
	const forcedBody = `{"errors":[{"message":"Unauthorized","status":401}]}` + "\n"
	none := []string{
		"Basic " + b64([]byte("anonymous:")),
		"Basic " + b64([]byte("Anonymous:secret")),
		"Basic " + b64([]byte(":secret")),
		"Basic " + b64([]byte(" :secret")),
		"Basic " + b64([]byte("nocolon")),
		"Basic Og==",
		"Basic !!!",
		"basic " + b64([]byte("anonymous:x")),
		"Bearer not-a-token",
		"bearer not-a-token",
		"Bearer anonymous-token",
		"Bearer ",
	}
	for _, value := range none {
		rec, reached := serve("Authorization", value)
		if reached || rec.Code != http.StatusUnauthorized || rec.Body.String() != forcedBody {
			t.Errorf("anonymous access on, %q: reached %v, %d %q; want the forced 401",
				value, reached, rec.Code, rec.Body)
		}
	}
	for _, h := range [][2]string{
		{"Authorization", "Basic " + b64([]byte("admin:wrong"))},
		{"Authorization", "Bearer user-token"},
		{"Authorization", "Bearer restricted-token"},
		{"Authorization", "Bearer unreadable-token"},
		{"Authorization", "Token user-token"},
		{"X-JFrog-Art-Api", "cmVmdGtu"},
	} {
		if rec, reached := serve(h[0], h[1]); !reached {
			t.Errorf("anonymous access on, %s %q: %d %q; want it passed on",
				h[0], h[1], rec.Code, rec.Body)
		}
	}
	anonymous = false
	for _, value := range none {
		if rec, reached := serve("Authorization", value); !reached {
			t.Errorf("anonymous access off, %q: %d %q; want it passed on",
				value, rec.Code, rec.Body)
		}
	}
	if rec, reached := serve("", ""); reached || rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous access off, no credentials: reached %v, %d; want the forced 401",
			reached, rec.Code)
	}
	anonymous, setting = true, errBoom
	rec, reached := serve("Authorization", none[0])
	if reached || rec.Code != http.StatusInternalServerError {
		t.Errorf("unreadable setting: reached %v, %d %q; want 500", reached, rec.Code, rec.Body)
	}
	if rec, reached := serve("", ""); reached || rec.Code != http.StatusUnauthorized {
		t.Errorf("unreadable setting, no credentials: reached %v, %d; want the forced 401",
			reached, rec.Code)
	}
}
