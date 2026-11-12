package vagranthttp_test

import (
	"bytes"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// boxPath is the escaped path of the box these tests publish, and boxURL
// the provider url the catalog lists for it: the native box file route,
// where 7.x's catalogs point (RTFACT-17993, RTFACT-27264).
const (
	boxPath = "arbitrary/release%20one.box"
	boxURL  = "https://public.example" + base + boxPath
)

const (
	missingResource = "Could not find resource"
	forbidden       = "Forbidden"
	unauthenticated = "Authentication is required"
	invalidName     = "invalid Vagrant box name"
	blackedOut      = "The repository 'boxes' is blacked out and cannot serve artifact 'boxes:%s'."
	rejected        = "The repository 'boxes' rejected the artifact 'boxes:%s' due to its " +
		"include/exclude pattern settings."
)

// send serves r as user, as do serves the request it builds, for a request
// that carries headers.
func (f *fixture) send(r *http.Request, user string) *httptest.ResponseRecorder {
	f.t.Helper()
	p := kernel.Principal{Name: user, Admin: user == "admin"}
	if user == "" {
		p = kernel.Anonymous
	}
	ctx, err := f.kit.Authorize(r.Context(), p, true)
	if err != nil {
		f.t.Fatal(err)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r.WithContext(ctx))
	return w
}

// grant lets user read the paths of boxes that include matches.
func (f *fixture) grant(user, include string) {
	f.t.Helper()
	if err := f.db.Permissions().Create(f.t.Context(), accesstest.Permission{
		Name: user,
		Resources: map[api.ResourceType]accesstest.Resource{
			api.ResourceArtifact: {
				Actions: accesstest.Grants{Users: map[string]api.Actions{user: api.ActionRead}},
				Targets: map[string]accesstest.Target{"boxes": {Include: []string{include}}},
			},
		},
	}); err != nil {
		f.t.Fatal(err)
	}
}

// configure changes the repository's configuration with change.
func (f *fixture) configure(change func(*repo.Config)) {
	f.t.Helper()
	change(&f.c)
	if err := f.db.Repos().Update(f.t.Context(), f.c); err != nil {
		f.t.Fatal(err)
	}
}

// The catalog lists each box at the native box file route, which serves the
// stored box as the item download does: its bytes and checksum headers to
// GET and HEAD, and 206 to a Range, which 7.x ignores on this route, the bug
// RTFACT-13807 and RTFACT-27264 report. A box at the repository root is a
// box file too. Box names of one segment and of two still answer the
// catalog.
func TestAccBoxFileRouteServesTheStoredBox(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) {
			f := newFixture(t, class)
			body := f.publish(boxPath, "acme/widget", "1.0.0", "virtualbox")
			root := f.publish("precise64-virtualbox-1.0.0.box", "precise64", "1.0.0", "virtualbox")
			for name, want := range map[string]string{
				"acme/widget": boxURL,
				"precise64":   "https://public.example" + base + "precise64-virtualbox-1.0.0.box",
			} {
				c := f.catalog(name, "admin")
				if c.Name != name || len(c.Versions) != 1 || len(c.Versions[0].Providers) != 1 ||
					c.Versions[0].Providers[0].URL != want {
					t.Fatalf("catalog %s: %+v, want url %s", name, c, want)
				}
			}

			sha1sum, sha256sum, md5sum := sha1.Sum(body), sha256.Sum256(body), md5.Sum(body)
			headers := map[string]string{
				"Content-Length":         strconv.Itoa(len(body)),
				"Accept-Ranges":          "bytes",
				"X-Checksum-Sha1":        hex.EncodeToString(sha1sum[:]),
				"X-Checksum-Sha256":      hex.EncodeToString(sha256sum[:]),
				"X-Checksum-Md5":         hex.EncodeToString(md5sum[:]),
				"X-Artifactory-Filename": "release one.box",
				"Content-Disposition": `attachment; filename="release one.box"; ` +
					`filename*=UTF-8''release%20one.box`,
			}
			for method, want := range map[string][]byte{http.MethodGet: body, http.MethodHead: nil} {
				w := f.do(method, boxURL, "admin", nil)
				if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), want) {
					t.Fatalf("%s: %d %q", method, w.Code, w.Body.String())
				}
				for key, value := range headers {
					if got := w.Header().Get(key); got != value {
						t.Fatalf("%s %s: %q, want %q", method, key, got, value)
					}
				}
			}

			r := httptest.NewRequest(http.MethodGet, boxURL, nil)
			r.Header.Set("Range", "bytes=0-99")
			w := f.send(r, "admin")
			if w.Code != http.StatusPartialContent || !bytes.Equal(w.Body.Bytes(), body[:100]) ||
				w.Header().Get("Content-Range") != fmt.Sprintf("bytes 0-99/%d", len(body)) {
				t.Fatalf("Range: %d %v %d bytes", w.Code, w.Header(), w.Body.Len())
			}

			w = f.do(http.MethodGet, base+"precise64-virtualbox-1.0.0.box", "admin", nil)
			if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), root) {
				t.Fatalf("root box: %d %q", w.Code, w.Body.String())
			}
		})
	}
}

