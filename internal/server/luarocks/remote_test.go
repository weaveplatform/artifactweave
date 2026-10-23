package luarocks_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/server/luarocks"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const remoteBase = "/artifactory/api/luarocks/rocks/"

type remoteFixture struct {
	*fixture
	up    *fakeupstream.Server
	clock *fakeupstream.Clock
}

func newRemoteFixture(t *testing.T) *remoteFixture {
	t.Helper()
	up := fakeupstream.New(t, fakeupstream.Options{})
	clock := fakeupstream.NewClock(time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC))
	f := newFixtureWithProtocol(t, repo.Remote, func(items *item.Service) luarocks.Options {
		c, err := items.Store.Repos().Get(t.Context(), "rocks")
		if err != nil {
			t.Fatal(err)
		}
		c.URL = up.URL + "/rocks/"
		ttl := 10
		c.RetrievalCachePeriodSecs = &ttl
		if err = items.Store.Repos().Update(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		if err = items.Store.Permissions().Create(t.Context(), types.Permission{
			Name: "visible-only",
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{"partial": types.ActionRead}},
				Targets: map[string]types.Target{"rocks": {Include: []string{"visible-*"}}},
			}},
		}); err != nil {
			t.Fatal(err)
		}
		key := []byte(strings.Repeat("k", 32))
		resolver, err := remote.NewResolver(
			items.Store,
			nil,
			remote.ResolverOptions{RevisionKey: key},
		)
		if err != nil {
			t.Fatal(err)
		}
		policy := &remote.Policy{
			Store: items.Store, Blobs: items.Blobs, Items: items,
			Pool: new(remote.Pool), Options: resolver.Options, Key: key, Now: clock.Now,
		}
		t.Cleanup(policy.Pool.Close)
		svc := &source.Service{
			HTTP: policy, Cache: policy,
			Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
				return items.Store.RemoteCache().Source(ctx, c.Key)
			},
		}
		return luarocks.Options{RemoteSources: svc}
	})
	return &remoteFixture{fixture: f, up: up, clock: clock}
}

func (f *remoteFixture) seed() {
	f.up.Set("/rocks/manifest", fakeupstream.Resource{Body: []byte(`commands = {}
modules = {}
repository = { ["visible"] = { ["1.0-1"] = { { arch = "rockspec" }, { arch = "src" } } },
["hidden"] = { ["1.0-1"] = { { arch = "rockspec" } } } }
`)})
	f.up.Set("/rocks/manifest-5.4", fakeupstream.Resource{Body: []byte(`commands = {}
modules = {}
repository = { ["visible"] = { ["1.0-1"] = { { arch = "rockspec" }, { arch = "src" } } },
["hidden"] = { ["1.0-1"] = { { arch = "rockspec" } } } }
`)})
	f.up.Set("/rocks/visible-1.0-1.rockspec", fakeupstream.Resource{Body: spec("visible", "1.0-1")})
	f.up.Set(
		"/rocks/visible-1.0-1.src.rock",
		fakeupstream.Resource{Body: rock(f.t, "visible", "1.0-1")},
	)
	f.up.Set("/rocks/hidden-1.0-1.rockspec", fakeupstream.Resource{Body: spec("hidden", "1.0-1")})
}

