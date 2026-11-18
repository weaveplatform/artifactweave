package nugethttp_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

func TestAccVirtualSymbolsFirstHolderAndCacheOnly(t *testing.T) {
	f := newVirtualFixture(t)
	path := "app.pdb/" + remotePDBKey + "/app.pdb"
	local := remotePDB()
	local[110] = 1
	f.deploy(".symbols/"+path, local)
	f.virtualRepo(t, "nuget-remote", func(c *repo.Config) { c.SymbolServerURL = f.up.URL + "/symbols" })
	f.up.Set("/symbols/"+path, fakeupstream.Resource{Body: remotePDB()})
	got := f.do("GET", virtualV3+path, "reader")
	wantStatus(t, got, 200)
	if !bytes.Equal(got.Body.Bytes(), local) || len(f.up.Requests()) != 0 {
		t.Fatal("first local holder lost", got.Body, len(f.up.Requests()))
	}
	f.virtualRepo(t, "nuget-local", func(c *repo.Config) { c.ExcludesPattern = new(".symbols/**") })
	got = f.do("GET", virtualV3+path, "reader")
	wantStatus(t, got, 200)
	if !bytes.Equal(got.Body.Bytes(), remotePDB()) {
		t.Fatal("member policy failed to pass over local")
	}
	before := len(f.up.Requests())
	f.virtualRepo(t, "nuget-remote", func(c *repo.Config) { c.Offline = new(true) })
	wantStatus(t, f.do("GET", virtualV3+path, "reader"), 200)
	missing := strings.Replace(path, remotePDBKey, strings.Repeat("A", 40), 1)
	wantStatus(t, f.do("GET", virtualV3+missing, "reader"), 404)
	if len(f.up.Requests()) != before {
		t.Fatal("offline symbol touched upstream")
	}
	f.virtualRepo(t, "nuget-remote", func(c *repo.Config) { c.Offline = new(false) })
	f.opts.NoUpstream = func(_ *http.Request, _ repo.Config) bool { return true }
	f.mount(t, base)
	wantStatus(t, f.do("GET", virtualV3+path, "reader"), 200)
	wantStatus(t, f.do("GET", virtualV3+missing, "reader"), 404)
	if len(f.up.Requests()) != before {
		t.Fatal("cache-only symbol touched upstream")
	}
}

func TestAccVirtualSymbolsSourceFenceAndStorageFaults(t *testing.T) {
	for _, mode := range []string{"denied", "delivery denied", "revision", "storage"} {
		t.Run(mode, func(t *testing.T) {
			f := newVirtualFixture(t)
			path := "app.pdb/" + remotePDBKey + "/app.pdb"
			f.virtualRepo(t, "nuget-remote", func(c *repo.Config) { c.SymbolServerURL = f.up.URL + "/symbols" })
			f.up.Set("/symbols/"+path, fakeupstream.Resource{Body: remotePDB()})
			original := f.svc.HTTP
			done := false
			f.svc.HTTP = backendFunc(func(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
				if mode == "storage" {
					return source.Result{}, fmt.Errorf("%w: %w", format.ErrStorageFault, errBroken)
				}
				out, err := original.Read(ctx, q, p)
				if mode == "delivery denied" {
					checks := 0
					out.Revalidate = func(context.Context) error {
						checks++
						if checks >= 3 {
							return source.ErrDenied
						}
						return nil
					}
				}
				done = true
				return out, err
			})
			f.svc.Revalidate = func(context.Context, source.Request, source.Permit) error {
				if done && mode != "delivery denied" {
					if mode == "revision" {
						return source.ErrSuperseded
					}
					return source.ErrDenied
				}
				return nil
			}
			want := 404
			if mode == "storage" {
				want = 500
			}
			if mode == "revision" {
				want = 503
			}
			got := f.do("GET", virtualV3+path, "reader")
			wantStatus(t, got, want)
			if bytes.Contains(got.Body.Bytes(), []byte("BSJB")) {
				t.Fatal("fenced symbol bytes disclosed")
			}
		})
	}
	f := newVirtualFixture(t)
	f.opts.Store = failingStore{Store: f.db, failing: "item"}
	f.mount(t, base)
	wantStatus(t, f.do("GET", virtualV3+"app.pdb/"+remotePDBKey+"/app.pdb", "reader"), 500)
}
