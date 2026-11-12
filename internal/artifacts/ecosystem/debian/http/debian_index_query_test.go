package debianhttp

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
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
// one that lacks the file is [I]. A remote member's stream carries no
// properties, so only a mandatory key passes over it; its cached file is
// matched by the properties it carries.
func TestAccDebianVirtualPassesOverAMemberTheMatrixDoesNotMatch(t *testing.T) {
	for _, tc := range []struct {
		name, matrix string
		cached       model.Properties // nil streams the package, not cached
		want         []byte
	}{
		{"stream, a key it may lack", ";x=1", nil, remoteDeb},
		{"stream, a mandatory key", ";x+=1", nil, localDeb},
		{"stream, a mandatory key no member carries", ";x+=2", nil, nil},
		{"cached file without the key", ";x=1", model.Properties{}, remoteDeb},
		{"cached file without a mandatory key", ";x+=1", model.Properties{}, localDeb},
		{"cached file with another value", ";x=1", model.Properties{"x": {"2"}}, localDeb},
		{"cached file with the value", ";x+=2", model.Properties{"x": {"2"}}, remoteDeb},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var w *overlayWorld
			read := streamed(remoteDeb, nil)
			if tc.cached != nil {
				read = func() (source.Result, error) {
					it := w.put("deb-remote", debAlpha, true, remoteDeb)
					if len(tc.cached) != 0 {
						w.withProperties(it, tc.cached)
					}
					return source.Result{Item: &it}, nil
				}
			}
			w = newOverlayWorld(t, withSources(upstream(
				map[string]func() (source.Result, error){debAlpha: read},
			)))
			w.debRepos(
				repo.Config{Key: "deb-remote", Class: repo.Remote},
				repo.Config{Key: "deb-local", Class: repo.Local},
				repo.Config{
					Key: "deb-virtual", Class: repo.Virtual,
					Repositories: []string{"deb-remote", "deb-local"},
				},
			)
			w.withProperties(w.put("deb-local", debAlpha, false, localDeb),
				model.Properties{"x": {"1"}})
			rec := w.get("deb-virtual", debAlpha+tc.matrix, "admin")
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
// A remote member serves its index for a query as it does without one
// (#1349), so it still answers first. It refuses a matrix parameter, so the
// virtual passes over it without asking its upstream, and a local member's
// index is matched by the properties it carries.
func TestAccDebianVirtualSourcesWithAQueryOrMatrixParameters(t *testing.T) {
	for _, tc := range []struct {
		name, suffix string
		want         []byte
	}{
		{"no parameter", "", sourcesIndex("2.0-1")},
		{"query", "?skipUpdateStats=true", sourcesIndex("2.0-1")},
		{"matrix parameter it matches", ";x=1", sourcesIndex("1.0-1")},
		{"matrix parameter it does not match", ";x=2", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asked := false
			w := newOverlayWorld(t, withSources(
				func(context.Context, source.Request, source.Permit) (source.Result, error) {
					asked = true
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
			w.withProperties(w.put("deb-local", debSources, false, sourcesIndex("1.0-1")),
				model.Properties{"x": {"1"}})
			rec := w.get("deb-virtual", debSources+tc.suffix, "admin")
			remote := !strings.HasPrefix(tc.suffix, ";")
			if asked != remote {
				t.Errorf("upstream asked %v, want %v", asked, remote)
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
