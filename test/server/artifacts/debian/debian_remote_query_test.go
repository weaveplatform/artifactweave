package debian_test

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// requestLog is an upstream Debian archive that serves fixed files by path
// and records the request URI of every GET, query and matrix parameters
// included, so a test sees exactly what a remote repository asked for.
type requestLog struct {
	*httptest.Server
	mu   sync.Mutex
	uris []string
}

func newRequestLog(t *testing.T, files map[string][]byte) *requestLog {
	t.Helper()
	l := &requestLog{}
	r := chi.NewRouter()
	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		l.uris = append(l.uris, r.URL.RequestURI())
		l.mu.Unlock()
		body, ok := files[chi.URLParam(r, "*")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	})
	l.Server = httptest.NewServer(r)
	t.Cleanup(l.Close)
	return l
}

// take returns the request URIs recorded since the last take.
func (l *requestLog) take() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.uris
	l.uris = nil
	return out
}

// queryArchive is the upstream archive of the query tests: a Packages index
// and its gzipped form, a Sources index and a Contents index's by-hash copy.
func queryArchive(t *testing.T) (map[string][]byte, []string) {
	t.Helper()
	deb := repopath.Read(t, "internal/artifacts/ecosystem/debian/testdata/alpha_1.0_amd64_0.deb")
	index := []byte(stanza("pool/main/a/alpha/alpha_1.0_amd64.deb", deb))
	contents := []byte("usr/bin/alpha\tutils/alpha\n")
	files := map[string][]byte{
		"dists/stable/main/binary-amd64/Packages":    index,
		"dists/stable/main/binary-amd64/Packages.gz": debianForm(t, index, ".gz"),
		"dists/stable/main/source/Sources": []byte(
			"Package: alpha\nVersion: 1.0-1\nDirectory: pool/main/a/alpha\nFiles:\n " +
				strings.Repeat("b", 32) + " 1 alpha_1.0-1.dsc\nChecksums-Sha256:\n " +
				strings.Repeat("c", 64) + " 1 alpha_1.0-1.dsc\n\n"),
		fmt.Sprintf("dists/stable/main/by-hash/SHA256/%x", sha256.Sum256(contents)): contents,
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	return files, paths
}

// queryRemote is a remote Debian repository over an upstream that serves
// files, and the virtual repository deb-virtual over it, which reader may
// read too.
func queryRemote(t *testing.T, files map[string][]byte) (*harness.RemoteWorld, *requestLog) {
	t.Helper()
	up := newRequestLog(t, files)
	w := harness.NewRemoteWorld(t)
	w.Repo.PackageType = repo.Debian
	w.Repo.URL = up.URL
	w.Update()
	if err := w.Runtime.Store.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
		Key: "deb-virtual", Class: repo.Virtual, PackageType: repo.Debian,
		Repositories: []string{"remote"},
	})); err != nil {
		t.Fatal(err)
	}
	if err := w.Runtime.Store.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "virtual-read", Resources: map[api.ResourceType]accesstest.Resource{
			api.ResourceArtifact: {
				Actions: accesstest.Grants{Users: map[string]api.Actions{"reader": api.ActionRead}},
				Targets: map[string]accesstest.Target{"deb-virtual": {}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return w, up
}

// TestAccDebianRemoteIndexServesAQueryAsWithoutOne (#1349): a remote
// Debian repository's Packages index, its gzipped form, its Sources index
// and a by-hash file, asked for with a query, answer as without one, where
// each answered 404 "Could not find resource" without asking the upstream
// before (the by-hash file only for a query other than skipUpdateStats).
// The upstream is asked for the path alone: 6.20's HttpRepo appends a
// request's query to the upstream URL only when Propagate Query Params is
// set, which JFrog's Remote Repositories page lists for a Generic remote
// only, and RTFACT-18414 and RTFACT-26349 show 7.x reading a remote Debian
// file through that same download path. The file is cached under its path,
// so the next caller with a query is served from the cache. A reader and an
// administrator get the upstream's bytes; a user without read permission is
// refused 403 and an anonymous caller 401 before the upstream is asked
// (DV-057). A virtual repository over the remote serves its Sources index
// and by-hash file the same way, where it passed the remote over before.
func TestAccDebianRemoteIndexServesAQueryAsWithoutOne(t *testing.T) {
	files, paths := queryArchive(t)
	for _, key := range []string{"remote", "deb-virtual"} {
		for _, p := range paths {
			if key == "deb-virtual" && strings.Contains(p, "/Packages") {
				continue // a virtual composes its Packages index (#1330)
			}
			for _, query := range []string{"?skipUpdateStats=true", "?unknown=1"} {
				t.Run(key+"/"+p+query, func(t *testing.T) {
					w, up := queryRemote(t, files)
					target := "/artifactory/" + key + "/" + p + query
					for _, caller := range []string{"denied", "", "reader", "admin"} {
						r := w.Request(http.MethodGet, target, caller, "")
						switch caller {
						case "denied":
							queryRefusal(t, r, http.StatusForbidden, "Forbidden")
						case "":
							queryRefusal(
								t,
								r,
								http.StatusUnauthorized,
								"Authentication is required",
							)
						default:
							if r.Code != http.StatusOK || r.Body.String() != string(files[p]) {
								t.Errorf("%s answered %d %q, want 200 and the upstream's bytes",
									caller, r.Code, r.Body)
							}
						}
						want := []string(nil)
						if caller == "reader" {
							want = []string{"/" + p}
						}
						if got := up.take(); !slices.Equal(got, want) {
							t.Errorf("%s: the upstream was asked %q, want %q", caller, got, want)
						}
					}
				})
			}
		}
	}
}

// TestAccDebianRemoteIndexKeepsRefusingAMatrixParameter (#1349): 7.x
// appends a request's matrix parameters to a remote repository's upstream
// URL (RTFACT-26349), which artifactweave's remote read does not do for any
// format, so a remote Debian index asked for with one still answers 404
// "Could not find resource" to a caller who may read it, without asking the
// upstream, and a virtual over it passes it over for its Sources index.
func TestAccDebianRemoteIndexKeepsRefusingAMatrixParameter(t *testing.T) {
	files, paths := queryArchive(t)
	w, up := queryRemote(t, files)
	for _, p := range paths {
		if strings.Contains(p, "/by-hash/") {
			continue // a by-hash file is any other remote file (#1330)
		}
		for _, key := range []string{"remote", "deb-virtual"} {
			if key == "deb-virtual" && strings.Contains(p, "/Packages") {
				continue // a virtual composes its Packages index (#1330)
			}
			r := w.Request(http.MethodGet, "/artifactory/"+key+"/"+p+";x=1", "reader", "")
			queryRefusal(t, r, http.StatusNotFound, "Could not find resource")
			if got := up.take(); len(got) != 0 {
				t.Errorf("%s %s;x=1 asked the upstream for %q", key, p, got)
			}
		}
	}
}