// A .box path names a box file whatever is stored there, so with no file at
// it, one segment or several, it answers the item download's 404 [I], where
// it answered as a box name before: so does a folder named like a box, and a
// box whose name ends .box, which only matrix parameters, the catalog's
// filter, still reach [I]. A path a box file cannot have keeps its 400, and
// a remote or virtual repository its 501.
func TestAccBoxFileRouteAnswersAPathWithNoBox(t *testing.T) {
	f := newFixture(t, repo.Local)
	f.publish("folder.box/inner.box", "bento.box", "1.0.0", "virtualbox")
	for _, path := range []string{
		"missing.box", "acme/missing.box", "a/b/c/missing.box", "folder.box", "bento.box",
	} {
		wantError(t, f.do(http.MethodGet, base+path, "admin", nil), 404, missingResource)
		if w := f.do(http.MethodHead, base+path, "admin", nil); w.Code != 404 {
			t.Fatalf("HEAD %s: %d", path, w.Code)
		}
	}
	if c := f.catalog("bento.box;box_provider=virtualbox", "admin"); c.Name != "bento.box" ||
		len(c.Versions) != 1 || !strings.HasSuffix(c.Versions[0].Providers[0].URL, "/inner.box") {
		t.Fatalf("matrix catalog: %+v", c)
	}
	wantError(t, f.do(http.MethodGet, base+"latest.box;box_provider=virtualbox", "admin", nil),
		404, "Vagrant box not found")
	for _, path := range []string{
		boxPath + ";box_provider=virtualbox", boxPath + "/", "%2e%2e/x.box",
		strings.Repeat("a", 4093) + ".box",
	} {
		wantError(t, f.do(http.MethodGet, base+path, "admin", nil), 400, invalidName)
	}
	for _, class := range []repo.Class{repo.Remote, repo.Virtual} {
		if w := newFixture(t, class).do(http.MethodGet, base+"a.box", "admin", nil); w.Code != 501 {
			t.Fatalf("%s: %d %s", class, w.Code, w.Body.String())
		}
	}
}