func TestRemoteManifestFiltersAndCachesNativeFiles(t *testing.T) {
	f := newRemoteFixture(t)
	f.seed()
	partial := f.do(http.MethodGet, remoteBase+"manifest-5.4", nil, "", "partial")
	if partial.Code != 200 || !strings.Contains(partial.Body.String(), "visible") ||
		strings.Contains(partial.Body.String(), "hidden") {
		t.Fatalf("partial manifest: %d %s", partial.Code, partial.Body)
	}
	if rec := f.do(
		http.MethodGet,
		remoteBase+"hidden-1.0-1.rockspec",
		nil,
		"",
		"partial",
	); rec.Code != 403 {
		t.Fatalf("narrow read: %d %s", rec.Code, rec.Body)
	}
	if got := f.up.Count(http.MethodGet, "/rocks/hidden-1.0-1.rockspec"); got != 0 {
		t.Fatalf("denied file reached upstream: %d", got)
	}
	for _, path := range []string{"manifest", "visible-1.0-1.rockspec", "visible-1.0-1.src.rock"} {
		rec := f.do(http.MethodGet, remoteBase+path, nil, "", "admin")
		if rec.Code != 200 || rec.Body.Len() == 0 {
			t.Fatalf("cold %s: %d %s", path, rec.Code, rec.Body)
		}
	}
	before := f.up.Count(http.MethodGet, "")
	f.up.Delete("/rocks/visible-1.0-1.rockspec")
	f.up.Delete("/rocks/visible-1.0-1.src.rock")
	c, err := f.db.Repos().Get(t.Context(), "rocks")
	if err != nil {
		t.Fatal(err)
	}
	offline := true
	c.Offline = &offline
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"visible-1.0-1.rockspec", "visible-1.0-1.src.rock"} {
		rec := f.do(http.MethodGet, remoteBase+path, nil, "", "admin")
		if rec.Code != 200 || rec.Body.Len() == 0 {
			t.Fatalf("offline %s: %d %s", path, rec.Code, rec.Body)
		}
	}
	if after := f.up.Count(http.MethodGet, ""); after != before {
		t.Fatalf("offline contacted upstream: %d -> %d", before, after)
	}
}

func TestRemoteRejectsInvalidManifestAndRock(t *testing.T) {
	f := newRemoteFixture(t)
	f.seed()
	f.up.Set(
		"/rocks/manifest",
		fakeupstream.Resource{Body: []byte(`repository = os.execute("bad")`)},
	)
	if rec := f.do(http.MethodGet, remoteBase+"manifest", nil, "", "admin"); rec.Code != 502 {
		t.Fatalf("invalid manifest: %d %s", rec.Code, rec.Body)
	}
	f.seed()
	f.up.Set("/rocks/visible-1.0-1.src.rock", fakeupstream.Resource{Body: []byte("bad archive")})
	if rec := f.do(
		http.MethodGet,
		remoteBase+"visible-1.0-1.src.rock",
		nil,
		"",
		"admin",
	); rec.Code != 502 {
		t.Fatalf("invalid rock: %d %s", rec.Code, rec.Body)
	}
}

func TestRemoteManifestRefreshAndZapDoNotEagerlyFetchRocks(t *testing.T) {
	f := newRemoteFixture(t)
	f.seed()
	get := func() *httptest.ResponseRecorder {
		t.Helper()
		return f.do(http.MethodGet, remoteBase+"manifest", nil, "", "admin")
	}
	if rec := get(); rec.Code != 200 || strings.Contains(rec.Body.String(), "2.0-1") {
		t.Fatalf("original manifest: %d %s", rec.Code, rec.Body)
	}
	updated := `commands = {}
modules = {}
repository = { ["visible"] = { ["1.0-1"] = { { arch = "rockspec" } },
["2.0-1"] = { { arch = "rockspec" } } } }
`
	f.up.Set("/rocks/manifest", fakeupstream.Resource{Body: []byte(updated)})
	if rec := get(); rec.Code != 200 {
		t.Fatalf("manifest refresh: %d %s", rec.Code, rec.Body)
	}
	f.clock.Advance(11 * time.Second)
	if rec := get(); rec.Code != 200 || !strings.Contains(rec.Body.String(), "2.0-1") {
		t.Fatalf("refreshed manifest: %d %s", rec.Code, rec.Body)
	}
	if got := f.up.Count(http.MethodGet, "/rocks/visible-2.0-1.rockspec"); got != 0 {
		t.Fatalf("eagerly fetched new rockspec: %d", got)
	}
	f.up.Set("/rocks/manifest", fakeupstream.Resource{Body: []byte(fixtureManifestWithoutHidden())})
	c, err := f.db.Repos().Get(t.Context(), "rocks")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.items.Zap(t.Context(), item.Target{Repo: c, Cached: true}); err != nil {
		t.Fatal(err)
	}
	if rec := get(); rec.Code != 200 || strings.Contains(rec.Body.String(), "2.0-1") {
		t.Fatalf("zapped manifest: %d %s", rec.Code, rec.Body)
	}
	if got := f.up.Count(http.MethodGet, "/rocks/manifest"); got != 3 {
		t.Fatalf("manifest upstream fetches %d, want 3", got)
	}
}

