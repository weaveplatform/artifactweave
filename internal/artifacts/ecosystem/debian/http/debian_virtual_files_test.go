package debianhttp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// One file of each kind under dists/ that a virtual repository does not
// compose: a component Release, a Contents index, a translation and an
// installer file, the last at the path of RTFACT-30014's workaround.
const (
	debComponentRelease = "dists/stable/main/binary-amd64/Release"
	debContents         = "dists/stable/main/Contents-amd64.gz"
	debTranslation      = "dists/stable/main/i18n/Translation-en.bz2"
	debInstaller        = "dists/stable/main/installer-amd64/current/images/cdrom/initrd.gz"
)

// The files the Sources index of sourcesIndex lists.
const (
	debDescriptor = "pool/main/a/alpha/alpha_1.0-1.dsc"
	debOrig       = "pool/main/a/alpha/alpha_1.0.orig.tar.gz"
)

// sourcesIndex is a Sources index with one stanza, for the source package
// alpha at version, listing debDescriptor and debOrig.
func sourcesIndex(version string) []byte {
	md5, sha := strings.Repeat("b", 32), strings.Repeat("c", 64)
	return fmt.Appendf(nil, "Package: alpha\nVersion: %s\nDirectory: pool/main/a/alpha\n"+
		"Files:\n %s 1 alpha_1.0-1.dsc\n %s 1 alpha_1.0.orig.tar.gz\n"+
		"Checksums-Sha256:\n %s 1 alpha_1.0-1.dsc\n %s 1 alpha_1.0.orig.tar.gz\n\n",
		version, md5, md5, sha, sha)
}

// getAs serves a GET of path in the repository key through the kit's item
// route as a caller who is not an administrator and whom d answers.
func (w *overlayWorld) getAs(key, path string, d accessapi.Decider) *httptest.ResponseRecorder {
	w.t.Helper()
	r := w.request(key, path, "reader")
	r = r.WithContext(accessapi.WithCaller(r.Context(), kernel.Principal{Name: "reader"}, d))
	rec := httptest.NewRecorder()
	w.kit.GetItem(rec, r)
	return rec
}

// TestAccDebianVirtualServesItsMembersOtherDistsFiles: a virtual repository
// serves a file under dists/ that it does not compose, a component Release,
// a Contents index, a translation, an installer file or a Sources index,
// from a local member that stores it or a remote member whose upstream
// holds it, as the member has it (RTFACT-19086, RTFACT-30014). A Sources
// index is served as text, as the member's own index read serves it.
func TestAccDebianVirtualServesItsMembersOtherDistsFiles(t *testing.T) {
	for _, kind := range []struct {
		name, path string
		body       []byte
	}{
		{"component Release", debComponentRelease, []byte("Archive: stable\nComponent: main\n")},
		{"Contents index", debContents, []byte("contents bytes")},
		{"translation", debTranslation, []byte("translation bytes")},
		{"installer file", debInstaller, []byte("installer bytes")},
		{"Sources index", debSources, sourcesIndex("1.0-1")},
	} {
		for _, class := range []repo.Class{repo.Local, repo.Remote} {
			t.Run(kind.name+" from a "+string(class)+" member", func(t *testing.T) {
				w := newOverlayWorld(t, withSources(upstream(
					map[string]func() (source.Result, error){kind.path: streamed(kind.body, nil)},
				)))
				w.debRepos(
					repo.Config{Key: "deb-member", Class: class},
					repo.Config{
						Key:          "deb-virtual",
						Class:        repo.Virtual,
						Repositories: []string{"deb-member"},
					},
				)
				if class == repo.Local {
					w.put("deb-member", kind.path, false, kind.body)
				}
				rec := w.get("deb-virtual", kind.path, "admin")
				if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), kind.body) {
					t.Fatalf("answered %d %q, want 200 and the member's file", rec.Code, rec.Body)
				}
				if got := rec.Header().Get("Content-Type"); kind.path == debSources &&
					got != "text/plain; charset=utf-8" {
					t.Errorf("Sources served as %q, want text", got)
				}
			})
		}
	}
}

