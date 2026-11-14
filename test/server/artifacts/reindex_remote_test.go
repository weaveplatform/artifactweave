package artifacts_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func TestAccRemoteReindexRechecksCurrentAuthority(t *testing.T) {
	for _, mutation := range []string{"permission", "password", "disabled"} {
		t.Run(mutation, func(t *testing.T) {
			w := newItemWorld(t, false, func(c *Config) {
				st := c.Store
				c.Store = &reindexSourceStore{Store: st, before: func(ctx context.Context) {
					if mutation == "permission" {
						if err := st.Permissions().Delete(ctx, "manage-remote"); err != nil {
							t.Fatal(err)
						}
						return
					}
					u, err := st.Users().Get(ctx, "deployer")
					if err != nil {
						t.Fatal(err)
					}
					if mutation == "password" {
						u.PasswordHash, err = authn.HashPasswordWith(testHash, "changed")
						if err != nil {
							t.Fatal(err)
						}
					} else {
						u.Status = accesstest.UserDisabled
					}
					if err = st.Users().Update(ctx, u); err != nil {
						t.Fatal(err)
					}
				}}
			})
			createRemoteReindexGrant(t, w, "remote")
			got := w.do("POST", "/artifactory/api/nuget/remote-cache/reindex", "deployer", "")
			wantStatus(t, got, http.StatusForbidden)
			if _, err := w.db.WorkQueue().
				Get(t.Context(), format.KindReindex, "remote"); !errors.Is(
				err,
				kernel.ErrNotFound,
			) {
				t.Fatal("revoked caller queued work", err)
			}
		})
	}
}

func TestAccRemoteReindexCacheAliasDoesNotGrantManage(t *testing.T) {
	w := newItemWorld(t, false)
	createRemoteReindexGrant(t, w, "remote-cache")
	for _, key := range []string{"remote", "remote-cache"} {
		wantStatus(
			t,
			w.do("POST", "/artifactory/api/nuget/"+key+"/reindex", "deployer", ""),
			http.StatusForbidden,
		)
	}
	if _, err := w.db.WorkQueue().
		Get(t.Context(), format.KindReindex, "remote"); !errors.Is(
		err,
		kernel.ErrNotFound,
	) {
		t.Fatal("alias-only grant queued work", err)
	}
}

func createRemoteReindexGrant(t *testing.T, w *itemWorld, target string) {
	t.Helper()
	c := *repo.Defaults(&repo.Config{Key: "remote", Class: repo.Remote, PackageType: repo.NuGet, URL: "https://up.example"})
	if err := w.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Permissions().
		Create(t.Context(), accesstest.Permission{Name: "manage-remote", Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]api.Actions{"deployer": api.ActionManage}}, Targets: map[string]accesstest.Target{target: {Include: []string{"**"}}}}}}); err != nil {
		t.Fatal(err)
	}
}

type reindexSourceStore struct {
	harness.Store
	before func(context.Context)
	once   sync.Once
}

func (s *reindexSourceStore) RemoteCache() port.RemoteCacheStore {
	return reindexSourceCache{RemoteCacheStore: s.Store.RemoteCache(), s: s}
}

type reindexSourceCache struct {
	port.RemoteCacheStore
	s *reindexSourceStore
}

func (c reindexSourceCache) Source(
	ctx context.Context,
	key string,
) (artmodel.SourceIdentity, error) {
	c.s.once.Do(func() { c.s.before(ctx) })
	return c.RemoteCacheStore.Source(ctx, key) //nolint:wrapcheck // authority-change barrier
}

