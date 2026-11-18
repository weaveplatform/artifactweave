package pypi_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	mavenfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/maven"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

func TestAccPypiCachePolicyPathBackends(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			for _, route := range []string{"remote", "virtual"} {
				for _, mode := range []string{"cold", "no-store", "warm", "excluded"} {
					t.Run(route+"/"+mode, func(t *testing.T) {
						var changes []func(*config.Config)
						if backend == "postgres" {
							dsn := sqltest.ScratchDSN(t, sqltest.PostgresDSN(t))
							changes = append(changes, func(c *config.Config) { c.Database.Driver, c.Database.DSN = "postgres", dsn })
						}
						w := virtualPythonWorld(t, changes...)
						path, data := pythonRemote(t, w, "demo", "1.0")
						if mode == "no-store" {
							pythonConfig(t, w, "remote", func(c *repo.Config) { c.StoreArtifactsLocally = new(false) })
						}
						if mode == "warm" {
							r := w.Request(http.MethodGet, "/artifactory/api/pypi/remote/"+path, "reader", "")
							if r.Code != http.StatusOK {
								t.Fatal("warm seed", r.Code, r.Body.String())
							}
						}
						pythonConfig(t, w, "remote", func(c *repo.Config) {
							c.IncludesPattern = new("files/**")
							if mode == "excluded" {
								c.ExcludesPattern = new("files/**")
							}
						})
						r := w.Request(http.MethodGet, "/artifactory/api/pypi/"+route+"/"+path, "reader", "")
						want := http.StatusOK
						if mode == "excluded" {
							want = http.StatusNotFound
						}
						if r.Code != want || want == http.StatusOK && !bytes.Equal(r.Body.Bytes(), data) {
							t.Fatalf("%s %s %s: %d %s", backend, route, mode, r.Code, r.Body.String())
						}
						it, err := w.Runtime.Store.Items().Get(t.Context(), "remote", true, path)
						entries, _, entryErr := w.Runtime.Store.RemoteCache().Exact(t.Context(), "remote", path, kernel.Page{Limit: 100})
						if entryErr != nil {
							t.Fatal(entryErr)
						}
						if mode == "excluded" || mode == "no-store" {
							if !errors.Is(err, kernel.ErrNotFound) || len(entries) != 0 {
								t.Fatalf("refused/transient read published item=%v entries=%v", err, entries)
							}
						} else if err != nil || it.FullPath() != path || len(entries) != 1 || entries[0].ItemID != it.ID {
							t.Fatalf("canonical publication: item=%+v err=%v entries=%v", it, err, entries)
						}
						gets := w.Upstream.Count(http.MethodGet, "/files/demo-1.0-py3-none-any.whl")
						wantGets := 1
						if mode == "excluded" {
							wantGets = 0
						}
						if gets != wantGets {
							t.Fatalf("origin GETs=%d want%d", gets, wantGets)
						}
					})
				}
			}
		})
	}
}

func TestAccPypiSourcePolicyCannotAuthorizeOrdinaryDeploy(t *testing.T) {
	w := virtualPythonWorld(t)
	path, data := pythonRemote(t, w, "demo", "1.0")
	pythonConfig(t, w, "remote", func(c *repo.Config) { c.IncludesPattern = new("files/**") })
	r := w.Request(http.MethodGet, "/artifactory/api/pypi/remote/"+path, "reader", "")
	if r.Code != http.StatusOK {
		t.Fatal(r.Code, r.Body.String())
	}
	pythonConfig(t, w, "first", func(c *repo.Config) { c.IncludesPattern = new("files/**") })
	r = w.Request(http.MethodPut, "/artifactory/first/"+path+"?policyPath=files/allowed.whl", "admin", string(data), "X-Policy-Path", "files/allowed.whl")
	if r.Code != http.StatusConflict {
		t.Fatal("ordinary deploy borrowed source admission", r.Code, r.Body.String())
	}
	if _, err := w.Runtime.Store.Items().Get(t.Context(), "first", false, path); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("refused ordinary deploy published", err)
	}
}

func TestAccPypiCachePolicyRetainsPublicationFences(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			for _, route := range []string{"remote", "virtual"} {
				for _, change := range []string{"patterns", "permission", "revision"} {
					t.Run(route+"/"+change, func(t *testing.T) {
						var mutate []func(*config.Config)
						if backend == "postgres" {
							dsn := sqltest.ScratchDSN(t, sqltest.PostgresDSN(t))
							mutate = append(mutate, func(c *config.Config) { c.Database.Driver, c.Database.DSN = "postgres", dsn })
						}
						w := virtualPythonWorld(t, mutate...)
						path, _ := pythonRemote(t, w, "demo", "1.0")
						pythonConfig(t, w, "remote", func(c *repo.Config) { c.IncludesPattern = new("files/**") })
						changed, err := w.Runtime.Store.Repos().Get(t.Context(), "remote")
						if err != nil {
							t.Fatal(err)
						}
						switch change {
						case "patterns":
							changed.ExcludesPattern = new("files/**")
						case "revision":
							changed.Password = "new-test-source-revision"
						}
						var called atomic.Bool
						policy := w.Runtime.Artifacts.RemoteSources().HTTP.(*mavenfmt.MavenBackend).Policy
						previous := policy.PrepareUpload
						policy.PrepareUpload = func(ctx context.Context, q source.Request, p source.Permit, o remote.Options, u *item.CacheUpload) error {
							if previous != nil {
								if err := previous(ctx, q, p, o, u); err != nil {
									return err
								}
							}
							if q.Resource.Kind != source.ResourceKindArtifact || q.Resource.Path != path {
								return nil
							}
							validate := u.Validate
							u.Validate = func(ctx context.Context, body io.ReadSeeker, size int64) error {
								if validate != nil {
									if err := validate(ctx, body, size); err != nil {
										return err
									}
								}
								if called.Swap(true) {
									return nil
								}
								if change == "permission" {
									return w.Runtime.Store.Permissions().Delete(ctx, "remote-read")
								}
								return w.Runtime.Store.Repos().Update(ctx, changed)
							}
							return nil
						}
						r := w.Request(http.MethodGet, "/artifactory/api/pypi/"+route+"/"+path, "reader", "")
						want := http.StatusServiceUnavailable
						if change == "permission" {
							want = http.StatusForbidden
							if route == "virtual" {
								want = http.StatusNotFound
							}
						}
						if !called.Load() || r.Code != want || bytes.Contains(r.Body.Bytes(), []byte("PK\x03\x04")) {
							t.Fatalf("%s %s %s: called%v status%d want%d: %s", backend, route, change, called.Load(), r.Code, want, r.Body.String())
						}
						if _, err := w.Runtime.Store.Items().Get(t.Context(), "remote", true, path); !errors.Is(err, kernel.ErrNotFound) {
							t.Fatal("late denial published item", err)
						}
						entries, _, err := w.Runtime.Store.RemoteCache().Exact(t.Context(), "remote", path, kernel.Page{Limit: 100})
						if err != nil {
							t.Fatal(err)
						}
						// Begin retains an empty CAS tombstone; a denied fill must publish no body.
						for _, entry := range entries {
							if entry.ItemID != 0 || entry.Blob.Checksums.SHA256 != "" || !entry.NegativeUntil.IsZero() {
								t.Fatal("late denial published raw cache body", entry)
							}
						}
					})
				}
			}
		})
	}
}