// TestAccDebianVirtualDistsFileFromTheFirstMemberThatServesIt: a virtual
// repository serves a dists/ file it does not compose, a Sources index
// included, from the first member, in its order, that may serve the file
// and holds it. A member that is absent, blacked out, or whose patterns
// reject the file is passed over, as decision 0032 passes over a member
// that refuses (R5), and so are a member that holds a folder there and a
// remote member whose upstream lacks the file, which is asked for each of
// them, an installer's Packages index and a translation's by-hash copy
// among them (#1273).
func TestAccDebianVirtualDistsFileFromTheFirstMemberThatServesIt(t *testing.T) {
	for _, path := range []string{
		debTranslation, debSources,
		"dists/stable/main/debian-installer/binary-amd64/Packages.gz",
		"dists/stable/main/i18n/by-hash/SHA256/" + strings.Repeat("d", 64),
	} {
		t.Run(path, func(t *testing.T) {
			first, second := []byte("first bytes"), []byte("second bytes")
			if path == debSources {
				first, second = sourcesIndex("1.0-1"), sourcesIndex("2.0-1")
			}
			asked := map[string]bool{}
			w := newOverlayWorld(t, withSources(
				func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
					asked[q.Resource.Path] = true
					return source.Result{}, kernel.ErrNotFound
				},
			))
			w.debRepos(
				repo.Config{Key: "deb-dark", Class: repo.Local, BlackedOut: new(true)},
				repo.Config{
					Key: "deb-hiding", Class: repo.Local,
					ExcludesPattern: new(
						"**/Translation-*,**/Sources,**/debian-installer/**,**/by-hash/**",
					),
				},
				repo.Config{Key: "deb-folder", Class: repo.Local},
				repo.Config{Key: "deb-remote", Class: repo.Remote},
				repo.Config{Key: "deb-first", Class: repo.Local},
				repo.Config{Key: "deb-second", Class: repo.Local},
				repo.Config{Key: "deb-virtual", Class: repo.Virtual, Repositories: []string{
					"absent", "deb-dark", "deb-hiding", "deb-folder", "deb-remote",
					"deb-first", "deb-second",
				}},
			)
			w.put("deb-dark", path, false, second)
			w.put("deb-hiding", path, false, second)
			w.folderAt("deb-folder", path)
			w.put("deb-first", path, false, first)
			w.put("deb-second", path, false, second)
			rec := w.get("deb-virtual", path, "admin")
			if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), first) {
				t.Errorf("answered %d %q, want 200 and deb-first's file", rec.Code, rec.Body)
			}
			if !asked[path] {
				t.Error("deb-remote's upstream was not asked")
			}
		})
	}
}

// TestAccDebianVirtualDistsFileRefusals: a dists/ file the virtual
// repository does not compose is refused to a caller who may not read it,
// before the virtual repository's patterns are asked (DV-057); patterns
// that reject it answer that no repository serves it; a remote member's
// upstream fault answers 502; and a file no member holds is not found.
func TestAccDebianVirtualDistsFileRefusals(t *testing.T) {
	w := newOverlayWorld(t, withSources(upstream(map[string]func() (source.Result, error){
		debInstaller: failing(source.ErrUnavailable),
	})))
	w.debRepos(
		repo.Config{Key: "deb-local", Class: repo.Local},
		repo.Config{Key: "deb-remote", Class: repo.Remote},
		repo.Config{Key: "deb-virtual", Class: repo.Virtual, Repositories: []string{"deb-local"}},
		repo.Config{
			Key: "deb-closed", Class: repo.Virtual, Repositories: []string{"deb-local"},
			ExcludesPattern: new("dists/**"),
		},
		repo.Config{
			Key: "deb-upstream", Class: repo.Virtual,
			Repositories: []string{"deb-remote", "deb-local"},
		},
	)
	w.put("deb-local", debTranslation, false, []byte("translation bytes"))
	w.put("deb-local", debInstaller, false, []byte("installer bytes"))
	w.put("deb-local", debSources, false, sourcesIndex("1.0-1"))
	for _, tc := range []struct {
		name, key, path, as string
		status              int
		message             string
	}{
		{
			"caller who may not read", "deb-virtual", debTranslation, "user",
			http.StatusForbidden, "Forbidden",
		},
		{
			"caller who may not read a Sources index", "deb-virtual", debSources, "user",
			http.StatusForbidden, "Forbidden",
		},
		{
			"caller who may not read a file the patterns reject", "deb-closed", debTranslation,
			"user", http.StatusForbidden, "Forbidden",
		},
		{
			"file the virtual repository's patterns reject", "deb-closed", debTranslation,
			"admin", http.StatusNotFound,
			"No repository found to serve the request for deb-closed:" + debTranslation,
		},
		{
			"Sources the virtual repository's patterns reject", "deb-closed", debSources,
			"admin", http.StatusNotFound,
			"No repository found to serve the request for deb-closed:" + debSources,
		},
		{
			"remote member's upstream unavailable", "deb-upstream", debInstaller, "admin",
			http.StatusBadGateway, "Bad Gateway",
		},
		{
			"no member holds it", "deb-virtual", debContents, "admin",
			http.StatusNotFound, "Could not find resource",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantError(t, w.get(tc.key, tc.path, tc.as), tc.status, tc.message)
		})
	}
}

