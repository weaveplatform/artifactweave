package server

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestRemoteReindexRechecksCurrentAuthority(t *testing.T) {
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
						u.PasswordHash, err = auth.HashPasswordWith(testHash, "changed")
						if err != nil {
							t.Fatal(err)
						}
					} else {
						u.Status = types.UserDisabled
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
				store.ErrNotFound,
			) {
				t.Fatal("revoked caller queued work", err)
			}
		})
	}
}

func TestRemoteReindexCacheAliasDoesNotGrantManage(t *testing.T) {
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
		store.ErrNotFound,
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
		Create(t.Context(), types.Permission{Name: "manage-remote", Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {Actions: types.Grants{Users: map[string]types.Actions{"deployer": types.ActionManage}}, Targets: map[string]types.Target{target: {Include: []string{"**"}}}}}}); err != nil {
		t.Fatal(err)
	}
}

type reindexSourceStore struct {
	store.Store
	before func(context.Context)
	once   sync.Once
}

func (s *reindexSourceStore) RemoteCache() store.RemoteCacheStore {
	return reindexSourceCache{RemoteCacheStore: s.Store.RemoteCache(), s: s}
}

type reindexSourceCache struct {
	store.RemoteCacheStore
	s *reindexSourceStore
}

func (c reindexSourceCache) Source(ctx context.Context, key string) (types.SourceIdentity, error) {
	c.s.once.Do(func() { c.s.before(ctx) })
	return c.RemoteCacheStore.Source(ctx, key) //nolint:wrapcheck // authority-change barrier
}
