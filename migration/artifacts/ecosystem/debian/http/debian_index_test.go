package debianhttp

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
)

// zeros reads as an endless run of zero bytes, an upstream body that
// never ends.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// TestAccDebianIndexRefusesQueriesAndCallersWhoMayNotRead: a Packages index
// with a query is not found, and one in a blacked-out repository, or asked
// for by a caller who may not read it, is refused before it is read.
func TestAccDebianIndexRefusesQueriesAndCallersWhoMayNotRead(t *testing.T) {
	w := newOverlayWorld(t)
	w.debRepos(
		repo.Config{Key: "deb-local", Class: repo.Local},
		repo.Config{Key: "deb-dark", Class: repo.Local, BlackedOut: new(true)},
	)
	for _, key := range []string{"deb-local", "deb-dark"} {
		w.publish(key, packagesIndex("alpha 1.0 "+debAlpha))
	}
	wantError(t, w.get("deb-local", debPackages+"?x=1", "admin"),
		http.StatusNotFound, "Could not find resource")
	wantError(t, w.get("deb-local", debPackages, "user"), http.StatusForbidden, "Forbidden")
	wantError(t, w.get("deb-dark", debPackages, "admin"), http.StatusForbidden, "Forbidden")
}

// TestUnitDebianIndexOfARepositoryWithoutOne: the index read answers not
// found, without asking the source, for a local repository's cache, for a
// virtual repository, whose indexes the virtual view serves, and for a
// remote index path the source cannot name. The item route and the overlay
// never pass these, so only a direct call reaches the refusals.
func TestUnitDebianIndexOfARepositoryWithoutOne(t *testing.T) {
	w := newOverlayWorld(t, withSources(func(
		context.Context, source.Request, source.Permit,
	) (source.Result, error) {
		t.Error("the source was asked for an index the read should refuse first")
		return source.Result{}, source.ErrUnavailable
	}))
	w.debRepos(
		repo.Config{Key: "deb-local", Class: repo.Local},
		repo.Config{Key: "deb-remote", Class: repo.Remote},
		repo.Config{Key: "deb-virtual", Class: repo.Virtual, Repositories: []string{"deb-local"}},
	)
	w.publish("deb-local", packagesIndex("alpha 1.0 "+debAlpha))
	for _, tc := range []struct {
		name, key string
		cached    bool
		path      string
	}{
		{"local repository's cache", "deb-local", true, debPackages},
		{"virtual repository", "deb-virtual", false, debPackages},
		{"remote index the source cannot name", "deb-remote", false, debPackages + ".lz4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := itemkit.ItemRequest{Key: tc.key, Path: tc.path}
			DebianAPI{w.kit}.serveDebianIndex(rec, w.request(tc.key, tc.path, "admin"), w.db,
				w.debRepo(tc.key), tc.cached, req, kittest.Admin, kittest.AdminAccess())
			wantError(t, rec, http.StatusNotFound, "Could not find resource")
		})
	}
}