// TestAccDebianVirtualSourcesNeedsReadPermissionOnEveryListedFile: a
// member's Sources index reaches a caller through the virtual repository
// only when the caller may read every file it lists, in the member and in
// the virtual repository, and, for a remote member, by its source's check;
// otherwise it is refused, not passed to the next member. A member's index
// that cannot be read, does not parse or is over the size bound, and a
// check the source cannot make, are server faults; an upstream fault
// answers 502.
func TestAccDebianVirtualSourcesNeedsReadPermissionOnEveryListedFile(t *testing.T) {
	index := sourcesIndex("1.0-1")
	checkFails := func(err error) func() func(context.Context) error {
		return func() func(context.Context) error { return failsOnCall(2, err) }
	}
	// notIn refuses the caller the descriptor in the repository key alone.
	notIn := func(key string) accessapi.Decider {
		return &accessapitest.Decider{Admin: true, AllowsFunc: func(
			_ accessapi.ResourceType, r accessapi.Repo, path string, _ accessapi.Actions,
		) bool {
			return r.Key != key || path != debDescriptor
		}}
	}
	for _, tc := range []struct {
		name    string
		tune    []func(*overlayConfig)
		member  repo.Class
		answer  func() (source.Result, error)
		seed    func(*overlayWorld)
		as      accessapi.Decider
		status  int
		message string
	}{
		{
			name: "descriptor the caller may not read in the member", member: repo.Local,
			as: notIn("deb-member"), status: http.StatusForbidden, message: "Forbidden",
		},
		{
			name:   "descriptor the caller may not read in the virtual repository",
			member: repo.Local, as: notIn("deb-virtual"),
			status: http.StatusForbidden, message: "Forbidden",
		},
		{
			name: "listed file the source refuses", member: repo.Remote,
			answer: streamed(index, checkFails(source.ErrDenied)),
			status: http.StatusForbidden, message: "Forbidden",
		},
		{
			name: "listed file the source cannot check", member: repo.Remote,
			answer: streamed(index, checkFails(source.ErrUnavailable)),
			status: http.StatusInternalServerError, message: "internal error",
		},
		{
			name: "Sources that does not parse", member: repo.Local,
			seed: func(w *overlayWorld) {
				w.put("deb-member", debSources, false, []byte("not an index"))
			},
			status: http.StatusInternalServerError, message: "internal error",
		},
		{
			name: "Sources unreadable", member: repo.Local,
			tune:   []func(*overlayConfig){func(c *overlayConfig) { c.Blobs = brokenBlobs{c.Blobs} }},
			status: http.StatusInternalServerError, message: "internal error",
		},
		{
			name: "Sources over the size bound", member: repo.Remote,
			answer: func() (source.Result, error) {
				return source.Result{Stream: &source.Stream{
					Body:           io.NopCloser(io.LimitReader(zeros{}, debianIndexLimit+1)),
					Status:         http.StatusOK,
					Representation: source.Representation{Size: debianIndexLimit + 1},
				}}, nil
			},
			status: http.StatusInternalServerError, message: "internal error",
		},
		{
			name: "remote member's upstream unavailable", member: repo.Remote,
			answer: failing(source.ErrUnavailable),
			status: http.StatusBadGateway, message: "Bad Gateway",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tune := tc.tune
			if tc.answer != nil {
				tune = append(tune, withSources(upstream(
					map[string]func() (source.Result, error){debSources: tc.answer},
				)))
			}
			w := newOverlayWorld(t, tune...)
			w.debRepos(
				repo.Config{Key: "deb-member", Class: tc.member},
				repo.Config{Key: "deb-later", Class: repo.Local},
				repo.Config{
					Key: "deb-virtual", Class: repo.Virtual,
					Repositories: []string{"deb-member", "deb-later"},
				},
			)
			switch {
			case tc.seed != nil:
				tc.seed(w)
			case tc.member == repo.Local:
				w.put("deb-member", debSources, false, index)
			}
			w.put("deb-later", debSources, false, sourcesIndex("2.0-1"))
			as := tc.as
			if as == nil {
				as = &accessapitest.Decider{Admin: true}
			}
			wantError(t, w.getAs("deb-virtual", debSources, as), tc.status, tc.message)
		})
	}
}
