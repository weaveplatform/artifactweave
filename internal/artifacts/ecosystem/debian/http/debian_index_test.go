package debianhttp

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// zeros reads as an endless run of zero bytes, an upstream body that
// never ends.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// TestAccDebianIndexRefusals: a Packages or Sources index asked for by a
// caller who may not read it is refused first, whatever the repository's
// settings (DV-057); and one in a blacked-out repository, or whose own path
// the patterns reject, answers the download policy's 404 and message before
// it is read, an administrator included.
func TestAccDebianIndexRefusals(t *testing.T) {
	w := newOverlayWorld(t)
	w.debRepos(
		repo.Config{Key: "deb-local", Class: repo.Local},
		repo.Config{Key: "deb-dark", Class: repo.Local, BlackedOut: new(true)},
		repo.Config{Key: "deb-hidden", Class: repo.Local, ExcludesPattern: new("**/Packages*")},
	)
	for _, key := range []string{"deb-local", "deb-dark", "deb-hidden"} {
		w.publish(key, packagesIndex("alpha 1.0 "+debAlpha))
	}
	for _, tc := range []struct {
		name, key, path, as string
		status              int
		message             string
	}{
		{
			"caller who may not read", "deb-local", debPackages, "user",
			http.StatusForbidden, "Forbidden",
		},
		{
			"caller who may not read a blacked-out index", "deb-dark", debPackages, "user",
			http.StatusForbidden, "Forbidden",
		},
		{
			"blacked-out Packages", "deb-dark", debPackages, "admin",
			http.StatusNotFound, "The repository 'deb-dark' is blacked out and cannot serve " +
				"artifact 'deb-dark:" + debPackages + "'.",
		},
		{
			"blacked-out Sources", "deb-dark", debSources, "admin",
			http.StatusNotFound, "The repository 'deb-dark' is blacked out and cannot serve " +
				"artifact 'deb-dark:" + debSources + "'.",
		},
		{
			"Packages the patterns reject", "deb-hidden", debPackages, "admin",
			http.StatusNotFound, "The repository 'deb-hidden' rejected the artifact " +
				"'deb-hidden:" + debPackages + "' due to its include/exclude pattern settings.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantError(t, w.get(tc.key, tc.path, tc.as), tc.status, tc.message)
		})
	}
}

// TestAccDebianIndexNeedsReadPermissionOnEveryListedPackage: a caller who
// may read an index but not a package it lists is refused the index, the
// permission rule the generic item route cannot apply.
func TestAccDebianIndexNeedsReadPermissionOnEveryListedPackage(t *testing.T) {
	w := newOverlayWorld(t)
	w.debRepos(repo.Config{Key: "deb-local", Class: repo.Local})
	w.publish("deb-local", packagesIndex("alpha 1.0 "+debAlpha))
	indexOnly := &accessapitest.Decider{Admin: true, AllowsFunc: func(
		_ accessapi.ResourceType, _ accessapi.Repo, path string, _ accessapi.Actions,
	) bool {
		return !strings.HasPrefix(path, "pool/")
	}}
	r := w.request("deb-local", debPackages, "reader")
	r = r.WithContext(
		accessapi.WithCaller(r.Context(), kernel.Principal{Name: "reader"}, indexOnly),
	)
	rec := httptest.NewRecorder()
	w.kit.GetItem(rec, r)
	wantError(t, rec, http.StatusForbidden, "Forbidden")
}

// TestAccDebianIndexKeepsAPackageThePatternsReject: a local or remote
// Packages index is served unchanged, listing a package whose file the
// repository's patterns reject, as 7.x keeps such a package in its Debian
// metadata (RTFACT-30830); only that file's GET answers the policy's 404.
func TestAccDebianIndexKeepsAPackageThePatternsReject(t *testing.T) {
	index := packagesIndex("alpha 1.0 "+debAlpha, "beta 1.0 "+debBeta)
	w := newOverlayWorld(t, withSources(upstream(map[string]func() (source.Result, error){
		debPackages: streamed(index, nil),
		debBeta:     streamed(debBody, nil),
	})))
	w.debRepos(
		repo.Config{Key: "deb-local", Class: repo.Local, ExcludesPattern: new("pool/beta*")},
		repo.Config{Key: "deb-remote", Class: repo.Remote, ExcludesPattern: new("pool/beta*")},
	)
	w.publish("deb-local", index)
	w.put("deb-local", debAlpha, false, debBody)
	w.put("deb-local", debBeta, false, debBody)
	for _, key := range []string{"deb-local", "deb-remote"} {
		t.Run(key, func(t *testing.T) {
			rec := w.get(key, debPackages, "admin")
			if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), index) {
				t.Errorf("Packages answered %d %q, want the index as it is", rec.Code, rec.Body)
			}
			wantError(t, w.get(key, debBeta, "admin"), http.StatusNotFound,
				"The repository '"+key+"' rejected the artifact '"+key+":"+debBeta+
					"' due to its include/exclude pattern settings.")
		})
	}
	if rec := w.get("deb-local", debAlpha, "admin"); rec.Code != http.StatusOK {
		t.Errorf("accepted package answered %d %s, want 200", rec.Code, rec.Body)
	}
}

// TestAccDebianRemoteIndexRefusedByThePolicy: a remote repository's index,
// and the same index through its -cache alias, answers the policy's 404 and
// message naming the remote and the index's path when the remote is blacked
// out or its patterns reject that path, before its source is asked.
func TestAccDebianRemoteIndexRefusedByThePolicy(t *testing.T) {
	w := newOverlayWorld(t, refuseSources(t))
	w.debRepos(
		repo.Config{Key: "deb-dark", Class: repo.Remote, BlackedOut: new(true)},
		repo.Config{Key: "deb-hidden", Class: repo.Remote, ExcludesPattern: new("**/Packages*")},
	)
	blackedOut := "The repository 'deb-dark' is blacked out and cannot serve artifact " +
		"'deb-dark:" + debPackages + "'."
	rejected := "The repository 'deb-hidden' rejected the artifact 'deb-hidden:" + debPackages +
		"' due to its include/exclude pattern settings."
	for _, tc := range []struct{ key, message string }{
		{"deb-dark", blackedOut},
		{"deb-dark-cache", blackedOut},
		{"deb-hidden", rejected},
		{"deb-hidden-cache", rejected},
	} {
		t.Run(tc.key, func(t *testing.T) {
			wantError(t, w.get(tc.key, debPackages, "admin"), http.StatusNotFound, tc.message)
		})
	}
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
