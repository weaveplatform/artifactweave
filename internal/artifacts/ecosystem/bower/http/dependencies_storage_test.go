package bowerhttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	artsql "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

func TestAccBowerDependencyAuthorityStorage(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{artsql.Module}, artsql.New, func(t *testing.T, newStore func(*testing.T) *artsql.Store) {
		for _, phase := range []string{"check", "current"} {
			for _, mode := range []string{"healthy", "repositories", "remote_sources", "generation", "deleted", "ended"} {
				t.Run(phase+"/"+mode, func(t *testing.T) {
					db := newStore(t)
					f := newFixture(t)
					st := dependencySQLStore{Store: f.db, sql: db}
					c := *repo.Defaults(&repo.Config{Key: "virtual", Class: repo.Virtual, PackageType: repo.Bower, ExternalDependenciesEnabled: new(true)})
					if err := db.Repos().Create(t.Context(), c); err != nil {
						t.Fatal(err)
					}
					var faults []error
					router := chi.NewRouter()
					router.Use(accesstest.Over(t, f.db).Middleware(t, true, func(*http.Request) kernel.Principal { return kernel.Principal{Name: "admin", Admin: true} }))
					protocol := Protocol(Options{})
					protocol.Routes = func(r chi.Router, k *pkgroute.Kit) {
						r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
							h := handler{k: k}
							pinned, e := h.dependencyRequest(r)
							if e != nil {
								t.Fatal(e)
							}
							var statement error
							switch mode {
							case "repositories", "remote_sources":
								sqltest.Exec(t, db.DB(), "ALTER TABLE "+mode+" RENAME TO "+mode+"_hidden")
								defer sqltest.Exec(t, db.DB(), "ALTER TABLE "+mode+"_hidden RENAME TO "+mode)
								if mode == "repositories" {
									_, statement = db.Repos().Get(context.Background(), "virtual")
								} else {
									_, statement = db.RemoteCache().Source(context.Background(), "virtual")
								}
								if statement == nil || errors.Is(statement, kernel.ErrNotFound) {
									t.Fatal("not a real statement failure", statement)
								}
							case "generation":
								before, e := db.RemoteCache().Source(r.Context(), "virtual")
								if e != nil {
									t.Fatal(e)
								}
								changed := c
								changed.ExternalDependenciesPatterns = []string{"https://changed.example/**"}
								if e = db.Repos().Update(r.Context(), changed); e != nil {
									t.Fatal(e)
								}
								after, e := db.RemoteCache().Source(r.Context(), "virtual")
								if e != nil || before.Generation == after.Generation {
									t.Fatal("generation did not advance", before, after, e)
								}
							case "ended":
								ctx, cancel := context.WithCancel(pinned.Context())
								cancel()
								pinned = pinned.WithContext(ctx)
								r = r.WithContext(ctx)
							case "deleted":
								if e = db.Repos().Delete(r.Context(), "virtual"); e != nil {
									t.Fatal(e)
								}
							}
							if phase == "check" {
								e = h.checkDependencyAuthority(r.Context(), pinned)
							} else {
								e = h.currentDependencyRead(r.Context(), pinned, "dep", true)
							}
							t.Logf("mode=%s actual-statement=%v guard=%v", mode, statement, e)
							if e != nil {
								k.Error(w, r, pkgroute.SourceError(e, false))
								return
							}
							w.WriteHeader(200)
						})
					}
					pkgroute.Mount(
						router,
						pkgroute.Options{Store: st, Items: func() *item.Service { return nil }, Fault: func(_ *http.Request, err error) { faults = append(faults, err) }},
						protocol,
					)
					response := httptest.NewRecorder()
					router.ServeHTTP(response, httptest.NewRequest("GET", "/artifactory/api/bower/virtual/control", nil))
					want := 200
					if mode == "repositories" || mode == "remote_sources" {
						want = 500
					}
					if mode == "generation" || mode == "deleted" || mode == "ended" {
						want = 503
					}
					if response.Code != want {
						t.Fatalf("%s status=%d want%d body=%s", mode, response.Code, want, response.Body.String())
					}
					if want == 500 {
						wantDependencyStorageFault(t, response, faults)
					} else if len(faults) != 0 {
						t.Fatalf("ordinary response logged a fault: %v", faults)
					}
				})
			}
		}
	}, sqltest.WithPostgres())
}

func wantDependencyStorageFault(t *testing.T, response *httptest.ResponseRecorder, faults []error) {
	t.Helper()
	if response.Code != 500 || response.Body.String() != `{"errors":[{"message":"internal error","status":500}]}`+"\n" {
		t.Fatalf("storage fault: %d %s", response.Code, response.Body)
	}
	if len(faults) != 1 || !errors.Is(faults[0], format.ErrStorageFault) {
		t.Fatalf("storage fault was not logged exactly once: %v", faults)
	}
	if response.Header().Get("Retry-After") != "" {
		t.Fatal("storage fault must not advertise supersession retry")
	}
}

// Only the authoritative repository and source records need SQL here. Other
// kit services and authorization retain the ordinary in-memory route fixture.
type dependencySQLStore struct {
	pkgroute.Store
	sql *artsql.Store
}

func (s dependencySQLStore) Repos() port.RepoStore              { return s.sql.Repos() }
func (s dependencySQLStore) RemoteCache() port.RemoteCacheStore { return s.sql.RemoteCache() }
