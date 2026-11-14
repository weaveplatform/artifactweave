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
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
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

// TestUnitForceAuthenticationCountsAnonymousCredentialsAsNone: on a forced
// repository the middleware tells credentials apart as 7.x's filter does
// (OssForceAuthenticationFilter.acceptFilter and
// checkIfAuthenticationShouldBeIgnored, 7.161.26). While anonymous access
// is on, Basic credentials whose user name, decoded as Commons Codec
// decodes it, is blank or anonymous, and a Bearer token that does not
// verify or verifies as the anonymous user, count as none. The filter's
// other credentials pass the request on to authentication: another user's
// Basic credentials, an unpadded value naming one included; "Basic Og==",
// a blank Basic or Bearer value, a lowercase "basic" and an empty
// Authorization header, which the filter does not class as Basic or
// Bearer; a token of another user or whose check fails for another reason;
// a non-blank X-JFrog-Art-Api, an X-Api-Key header and a non-blank
// RT_IDN_TOKEN cookie. A blank X-JFrog-Art-Api, a blank RT_IDN_TOKEN and
// another cookie are none. While anonymous access is off every credential
// passes on. An anonymous access setting that cannot be read answers 500 to
// a credential it decides.
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
	// serve sends headers, name and value pairs, where the name Cookie
	// adds a cookie of the form name=value.
	serve := func(headers ...string) (*httptest.ResponseRecorder, bool) {
		reached := false
		h := kit.ForceAuthentication(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reached = true
			w.WriteHeader(http.StatusTeapot)
		}))
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
		for i := 0; i+1 < len(headers); i += 2 {
			r.Header.Add(headers[i], headers[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec, reached
	}
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	raw := func(s string) string { return base64.RawStdEncoding.EncodeToString([]byte(s)) }
	urlSafe := func(s string) string { return base64.URLEncoding.EncodeToString([]byte(s)) }
	const forcedBody = `{"errors":[{"message":"Unauthorized","status":401}]}` + "\n"
	none := [][]string{
		{"Authorization", "Basic " + b64("anonymous:")},
		{"Authorization", "Basic " + b64("Anonymous:secret")},
		{"Authorization", "Basic " + raw("anonymous:x")},
		{"Authorization", "Basic " + raw("anonymous:xy") + "Q"},
		{"Authorization", "Basic " + urlSafe("anonymous:xx\xfb\xff\xff")},
		{"Authorization", "Basic " + b64(":secret")},
		{"Authorization", "Basic " + b64(" \u2028\x1c:secret")},
		{"Authorization", "Basic " + b64("nocolon")},
		{"Authorization", "Basic !!!"},
		{"Authorization", "Bearer not-a-token"},
		{"Authorization", "bearer not-a-token"},
		{"Authorization", "Bearer anonymous-token"},
	}
	passed := [][]string{
		{"Authorization", "Basic " + b64("admin:wrong")},
		{"Authorization", "Basic " + raw("admin:wrong")},
		{"Authorization", "Basic " + b64("\u00a0:secret")},
		{"Authorization", "Basic Og=="},
		{"Authorization", "Basic  "},
		{"Authorization", "basic " + b64("anonymous:x")},
		{"Authorization", "Bearer  "},
		{"Authorization", ""},
		{"Authorization", "Bearer user-token"},
		{"Authorization", "Bearer restricted-token"},
		{"Authorization", "Bearer unreadable-token"},
		{"Authorization", "Token user-token"},
		{"X-JFrog-Art-Api", "cmVmdGtu"},
		{"X-JFrog-Art-Api", "", "X-Api-Key", ""},
		{"Cookie", "RT_IDN_TOKEN=idn"},
	}
	noCredentials := [][]string{
		nil,
		{"X-JFrog-Art-Api", " "},
		{"Cookie", "RT_IDN_TOKEN="},
		{"Cookie", "session=s"},
	}
	wantForced := func(what string, headers []string) {
		t.Helper()
		rec, reached := serve(headers...)
		if reached || rec.Code != http.StatusUnauthorized || rec.Body.String() != forcedBody {
			t.Errorf("%s, %q: reached %v, %d %q; want the forced 401",
				what, headers, reached, rec.Code, rec.Body)
		}
	}
	wantPassed := func(what string, headers []string) {
		t.Helper()
		if rec, reached := serve(headers...); !reached {
			t.Errorf("%s, %q: %d %q; want it passed on", what, headers, rec.Code, rec.Body)
		}
	}
	for _, h := range append(none, noCredentials...) {
		wantForced("anonymous access on", h)
	}
	for _, h := range passed {
		wantPassed("anonymous access on", h)
	}
	anonymous = false
	for _, h := range append(none, passed...) {
		wantPassed("anonymous access off", h)
	}
	for _, h := range noCredentials {
		wantForced("anonymous access off", h)
	}
	anonymous, setting = true, errBoom
	if rec, reached := serve(none[0]...); reached || rec.Code != http.StatusInternalServerError {
		t.Errorf("unreadable setting: reached %v, %d %q; want 500", reached, rec.Code, rec.Body)
	}
	wantForced("unreadable setting", nil)
}

// aliasStore is an in-memory store whose remote cache lists, for an exact
// path, the pages pages holds by the token that asks for them.
type aliasStore struct {
	*inmem.Store
	pages map[string]aliasPage
}

type aliasPage struct {
	entries []model.RemoteCacheEntry
	next    string
	err     error
}

func (s aliasStore) RemoteCache() port.RemoteCacheStore {
	return aliasCache{RemoteCacheStore: s.Store.RemoteCache(), pages: s.pages}
}

type aliasCache struct {
	port.RemoteCacheStore
	pages map[string]aliasPage
}

func (c aliasCache) Exact(
	_ context.Context, _, _ string, p kernel.Page,
) ([]model.RemoteCacheEntry, string, error) {
	page := c.pages[p.After]
	return page.entries, page.next, page.err
}

// TestUnitAliasRemoteMetadata (#1382): a remote's -cache alias reads a
// path through the remote source only when it is a Debian or RPM remote's
// metadata and the cache holds a complete document for it without an
// item; anything else is left to the item lookup, which answers as it did.
// A store that cannot list the cache fails the read.
func TestUnitAliasRemoteMetadata(t *testing.T) {
	debianRemote := repo.Config{Key: "deb", Class: repo.Remote, PackageType: repo.Debian}
	rpmRemote := repo.Config{Key: "rpm", Class: repo.Remote, PackageType: repo.RPM}
	document := model.RemoteCacheEntry{Blob: blob.Blob{Checksums: kernel.Checksums{
		SHA256: "0000000000000000000000000000000000000000000000000000000000000000",
	}}}
	backed := document
	backed.ItemID = 7
	held := map[string]aliasPage{"": {entries: []model.RemoteCacheEntry{document}}}
	fault := errors.New("store fault")
	for _, tc := range []struct {
		name    string
		c       repo.Config
		cached  bool
		path    string
		pages   map[string]aliasPage
		want    bool
		wantErr bool
	}{
		{
			name: "a held Release", c: debianRemote, cached: true, path: "dists/stable/Release",
			pages: held, want: true,
		},
		{
			name: "a held component file", c: debianRemote, cached: true,
			path: "dists/stable/main/binary-amd64/Release", pages: held, want: true,
		},
		{
			name: "held repodata", c: rpmRemote, cached: true, path: "repodata/repomd.xml",
			pages: held, want: true,
		},
		{
			name: "on a later page", c: debianRemote, cached: true, path: "dists/stable/InRelease",
			pages: map[string]aliasPage{
				"":      {entries: []model.RemoteCacheEntry{backed}, next: "later"},
				"later": {entries: []model.RemoteCacheEntry{document}},
			}, want: true,
		},
		{name: "nothing held", c: debianRemote, cached: true, path: "dists/stable/Release"},
		{
			name: "only an item-backed entry", c: debianRemote, cached: true,
			path:  "dists/stable/Release",
			pages: map[string]aliasPage{"": {entries: []model.RemoteCacheEntry{backed}}},
		},
		{name: "the remote itself", c: debianRemote, path: "dists/stable/Release", pages: held},
		{
			name: "a local repository", cached: true, path: "dists/stable/Release", pages: held,
			c: repo.Config{Key: "deb-local", Class: repo.Local, PackageType: repo.Debian},
		},
		{name: "the repository root", c: debianRemote, cached: true, pages: held},
		{
			name: "a checksum file", c: debianRemote, cached: true,
			path: "dists/stable/Release.sha256", pages: held,
		},
		{
			name: "another package type", cached: true, path: "repodata/repomd.xml", pages: held,
			c: repo.Config{Key: "gen", Class: repo.Remote, PackageType: repo.Generic},
		},
		{
			name: "a package", c: debianRemote, cached: true,
			path: "pool/main/a/alpha/alpha_1.0_amd64.deb", pages: held,
		},
		{
			name: "a path the source refuses", c: debianRemote, cached: true,
			path: "dists/stable/main/binary-amd64/Packagesx", pages: held,
		},
		{
			name: "a listing fault", c: debianRemote, cached: true, path: "dists/stable/Release",
			pages: map[string]aliasPage{"": {err: fault}}, wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := aliasStore{Store: inmem.New(), pages: tc.pages}
			got, err := aliasRemoteMetadata(t.Context(), st, tc.c, tc.cached, tc.path)
			if got != tc.want || (err != nil) != tc.wantErr ||
				tc.wantErr && !errors.Is(err, fault) {
				t.Fatalf("got %t, %v; want %t, error %t", got, err, tc.want, tc.wantErr)
			}
		})
	}
}
