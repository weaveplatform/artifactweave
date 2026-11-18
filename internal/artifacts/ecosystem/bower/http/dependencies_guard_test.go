package bowerhttp

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

type dependencyFaultStore struct {
	pkgroute.Store
	fail bool
}
type dependencyFaultRepos struct {
	port.RepoStore
	store *dependencyFaultStore
}

func (s *dependencyFaultStore) Repos() port.RepoStore {
	return dependencyFaultRepos{s.Store.Repos(), s}
}

func (r dependencyFaultRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	if r.store.fail {
		return repo.Config{}, fmt.Errorf("repository statement failed: %w", format.ErrStorageFault)
	}
	return r.RepoStore.Get(ctx, key)
}

// Shared guard markers must survive Bower's adapter, including the final
// authority check and a failed read of its own served copy.
func TestAccBowerExternalGuardStorageFaults(t *testing.T) {
	for _, tc := range []string{"authority", "body"} {
		t.Run(tc, func(t *testing.T) {
			f := newFixture(t)
			c := f.repo("bower-remote", repo.Remote)
			c.URL = "https://allowed.example"
			if err := f.db.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			v := f.repo("virtual", repo.Virtual, c.Key)
			v.ExternalDependenciesEnabled = new(true)
			v.ExternalDependenciesPatterns = []string{"https://allowed.example/**"}
			v.ExternalDependenciesRemoteRepo = c.Key
			if err := f.db.Repos().Update(t.Context(), v); err != nil {
				t.Fatal(err)
			}
			st := &dependencyFaultStore{Store: f.db}
			backend := bowerSourceRead(func(context.Context, source.Request, source.Permit) (source.Result, error) {
				return metadataStream(brokenMetadataBody{}), nil
			})
			svc := &source.Service{HTTP: backend, Cache: backend, Resolve: func(ctx context.Context, c repo.Config) (model.SourceIdentity, error) {
				return f.db.RemoteCache().Source(ctx, c.Key)
			}}
			protocol := Protocol(Options{RemoteSources: svc})
			protocol.Routes = func(router chi.Router, k *pkgroute.Kit) {
				router.Get("/*", func(w http.ResponseWriter, r *http.Request) {
					h := handler{k: k, sources: svc}
					pinned, err := h.dependencyRequest(r)
					if err == nil {
						if tc == "authority" {
							st.fail = true
							err = h.currentDependencyRead(r.Context(), pinned, "dep", true)
						} else {
							_, err = h.readDependency(pinned, "dep", "https://allowed.example/a.tgz", "", true)
						}
					}
					st.fail = false
					if err != nil {
						k.Error(w, r, pkgroute.SourceError(err, false))
						return
					}
					w.WriteHeader(200)
				})
			}
			router := chi.NewRouter()
			router.Use(accesstest.Over(t, f.db).Middleware(t, true, func(*http.Request) kernel.Principal { return kernel.Principal{Name: "admin", Admin: true} }))
			pkgroute.Mount(
				router,
				pkgroute.Options{Store: st, Items: func() *item.Service { return f.items }, Fault: func(_ *http.Request, err error) { f.faults = append(f.faults, err) }},
				protocol,
			)
			f.router = router
			got := f.get("/artifactory/api/bower/virtual/control", "admin")
			wantDependencyStorageFault(t, got, f.faults)
			t.Logf("BOWER guard %s status=%d body=%s", tc, got.Code, got.Body.String())
		})
	}
}
