package debianhttp

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// The bytes a remote member's upstream and a local member serve for the
// same package, which tell the test which member answered.
var (
	remoteDeb = []byte("remote deb bytes")
	localDeb  = []byte("local deb bytes")
)

// propertiesFail makes every properties read of the kit's store fail, as
// a store that cannot be read does.
func propertiesFail(s serverStore) serverStore {
	return itemFaults{Store: s.(*inmem.Store), fails: func(call, _, _ string) bool {
		return call == "Properties"
	}}
}

// withProperties stores props on it, as a deploy with matrix parameters
// leaves them.
func (w *itemWorld) withProperties(it model.Item, props model.Properties) {
	w.t.Helper()
	if err := w.db.Items().
		SetProperties(w.t.Context(), it.ID, props, time.Now().UTC()); err != nil {
		w.t.Fatal(err)
	}
}

// TestUnitDebianIndexMatrixFailsClosed: a stored index, a virtual
// repository's package or Sources index from a local member, and a remote
// member's cached package answer 500 when a matrix parameter asks for the
// file's properties and they cannot be read, rather than serving a file the
// parameters may not match. Without a matrix parameter nothing reads an
// index's properties, so an index is served; a package's download reads
// them for its artifactory.content-type (#1380), as 7.x's getInfo reads
// the properties of every file it downloads, so it answers 500 too.
func TestUnitDebianIndexMatrixFailsClosed(t *testing.T) {
	index := packagesIndex("alpha 1.0 " + debAlpha)
	for _, tc := range []struct {
		name, key, path string
		plain           int // the answer without a matrix parameter
	}{
		{"local index", "deb-local", debPackages, http.StatusOK},
		{"virtual package from a local member", "deb-virtual", debAlpha, http.StatusInternalServerError},
		{"virtual Sources from a local member", "deb-virtual", debSources, http.StatusOK},
		{
			"virtual package cached by a remote member", "deb-cached", debAlpha,
			http.StatusInternalServerError,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var w *overlayWorld
			w = newOverlayWorld(t, withStore(propertiesFail), withSources(upstream(
				map[string]func() (source.Result, error){debAlpha: func() (source.Result, error) {
					it := w.item("deb-remote", true, debAlpha)
					return source.Result{Item: &it}, nil
				}},
			)))
			w.debRepos(
				repo.Config{Key: "deb-local", Class: repo.Local},
				repo.Config{Key: "deb-remote", Class: repo.Remote},
				repo.Config{
					Key: "deb-virtual", Class: repo.Virtual, Repositories: []string{"deb-local"},
				},
				repo.Config{
					Key: "deb-cached", Class: repo.Virtual, Repositories: []string{"deb-remote"},
				},
			)
			w.publish("deb-local", index)
			w.put("deb-local", debAlpha, false, localDeb)
			w.put("deb-local", debSources, false, sourcesIndex("1.0-1"))
			w.put("deb-remote", debAlpha, true, remoteDeb)
			if rec := w.get(tc.key, tc.path, "admin"); rec.Code != tc.plain {
				t.Fatalf(
					"without a matrix parameter answered %d %s, want %d",
					rec.Code,
					rec.Body,
					tc.plain,
				)
			}
			wantError(t, w.get(tc.key, tc.path+";x=1", "admin"),
				http.StatusInternalServerError, "internal error")
		})
	}
}

// TestAccDebianVirtualPassesOverAMemberTheMatrixDoesNotMatch: a virtual
// repository asks its members in order for a package, and a member whose
// file the matrix parameters do not match is passed over for the next, as
// one that lacks the file is [I]. A remote member matches only the copy its
// cache held before the request, without asking its upstream (7.x's
// RemoteRepoBase.internalGetInfo, 7.161.26). Without one it asks the
// upstream with the parameters and serves its answer, a stream or the copy
// it stores, unmatched, as 7.x's downloadAndSave does (#1351, #1371).
func TestAccDebianVirtualPassesOverAMemberTheMatrixDoesNotMatch(t *testing.T) {
	for _, tc := range []struct {
		name, matrix string
		stored       bool             // the upstream's answer is stored, not streamed
		cached       model.Properties // non-nil: a copy cached before the request
		want         []byte
		asked        string // the matrix parameters the upstream got; "" not asked
	}{
		{"stream, a key it may lack", ";x=1", false, nil, remoteDeb, ";x=1"},
		{"stream, a mandatory key", ";x+=1", false, nil, remoteDeb, ";x%2B=1"},
		{"stream, a mandatory key no member carries", ";x+=2", false, nil, remoteDeb, ";x%2B=2"},
		{"stored on fetch, a mandatory key", ";x+=2", true, nil, remoteDeb, ";x%2B=2"},
		{"cached copy without the key", ";x=1", false, model.Properties{}, remoteDeb, ";x=1"},
		{"cached copy without a mandatory key", ";x+=1", false, model.Properties{}, localDeb, ""},
		{"cached copy with another value", ";x=1", false, model.Properties{"x": {"2"}}, localDeb, ""},
		{
			"cached copy with the value", ";x+=2", false,
			model.Properties{"x": {"2"}},
			remoteDeb,
			";x%2B=2",
		},
		{
			"cached copy, a mandatory key no member carries", ";y+=1", false,
			model.Properties{},
			nil, "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var w *overlayWorld
			var cached model.Item
			asked := ""
			w = newOverlayWorld(t, withSources(
				func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
					if q.Resource.Path != debAlpha {
						return source.Result{}, kernel.ErrNotFound
					}
					asked = q.MatrixParams
					switch {
					case tc.cached != nil:
						return source.Result{Item: &cached}, nil
					case tc.stored:
						it := w.put("deb-remote", debAlpha, true, remoteDeb)
						return source.Result{Item: &it}, nil
					}
					return streamed(remoteDeb, nil)()
				},
			))
			w.debRepos(
				repo.Config{Key: "deb-remote", Class: repo.Remote},
				repo.Config{Key: "deb-local", Class: repo.Local},
				repo.Config{
					Key: "deb-virtual", Class: repo.Virtual,
					Repositories: []string{"deb-remote", "deb-local"},
				},
			)
			if tc.cached != nil {
				cached = w.put("deb-remote", debAlpha, true, remoteDeb)
				if len(tc.cached) != 0 {
					w.withProperties(cached, tc.cached)
				}
			}
			w.withProperties(w.put("deb-local", debAlpha, false, localDeb),
				model.Properties{"x": {"1"}})
			rec := w.get("deb-virtual", debAlpha+tc.matrix, "admin")
			if asked != tc.asked {
				t.Errorf("upstream asked with %q, want %q", asked, tc.asked)
			}
			if tc.want == nil {
				wantError(t, rec, http.StatusNotFound, "Could not find resource")
				return
			}
			if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), tc.want) {
				t.Fatalf("answered %d %q, want 200 %q", rec.Code, rec.Body, tc.want)
			}
		})
	}
}