// TestAccDebianLocalIndexFailsClosed: a local index is served only from a
// file whose stored content parses as the index its path names. A folder at
// the path is not found; content that cannot be read, or a Packages or
// Sources index that does not parse, is a server fault, so apt is never
// handed an index whose packages were not checked.
func TestAccDebianLocalIndexFailsClosed(t *testing.T) {
	index := packagesIndex("alpha 1.0 " + debAlpha)
	t.Run("parsed index", func(t *testing.T) {
		w := newOverlayWorld(t)
		w.debRepos(repo.Config{Key: "deb-local", Class: repo.Local})
		w.publish("deb-local", index)
		rec := w.get("deb-local", debPackages, "admin")
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), index) ||
			rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Errorf("answered %d %q as %q, want the stored index as text",
				rec.Code, rec.Body, rec.Header().Get("Content-Type"))
		}
	})
	brokenStore := func(c *overlayConfig) { c.Blobs = brokenBlobs{c.Blobs} }
	for _, tc := range []struct {
		name    string
		tune    []func(*overlayConfig)
		path    string
		seed    func(*overlayWorld)
		status  int
		message string
	}{
		{
			name: "folder at the index path", path: debPackages,
			seed:   func(w *overlayWorld) { w.folderAt("deb-local", debPackages) },
			status: http.StatusNotFound, message: "Could not find resource",
		},
		{
			name: "Packages that does not parse", path: debPackages,
			seed: func(w *overlayWorld) {
				w.put("deb-local", debPackages, false, []byte("not an index"))
			},
			status: http.StatusInternalServerError, message: "internal error",
		},
		{
			name: "Sources that does not parse", path: debSources,
			seed: func(w *overlayWorld) {
				w.put("deb-local", debSources, false, []byte("not an index"))
			},
			status: http.StatusInternalServerError, message: "internal error",
		},
		{
			name: "content unreadable", path: debPackages,
			tune:   []func(*overlayConfig){brokenStore},
			seed:   func(w *overlayWorld) { w.publish("deb-local", index) },
			status: http.StatusInternalServerError, message: "internal error",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOverlayWorld(t, tc.tune...)
			w.debRepos(repo.Config{Key: "deb-local", Class: repo.Local})
			tc.seed(w)
			wantError(t, w.get("deb-local", tc.path, "admin"), tc.status, tc.message)
		})
	}
}

// TestAccDebianRemoteIndex: a remote index comes from the source's stream
// or the item it cached, and is served only within the size bound and
// once the source has rechecked each listed package. A server with no
// remote sources, an index over the bound and a package check that cannot
// be made are server faults; a refused package is refused.
func TestAccDebianRemoteIndex(t *testing.T) {
	index := packagesIndex("alpha 1.0 " + debAlpha)
	checkFails := func(err error) func() func(context.Context) error {
		return func() func(context.Context) error { return failsOnCall(2, err) }
	}
	for _, tc := range []struct {
		name    string
		sources bool
		answer  func(*overlayWorld) (source.Result, error)
		status  int
		message string
	}{
		{
			name:   "no remote sources",
			status: http.StatusInternalServerError, message: "internal error",
		},
		{
			name: "cached index", sources: true,
			answer: func(w *overlayWorld) (source.Result, error) {
				it := w.put("deb-remote", debPackages, true, index)
				return source.Result{Item: &it}, nil
			},
			status: http.StatusOK,
		},
		{
			name: "index over the size bound", sources: true,
			answer: func(*overlayWorld) (source.Result, error) {
				return source.Result{Stream: &source.Stream{
					Body:           io.NopCloser(io.LimitReader(zeros{}, debianIndexLimit+1)),
					Status:         http.StatusOK,
					Representation: source.Representation{Size: debianIndexLimit + 1},
				}}, nil
			},
			status: http.StatusInternalServerError, message: "internal error",
		},
		{
			name: "listed package refused", sources: true,
			answer: func(*overlayWorld) (source.Result, error) {
				return streamed(index, checkFails(source.ErrDenied))()
			},
			status: http.StatusForbidden, message: "Forbidden",
		},
		{
			name: "listed package cannot be checked", sources: true,
			answer: func(*overlayWorld) (source.Result, error) {
				return streamed(index, checkFails(source.ErrUnavailable))()
			},
			status: http.StatusInternalServerError, message: "internal error",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var w *overlayWorld
			var tune []func(*overlayConfig)
			if tc.sources {
				tune = append(tune, withSources(func(
					context.Context, source.Request, source.Permit,
				) (source.Result, error) {
					return tc.answer(w)
				}))
			}
			w = newOverlayWorld(t, tune...)
			w.debRepos(repo.Config{Key: "deb-remote", Class: repo.Remote})
			rec := w.get("deb-remote", debPackages, "admin")
			if tc.status != http.StatusOK {
				wantError(t, rec, tc.status, tc.message)
				return
			}
			if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), index) {
				t.Errorf("answered %d %q, want the cached index", rec.Code, rec.Body)
			}
		})
	}
}
