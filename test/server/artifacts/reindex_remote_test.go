package artifacts_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/core/authn"
	accmodel "github.com/weaveplatform/artifactweave/migration/access/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	artmodel "github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/migration/kernel"
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
						u.Status = accmodel.UserDisabled
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
		Create(t.Context(), accmodel.Permission{Name: "manage-remote", Resources: map[api.ResourceType]accmodel.Resource{api.ResourceArtifact: {Actions: accmodel.Grants{Users: map[string]api.Actions{"deployer": api.ActionManage}}, Targets: map[string]accmodel.Target{target: {Include: []string{"**"}}}}}}); err != nil {
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
