package bower

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type remoteFixture struct {
	*fixture
	up *fakeupstream.Server
}

func newRemoteFixture(t *testing.T, git bool) *remoteFixture {
	t.Helper()
	f := newFixture(t)
	up := fakeupstream.New(t, fakeupstream.Options{})
	c := f.repo("bower-remote", repo.Remote)
	if git {
		c.URL = up.URL + "/git"
		c.BowerRegistryURL = up.URL + "/registry"
		c.VCSGitProvider = repo.GitHubEnterprise
	} else {
		c.URL = up.URL + "/api/bower/source"
		c.VCSGitProvider = repo.ArtifactoryVCS
	}
	ttl := 5
	c.RetrievalCachePeriodSecs = &ttl
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	key := []byte(strings.Repeat("k", 32))
	resolver, err := remote.NewResolver(f.db, nil, remote.ResolverOptions{RevisionKey: key})
	if err != nil {
		t.Fatal(err)
	}
	clock := fakeupstream.NewClock(time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC))
	policy := &remote.Policy{
		Store: f.db, Blobs: f.items.Blobs, Items: f.items,
		Pool: new(remote.Pool), Options: resolver.Options, Key: key, Now: clock.Now,
	}
	t.Cleanup(policy.Pool.Close)
	f.mount(&source.Service{
		HTTP: policy, Cache: policy,
		Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
			return f.db.RemoteCache().Source(ctx, c.Key)
		},
	})
	return &remoteFixture{fixture: f, up: up}
}

func (f *remoteFixture) configure(change func(*repo.Config)) {
	f.t.Helper()
	c, err := f.db.Repos().Get(f.t.Context(), "bower-remote")
	if err != nil {
		f.t.Fatal(err)
	}
	change(&c)
	if err := f.db.Repos().Update(f.t.Context(), c); err != nil {
		f.t.Fatal(err)
	}
}

func TestUnitBowerArtifactoryRemoteCatalogAndArchiveAdmission(t *testing.T) {
	f := newRemoteFixture(t, false)
	base := "/artifactory/api/bower/bower-remote/"
	refs := strings.Repeat("a", 44) + " refs/tags/v1.0.0\n"
	f.up.Set(
		"/api/bower/source/packages",
		fakeupstream.Resource{Body: []byte(`[{"name":"hello","url":"art://source/hello"}]`)},
	)
	f.up.Set("/api/bower/source/refs/bower-remote/hello", fakeupstream.Resource{Body: []byte(refs)})
	f.up.Set("/api/bower/source/binaries/bower-remote/hello.git/v1.0.0", fakeupstream.Resource{
		Body: bowerArchive(
			t,
			[]archiveEntry{{"hello-1.0.0/bower.json", `{"name":"hello","version":"1.0.0"}`, 0}},
		),
	})
	for _, path := range []string{"packages", "packages/hello", "refs/bower-remote/hello", "binaries/bower-remote/hello.git/v1.0.0"} {
		r := f.get(base+path, "admin")
		if r.Code != http.StatusOK {
			t.Fatalf("remote %s: %d %s", path, r.Code, r.Body)
		}
	}
	if r := f.get(
		base+"packages/search/hello",
		"admin",
	); r.Code != http.StatusOK ||
		!strings.Contains(r.Body.String(), `"hello"`) {
		t.Fatalf("remote search: %d %s", r.Code, r.Body)
	}
	f.up.Set(
		"/api/bower/source/packages",
		fakeupstream.Resource{
			Body: []byte(`[{"name":"hello","url":"https://evil.example/repo.git"}]`),
		},
	)
	f.configure(func(c *repo.Config) { zero := 0; c.RetrievalCachePeriodSecs = &zero })
	if r := f.get(base+"packages", "admin"); r.Code == http.StatusOK {
		t.Fatal("invalid upstream catalog was served")
	}
	f.up.Set(
		"/api/bower/source/refs/bower-remote/hello",
		fakeupstream.Resource{Body: []byte("bad refs")},
	)
	if r := f.get(base+"refs/bower-remote/hello", "admin"); r.Code == http.StatusOK {
		t.Fatal("invalid upstream refs were served")
	}
	f.up.Set("/api/bower/source/refs/bower-remote/hello", fakeupstream.Resource{Body: []byte(refs)})
	f.up.Set(
		"/api/bower/source/binaries/bower-remote/hello.git/v1.0.0",
		fakeupstream.Resource{Body: []byte("not gzip")},
	)
	if r := f.get(base+"binaries/bower-remote/hello.git/v1.0.0", "admin"); r.Code == http.StatusOK {
		t.Fatal("invalid upstream binary was served")
	}
	f.up.Delete("/api/bower/source/refs/bower-remote/hello")
	if r := f.get(base+"refs/bower-remote/hello", "admin"); r.Code == http.StatusOK {
		t.Fatal("missing upstream refs were served from stale cache")
	}
}