// Read permission on the path comes before the lookup (DV-057): a caller who
// may not read it gets 403, or 401 when anonymous, whether or not a box is
// stored there. The query is the item download's: an access_token in it
// authenticates nothing (DV-148), and it refuses nothing.
func TestAccBoxFileRouteChecksReadPermissionFirst(t *testing.T) {
	f := newFixture(t, repo.Local)
	visible := f.publish("visible/a.box", "widget", "1.0", "virtualbox")
	f.publish("hidden/a.box", "widget", "2.0", "virtualbox")
	f.grant("reader", "visible/**")
	for _, path := range []string{"visible/a.box", "visible/a.box?skipUpdateStats=true"} {
		if w := f.do(http.MethodGet, base+path, "reader", nil); w.Code != 200 ||
			!bytes.Equal(w.Body.Bytes(), visible) {
			t.Fatalf("%s: %d %q", path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"hidden/a.box", "hidden/missing.box"} {
		wantError(t, f.do(http.MethodGet, base+path, "reader", nil), 403, forbidden)
		wantError(t, f.do(http.MethodGet, base+path, "outsider", nil), 403, forbidden)
		wantError(t, f.do(http.MethodGet, base+path, "", nil), 401, unauthenticated)
		if w := f.do(http.MethodHead, base+path, "reader", nil); w.Code != 403 {
			t.Fatalf("HEAD %s: %d", path, w.Code)
		}
	}
	wantError(t, f.do(http.MethodGet, base+"visible/a.box?access_token=x", "", nil),
		401, unauthenticated)
}

// The download policy follows read permission (R9, R10): a blacked-out
// repository refuses a box file with its 404, and patterns that reject the
// path with theirs, whether or not a box is stored there and to an
// administrator too, while a caller who may not read the path gets 403
// first. The catalog keeps leaving out a box the patterns reject, until P19,
// and refusing a blacked-out repository's catalog with the blackout's 404
// to a caller who could see a box (#1256).
func TestAccBoxFileRouteFollowsTheDownloadPolicy(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) {
			f := newFixture(t, class)
			body := f.publish(boxPath, "acme/widget", "1.0.0", "virtualbox")
			f.publish("other/b.box", "acme/widget", "2.0.0", "virtualbox")
			f.grant("reader", "other/**")
			const item, missing = "arbitrary/release one.box", "arbitrary/missing.box"

			f.configure(func(c *repo.Config) { c.BlackedOut = new(true) })
			wantError(t, f.do(http.MethodGet, boxURL, "admin", nil),
				404, fmt.Sprintf(blackedOut, item))
			wantError(t, f.do(http.MethodGet, base+missing, "admin", nil),
				404, fmt.Sprintf(blackedOut, missing))
			if w := f.do(http.MethodHead, boxURL, "admin", nil); w.Code != 404 {
				t.Fatalf("HEAD: %d", w.Code)
			}
			wantError(t, f.do(http.MethodGet, boxURL, "reader", nil), 403, forbidden)
			wantError(t, f.do(http.MethodGet, boxURL, "", nil), 401, unauthenticated)
			wantError(t, f.do(http.MethodGet, base+"acme/widget", "reader", nil),
				404, fmt.Sprintf(blackedOut, "acme/widget"))

			f.configure(func(c *repo.Config) {
				c.BlackedOut = nil
				c.ExcludesPattern = new("arbitrary/**")
			})
			wantError(
				t,
				f.do(http.MethodGet, boxURL, "admin", nil),
				404,
				fmt.Sprintf(rejected, item),
			)
			wantError(t, f.do(http.MethodGet, base+missing, "admin", nil),
				404, fmt.Sprintf(rejected, missing))
			wantError(t, f.do(http.MethodGet, boxURL, "reader", nil), 403, forbidden)
			if c := f.catalog("acme/widget", "admin"); len(c.Versions) != 1 ||
				c.Versions[0].Version != "2.0.0" {
				t.Fatalf("catalog under patterns: %+v", c)
			}

			f.configure(func(c *repo.Config) {
				c.ExcludesPattern = nil
				c.IncludesPattern = new("other/**")
			})
			wantError(
				t,
				f.do(http.MethodGet, boxURL, "admin", nil),
				404,
				fmt.Sprintf(rejected, item),
			)

			f.configure(
				func(c *repo.Config) { c.IncludesPattern = new(repo.DefaultIncludesPattern) },
			)
			if w := f.do(http.MethodGet, boxURL, "admin", nil); w.Code != 200 ||
				!bytes.Equal(w.Body.Bytes(), body) {
				t.Fatalf("restored: %d %q", w.Code, w.Body.String())
			}
		})
	}
}