// A remote Cargo repository's reindex, named by its key or its cache's,
// needs an administrator and expires the cached upstream config.json, which
// 7.x re-creates (RTFACT-29403), whatever async says. The cached index file
// keeps its validation [I]. A remote Pub repository's reindex answers 501:
// no source says what 7.x rebuilds there.
func TestAccCargoAndPubRemoteReindex(t *testing.T) {
	ctx := context.Background()
	w, _ := reindexWorld(t, ecosystem.For, true)
	remotes := map[repo.PackageType]repo.Config{}
	targets := map[string]accesstest.Target{}
	for _, pt := range []repo.PackageType{repo.Cargo, repo.Pub} {
		c := *repo.Defaults(&repo.Config{
			Key: string(pt) + "-remote", Class: repo.Remote, PackageType: pt,
			URL: "https://up.example/" + string(pt) + "/",
		})
		if err := w.db.Repos().Create(ctx, c); err != nil {
			t.Fatal(err)
		}
		remotes[pt] = c
		targets[c.Key] = accesstest.Target{Include: []string{"**"}}
	}
	if err := w.db.Permissions().Create(ctx, accesstest.Permission{
		Name: "manage-remotes",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{"deployer": api.ActionManage}},
			Targets: targets,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	cargo := remotes[repo.Cargo]
	svc := &item.Service{Store: w.db, Blobs: w.blobs, Formats: ecosystem.For}
	cached := map[string]artmodel.RemoteCacheKey{}
	for _, p := range []string{"index/config.json", "index/he/ll/hello_cargo"} {
		key := artmodel.RemoteCacheKey{RepoKey: cargo.Key, Path: p}
		token, err := w.db.RemoteCache().Begin(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = svc.Cache(ctx, item.CacheUpload{
			Token: token, Repo: cargo, Kind: artmodel.RemoteMetadata,
			Body: strings.NewReader("{}"), Length: 2,
		}); err != nil {
			t.Fatal(err)
		}
		cached[p] = key
	}
	read := func(p string) artmodel.RemoteCacheEntry {
		t.Helper()
		e, err := w.db.RemoteCache().Read(ctx, cached[p])
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	index := read("index/he/ll/hello_cargo")
	const forbidden = `{"errors":[{"message":"Forbidden","status":403}]}` + "\n"
	for _, key := range []string{"cargo-remote", "cargo-remote-cache"} {
		for _, query := range []string{"", "?async=false", "?async=true"} {
			path := "/artifactory/api/cargo/" + key + "/reindex" + query
			before := read("index/config.json")
			got := w.do(http.MethodPost, path, "deployer", "")
			if got.Code != http.StatusForbidden || got.Body.String() != forbidden {
				t.Errorf("%s as a manager: %d %q, want 403 %q", path, got.Code, got.Body, forbidden)
			}
			if read("index/config.json") != before {
				t.Errorf("%s: a refused reindex expired config.json", path)
			}
			wantText(t, w.do(http.MethodPost, path, "admin", ""),
				"Recalculating index for Cargo repository "+key+" scheduled to run.")
			after := read("index/config.json")
			if !after.ValidatedAt.IsZero() || after.Token.Epoch <= before.Token.Epoch {
				t.Errorf("%s: config.json not expired: %+v", path, after.Token)
			}
		}
	}
	if read("index/he/ll/hello_cargo") != index {
		t.Error("the remote reindex expired a cached index file")
	}
	for _, key := range []string{"pub-remote", "pub-remote-cache"} {
		path := "/artifactory/api/pub/" + key + "/reindex"
		got := w.do(http.MethodPost, path, "deployer", "")
		if got.Code != http.StatusForbidden || got.Body.String() != forbidden {
			t.Errorf("%s as a manager: %d %q, want 403 %q", path, got.Code, got.Body, forbidden)
		}
		want := `{"errors":[{"message":"'` + key +
			`' is a remote repository, which artifactweave does not reindex yet","status":501}]}` + "\n"
		got = w.do(http.MethodPost, path, "admin", "")
		if got.Code != http.StatusNotImplemented || got.Body.String() != want {
			t.Errorf("%s as admin: %d %q, want 501 %q", path, got.Code, got.Body, want)
		}
	}
	for _, key := range []string{"cargo-remote", "pub-remote"} {
		if _, err := w.db.WorkQueue().Get(ctx, format.KindReindex, key); !errors.Is(
			err,
			kernel.ErrNotFound,
		) {
			t.Errorf("%s: a remote reindex queued work: %v", key, err)
		}
	}
}