func TestUnitBowerGitRemoteRegistryRedirectAndSourcePolicy(t *testing.T) {
	f := newRemoteFixture(t, true)
	base := "/artifactory/api/bower/bower-remote/"
	pointer := `{"name":"hello","url":"` + f.up.URL + `/git/team/hello.git"}`
	f.up.Set("/registry/packages", fakeupstream.Resource{Body: []byte("[" + pointer + "]")})
	f.up.Set(
		"/registry/packages/search/hello",
		fakeupstream.Resource{Body: []byte("[" + pointer + "]")},
	)
	f.up.Set("/registry/packages/hello", fakeupstream.Resource{Body: []byte(pointer)})
	sha := strings.Repeat("a", 40)
	f.up.Set("/git/team/hello.git/info/refs", fakeupstream.Resource{Body: []byte(
		gitPkt("# service=git-upload-pack\n") + "0000" + gitPkt(sha+" refs/tags/v1.0.0\n") + "0000",
	)})
	f.up.Set("/git/team/hello/archive/refs/tags/v1.0.0.tar.gz", fakeupstream.Resource{
		Body: bowerArchive(t, []archiveEntry{{"hello-1.0.0/bower.json", `{"name":"hello"}`, 0}}),
	})
	for _, path := range []string{"packages", "packages/search/hello", "packages/hello", "refs/bower-remote/hello", "binaries/bower-remote/hello.git/v1.0.0"} {
		r := f.get(base+path, "admin")
		if r.Code != http.StatusOK {
			t.Fatalf("Git remote %s: %d %s", path, r.Code, r.Body)
		}
	}
	f.up.Set(
		"/registry/packages/hello",
		fakeupstream.Resource{Redirect: "https://evil.example/steal"},
	)
	f.configure(func(c *repo.Config) { zero := 0; c.RetrievalCachePeriodSecs = &zero })
	if r := f.get(base+"refs/bower-remote/hello", "admin"); r.Code == http.StatusOK {
		t.Fatal("registry redirect escaped configured source")
	}
	if got := f.up.Count(http.MethodGet, "/git/team/hello.git/info/refs"); got == 0 {
		t.Fatal("Git refs were not reached in the valid case")
	}
}