// TestAccDebianVirtualSourcesWithAQueryOrMatrixParameters: a virtual
// repository's Sources index comes from the first member that serves it.
// A remote member serves its index for a query (#1349) and for matrix
// parameters (#1371) as it does without them, asking its upstream with
// them, so it still answers first. A copy its cache holds is matched
// first, and a member whose copy does not match is passed over without
// asking its upstream, for a local member's index matched by the
// properties it carries.
func TestAccDebianVirtualSourcesWithAQueryOrMatrixParameters(t *testing.T) {
	for _, tc := range []struct {
		name, suffix string
		cached       bool // the remote member's cache holds a copy without properties
		want         []byte
		query        string // what the upstream got; "-" not asked
		matrix       string
	}{
		{"no parameter", "", false, sourcesIndex("2.0-1"), "", ""},
		{"query", "?skipUpdateStats=true&a=1", false, sourcesIndex("2.0-1"), "a=1", ""},
		{"matrix parameter", ";x=1", false, sourcesIndex("2.0-1"), "", ";x=1"},
		{"mandatory matrix parameter", ";x+=2", false, sourcesIndex("2.0-1"), "", ";x%2B=2"},
		{
			"cached copy, a mandatory key the local index carries", ";x+=1", true,
			sourcesIndex("1.0-1"), "-", "-",
		},
		{"cached copy, a mandatory key no member carries", ";x+=2", true, nil, "-", "-"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query, matrix := "-", "-"
			w := newOverlayWorld(t, withSources(
				func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
					query, matrix = q.Resource.Query, q.MatrixParams
					return streamed(sourcesIndex("2.0-1"), nil)()
				},
			))
			w.debRepos(
				repo.Config{Key: "deb-remote", Class: repo.Remote},
				repo.Config{Key: "deb-local", Class: repo.Local},
				repo.Config{
					Key: "deb-virtual", Class: repo.Virtual,
					Repositories: []string{"deb-remote", "deb-local"},
				},
			)
			if tc.cached {
				w.put("deb-remote", debSources, true, sourcesIndex("2.0-1"))
			}
			w.withProperties(w.put("deb-local", debSources, false, sourcesIndex("1.0-1")),
				model.Properties{"x": {"1"}})
			rec := w.get("deb-virtual", debSources+tc.suffix, "admin")
			if query != tc.query || matrix != tc.matrix {
				t.Errorf("upstream asked with query %q, matrix %q; want %q, %q",
					query, matrix, tc.query, tc.matrix)
			}
			if tc.want == nil {
				wantError(t, rec, http.StatusNotFound, "Could not find resource")
				return
			}
			if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), tc.want) {
				t.Fatalf("answered %d %q, want 200 %q", rec.Code, rec.Body, tc.want)
			}
		})
	}
}

// TestUnitDebianVirtualSourcesFailsOnAStoreFault: a local member's
// Sources index that cannot be looked up answers 500 when a matrix
// parameter needs it, rather than passing the member over.
func TestUnitDebianVirtualSourcesFailsOnAStoreFault(t *testing.T) {
	w := newOverlayWorld(t, withStore(func(s serverStore) serverStore {
		return faulty{serverStore: s, items: "deb-local"}
	}))
	w.debRepos(
		repo.Config{Key: "deb-local", Class: repo.Local},
		repo.Config{Key: "deb-virtual", Class: repo.Virtual, Repositories: []string{"deb-local"}},
	)
	wantError(t, w.get("deb-virtual", debSources+";x=1", "admin"),
		http.StatusInternalServerError, "internal error")
}