func fixtureManifestWithoutHidden() string {
	return `commands = {}
modules = {}
repository = { ["visible"] = { ["1.0-1"] = { { arch = "rockspec" }, { arch = "src" } } } }
`
}

func TestRemoteRejectsUnlistedAndBrokenResponses(t *testing.T) {
	for name, tc := range map[string]struct {
		change            func(*remoteFixture)
		wantStatus        int
		wantHead, wantGet int
	}{
		"upstream denied": {
			change: func(f *remoteFixture) {
				f.up.Set("/rocks/manifest", fakeupstream.Resource{Status: http.StatusUnauthorized})
			},
			wantStatus: http.StatusUnauthorized,
			wantHead:   1,
		},
		"upstream missing": {
			change:     func(f *remoteFixture) { f.up.Delete("/rocks/manifest") },
			wantStatus: http.StatusNotFound,
			wantHead:   1,
		},
		"redirect": {
			change: func(f *remoteFixture) {
				f.up.Set("/rocks/manifest", fakeupstream.Resource{
					Redirect: "http://example.invalid/manifest",
				})
			},
			wantStatus: http.StatusBadGateway,
			wantHead:   1,
		},
		"truncated manifest": {
			change: func(f *remoteFixture) {
				f.up.Set("/rocks/manifest", fakeupstream.Resource{
					Body:  []byte(fixtureManifestWithoutHidden()),
					Fault: fakeupstream.Fault{TruncateAfter: 10},
				})
			},
			wantStatus: http.StatusBadGateway,
			wantHead:   1,
			wantGet:    1,
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newRemoteFixture(t)
			f.seed()
			tc.change(f)
			if rec := f.do(
				http.MethodGet,
				remoteBase+"manifest",
				nil,
				"",
				"admin",
			); rec.Code != tc.wantStatus {
				t.Fatalf("bad upstream %d %s", rec.Code, rec.Body)
			}
			head := f.up.Count(http.MethodHead, "/rocks/manifest")
			get := f.up.Count(http.MethodGet, "/rocks/manifest")
			if head != tc.wantHead || get != tc.wantGet {
				t.Fatalf("upstream HEAD=%d GET=%d, want HEAD=%d GET=%d",
					head, get, tc.wantHead, tc.wantGet)
			}
		})
	}
	f := newRemoteFixture(t)
	f.seed()
	f.up.Set(
		"/rocks/unlisted-1.0-1.rockspec",
		fakeupstream.Resource{Body: spec("unlisted", "1.0-1")},
	)
	if rec := f.do(
		http.MethodGet,
		remoteBase+"unlisted-1.0-1.rockspec",
		nil,
		"",
		"admin",
	); rec.Code != 404 {
		t.Fatalf("unlisted rock: %d %s", rec.Code, rec.Body)
	}
	if got := f.up.Count(http.MethodGet, "/rocks/unlisted-1.0-1.rockspec"); got != 0 {
		t.Fatalf("fetched unlisted content: %d", got)
	}
	f.up.Delete("/rocks/visible-1.0-1.rockspec")
	if rec := f.do(
		http.MethodGet,
		remoteBase+"visible-1.0-1.rockspec",
		nil,
		"",
		"admin",
	); rec.Code != http.StatusNotFound {
		t.Fatalf("missing listed rock: %d %s", rec.Code, rec.Body)
	}
	if head, get := f.up.Count(http.MethodHead, "/rocks/visible-1.0-1.rockspec"),
		f.up.Count(http.MethodGet, "/rocks/visible-1.0-1.rockspec"); head != 1 || get != 0 {
		t.Fatalf("missing rock HEAD=%d GET=%d, want 1/0", head, get)
	}
}