func TestUnitBowerGitRemoteRejectsInvalidPointersRefsAndArchives(t *testing.T) {
	f := newRemoteFixture(t, true)
	base := "/artifactory/api/bower/bower-remote/"
	pointer := `{"name":"hello","url":"` + f.up.URL + `/git/team/hello.git"}`
	validRefs := gitPkt(strings.Repeat("a", 40) + " refs/tags/v1.0.0\n")
	setPointer := func(body string) {
		f.up.Set("/registry/packages/hello", fakeupstream.Resource{Body: []byte(body)})
	}
	setRefs := func(body string) {
		f.up.Set("/git/team/hello.git/info/refs", fakeupstream.Resource{Body: []byte(body)})
	}
	f.configure(func(c *repo.Config) { zero := 0; c.RetrievalCachePeriodSecs = &zero })
	setPointer(`{"name":"hello","url":"https://evil.example/repo.git"}`)
	if r := f.get(base+"binaries/bower-remote/hello.git/v1.0.0", "admin"); r.Code == http.StatusOK {
		t.Fatal("binary fetched from unapproved Git pointer")
	}
	setPointer(pointer)
	setRefs("not packet lines")
	if r := f.get(base+"binaries/bower-remote/hello.git/v1.0.0", "admin"); r.Code == http.StatusOK {
		t.Fatal("malformed smart refs admitted a binary")
	}
	setRefs(validRefs)
	if r := f.get(
		base+"binaries/bower-remote/hello.git/v2.0.0",
		"admin",
	); r.Code != http.StatusNotFound {
		t.Fatalf("missing Git tag returned %d %s", r.Code, r.Body)
	}
	f.configure(func(c *repo.Config) { c.VCSGitProvider = repo.CustomVCS })
	if r := f.get(base+"binaries/bower-remote/hello.git/v1.0.0", "admin"); r.Code == http.StatusOK {
		t.Fatal("unsupported Git archive provider was served")
	}
	f.configure(func(c *repo.Config) { c.VCSGitProvider = repo.GitHubEnterprise })
	f.up.Set(
		"/git/team/hello/archive/refs/tags/v1.0.0.tar.gz",
		fakeupstream.Resource{Body: []byte("not a gzip archive")},
	)
	if r := f.get(base+"binaries/bower-remote/hello.git/v1.0.0", "admin"); r.Code == http.StatusOK {
		t.Fatal("invalid Git archive was cached or served")
	}
	denyAuth := true
	f.configure(func(c *repo.Config) { c.AllowAnyHostAuth = &denyAuth })
	for _, path := range []string{"packages", "packages/hello", "refs/bower-remote/hello"} {
		if r := f.get(base+path, "admin"); r.Code == http.StatusOK {
			t.Fatalf("unsafe credential scope served %s", path)
		}
	}
}

func TestUnitBowerGitCatalogRejectsBadUpstreamResponses(t *testing.T) {
	f := newRemoteFixture(t, true)
	base := "/artifactory/api/bower/bower-remote/"
	f.configure(func(c *repo.Config) { zero := 0; c.RetrievalCachePeriodSecs = &zero })
	for _, body := range []string{
		`not json`, `[{"name":"../escape","url":"https://evil.example/repo.git"}]`,
		`[{"name":"same","url":"https://evil.example/repo.git"},{"name":"same","url":"https://evil.example/other.git"}]`,
	} {
		f.up.Set("/registry/packages", fakeupstream.Resource{Body: []byte(body)})
		if r := f.get(base+"packages", "admin"); r.Code == http.StatusOK {
			t.Fatalf("malformed Git catalog was served: %q", body)
		}
	}
	f.up.Delete("/registry/packages")
	if r := f.get(base+"packages", "admin"); r.Code != http.StatusNotFound {
		t.Fatalf("missing Git catalog = %d %s", r.Code, r.Body)
	}
	f.configure(func(c *repo.Config) { c.BowerRegistryURL = "ssh://untrusted.example/registry" })
	if r := f.get(base+"packages", "admin"); r.Code == http.StatusOK {
		t.Fatal("unsupported registry transport was served")
	}
	if r := f.get(base+"refs/bower-remote/hello", "admin"); r.Code == http.StatusOK {
		t.Fatal("unsupported registry transport was used for package pointer")
	}
}

func TestUnitBowerGitCatalogFiltersNamesBeforeFetchingRefs(t *testing.T) {
	f := newRemoteFixture(t, true)
	base := "/artifactory/api/bower/bower-remote/"
	f.up.Set("/registry/packages", fakeupstream.Resource{Body: []byte(`[
		{"name":"visible","url":"` + f.up.URL + `/git/team/visible.git"},
		{"name":"secret","url":"` + f.up.URL + `/git/team/secret.git"}
	]`)})
	if err := f.db.Permissions().Create(t.Context(), types.Permission{
		Name: "visible-bower",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"limited": types.ActionRead}},
			Targets: map[string]types.Target{"bower-remote": {Include: []string{"visible/**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	r := f.get(base+"packages", "limited")
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"visible"`) ||
		strings.Contains(r.Body.String(), `"secret"`) {
		t.Fatalf("scoped Git catalog = %d %s", r.Code, r.Body)
	}
	if r := f.get(base+"refs/bower-remote/secret", "limited"); r.Code == http.StatusOK {
		t.Fatal("limited reader accessed secret refs")
	}
	if got := f.up.Count(http.MethodGet, "/registry/packages/secret"); got != 0 {
		t.Fatalf("denied package reached registry: %d", got)
	}
}
