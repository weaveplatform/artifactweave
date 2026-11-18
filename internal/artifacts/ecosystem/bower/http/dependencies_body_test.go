package bowerhttp

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/blobtest"
)

type dependencyTestCancelBody struct{ cancel context.CancelFunc }

func (b dependencyTestCancelBody) Read([]byte) (int, error) {
	b.cancel()
	return 0, io.ErrUnexpectedEOF
}
func (b dependencyTestCancelBody) Close() error { return nil }

type dependencyTestZeros struct{}

func (dependencyTestZeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }
func TestAccBowerDependencyOwnedCopy(t *testing.T) {
	for _, mode := range []string{"healthy", "spool", "malformed", "wrong-name", "over-limit", "ended", "upstream"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			c := f.repo("bower-remote", repo.Remote)
			c.URL = "https://allowed.example"
			if e := f.db.Repos().Update(t.Context(), c); e != nil {
				t.Fatal(e)
			}
			v := f.repo("virtual", repo.Virtual, c.Key)
			v.ExternalDependenciesEnabled = new(true)
			v.ExternalDependenciesPatterns = []string{"https://allowed.example/**"}
			v.ExternalDependenciesRemoteRepo = c.Key
			if e := f.db.Repos().Update(t.Context(), v); e != nil {
				t.Fatal(e)
			}
			good := bowerArchive(t, []archiveEntry{{"dep/bower.json", `{"name":"dep","version":"1.0.0"}`, 0}})
			var cancel context.CancelFunc
			calls := 0
			backend := bowerSourceRead(func(context.Context, source.Request, source.Permit) (source.Result, error) {
				calls++
				if mode == "upstream" {
					return source.Result{}, &source.HTTPError{Status: 502}
				}
				var body io.ReadCloser = io.NopCloser(bytes.NewReader(good))
				switch mode {
				case "spool":
					p := filepath.Join(t.TempDir(), "owned-copy.tgz")
					if e := os.WriteFile(p, good, 0o600); e != nil {
						t.Fatal(e)
					}
					file, e := os.Open(p)
					if e != nil {
						t.Fatal(e)
					}
					if e = file.Close(); e != nil {
						t.Fatal(e)
					}
					body = file
				case "malformed":
					body = io.NopCloser(bytes.NewReader([]byte("not an archive")))
				case "wrong-name":
					body = io.NopCloser(bytes.NewReader(bowerArchive(t, []archiveEntry{{"dep/bower.json", `{"name":"other"}`, 0}})))
				case "over-limit":
					body = io.NopCloser(io.LimitReader(dependencyTestZeros{}, maxArchiveBytes+1))
				case "ended":
					body = dependencyTestCancelBody{cancel: cancel}
				}
				return metadataStream(body), nil
			})
			svc := &source.Service{HTTP: backend, Cache: backend, Resolve: func(ctx context.Context, c repo.Config) (model.SourceIdentity, error) {
				return f.db.RemoteCache().Source(ctx, c.Key)
			}}
			protocol := Protocol(Options{RemoteSources: svc})
			protocol.Routes = func(router chi.Router, k *pkgroute.Kit) {
				router.Get("/*", func(w http.ResponseWriter, r *http.Request) {
					ctx, stop := context.WithCancel(r.Context())
					defer stop()
					cancel = stop
					r = r.WithContext(ctx)
					h := handler{k: k, sources: svc}
					pinned, e := h.dependencyRequest(r)
					if e == nil {
						_, e = h.readDependency(pinned, "dep", "https://allowed.example/a.tgz", "", true)
					}
					if e != nil {
						k.Error(w, r, pkgroute.SourceError(e, false))
						return
					}
					w.WriteHeader(200)
				})
			}
			router := chi.NewRouter()
			router.Use(accesstest.Over(t, f.db).Middleware(t, true, func(*http.Request) kernel.Principal { return kernel.Principal{Name: "admin", Admin: true} }))
			pkgroute.Mount(
				router,
				pkgroute.Options{Store: f.db, Items: func() *item.Service { return f.items }, Fault: func(_ *http.Request, err error) { f.faults = append(f.faults, err) }},
				protocol,
			)
			f.router = router
			r := f.get("/artifactory/api/bower/virtual/control", "admin")
			want := 502
			if mode == "healthy" {
				want = 200
			}
			if mode == "spool" {
				want = 500
			}
			if r.Code != want || calls != 1 {
				t.Fatalf("BOWER1689 %s status%d want%d calls%d body%s", mode, r.Code, want, calls, r.Body)
			}
			if want == 500 {
				wantDependencyStorageFault(t, r, f.faults)
			} else if len(f.faults) != 0 {
				t.Fatalf("ordinary response logged a fault: %v", f.faults)
			}
		})
	}
}

// The remote policy owns and fully validates the external archive before it
// hands its cached blob or no-store spool to Bower. Fail that owned copy, not
// the upstream response that the policy has already consumed.
func TestAccBowerDependencyPolicyCopies(t *testing.T) {
	t.Run("cached blob", func(t *testing.T) {
		f, base := dependencyFixture(t)
		blobs := &blobtest.ReadFaults{Store: f.items.Blobs}
		f.items.Blobs, f.policy.Blobs = blobs, blobs
		path := base + "binaries/external/dep.git/" + url.PathEscape(dependencyTarget(f.up.URL+"/external/dep.tgz"))
		wantServedCopyFaults(t, f, blobs, path)
	})
	t.Run("no-store spool", func(t *testing.T) {
		f, base := dependencyFixture(t)
		f.configure(func(c *repo.Config) { c.StoreArtifactsLocally = new(false) })
		failed := false
		backend := bowerSourceRead(func(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
			result, err := f.policy.Read(ctx, q, p)
			if err == nil && failed {
				if result.Stream == nil {
					t.Fatal("no-store policy did not serve its spool")
				}
				if err = result.Stream.Body.Close(); err != nil {
					t.Fatal(err)
				}
			}
			return result, err
		})
		f.mount(&source.Service{HTTP: backend, Cache: backend, Resolve: func(ctx context.Context, c repo.Config) (model.SourceIdentity, error) {
			return f.db.RemoteCache().Source(ctx, c.Key)
		}})
		path := base + "binaries/external/dep.git/" + url.PathEscape(dependencyTarget(f.up.URL+"/external/dep.tgz"))
		if response := f.get(path, "admin"); response.Code != 200 {
			t.Fatalf("sound spool: %d %s", response.Code, response.Body)
		}
		failed = true
		wantDependencyStorageFault(t, f.get(path, "admin"), f.faults)
		failed = false
		if response := f.get(path, "admin"); response.Code != 200 {
			t.Fatalf("restored spool: %d %s", response.Code, response.Body)
		}
	})
}
