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
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/debian"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraform/terraformtest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
	"github.com/weaveplatform/artifactweave/test/server/support"
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

// queryArchive is the upstream archive of the query tests: a Release file,
// its signed InRelease form, a Packages index, its gzipped form and its
// by-hash copy, a Sources index and a Contents index's by-hash copy.
func queryArchive(t *testing.T) (map[string][]byte, []string) {
	t.Helper()
	deb := repopath.Read(t, "internal/artifacts/ecosystem/debian/testdata/alpha_1.0_amd64_0.deb")
	index := []byte(stanza("pool/main/a/alpha/alpha_1.0_amd64.deb", deb))
	contents := []byte("usr/bin/alpha\tutils/alpha\n")
	release, err := debian.Release("stable", []string{"main"}, []string{"amd64"},
		[]debian.ReleaseEntry{
			{Path: "main/binary-amd64/Packages", Body: index},
			{Path: "main/binary-amd64/Packages.gz", Body: debianForm(t, index, ".gz")},
		}, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	key, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	signed, err := debian.SignRelease(release, key.Private, "")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"dists/stable/Release":                       release,
		"dists/stable/InRelease":                     signed.InRelease,
		"dists/stable/main/binary-amd64/Packages":    index,
		"dists/stable/main/binary-amd64/Packages.gz": debianForm(t, index, ".gz"),
		fmt.Sprintf("dists/stable/main/binary-amd64/by-hash/SHA256/%x", sha256.Sum256(index)): index,
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
// Debian repository's Release and InRelease files, its Packages index, its
// gzipped form, its Sources index and a by-hash file, asked for with a
// query, answer as without one. Before, an index or a by-hash file answered
// 404 "Could not find resource" without asking the upstream (a by-hash file
// only for a query other than skipUpdateStats), a Release or InRelease
// file 400 "Bad Request" for such a query, and the Packages index's by-hash
// copy 403 to every caller, with a query or without one, once it was
// fetched (its listed packages were checked on the copy's own path).
// The upstream is asked for the path alone: 6.20's HttpRepo appends a
// request's query to the upstream URL only when Propagate Query Params is
// set, which JFrog's Remote Repositories page lists for a Generic remote
// only, and RTFACT-18414 and RTFACT-26349 show 7.x reading a remote Debian
// file through that same download path. The file is cached under its path,
// so the next caller with a query is served from the cache. A reader and an
// administrator get the upstream's bytes; a user without read permission is
// refused 403 and an anonymous caller 401 before the upstream is asked
// (DV-057). A virtual repository over the remote serves its Sources index
// and Contents by-hash file the same way, where it passed the remote over
// before.
func TestAccDebianRemoteIndexServesAQueryAsWithoutOne(t *testing.T) {
	files, paths := queryArchive(t)
	for _, key := range []string{"remote", "deb-virtual"} {
		for _, p := range paths {
			if _, index := debian.ByHashMetadataPath(p); key == "deb-virtual" &&
				(index || debian.ReleaseMetadataPath(p) || strings.Contains(p, "/Packages")) {
				continue // a virtual composes these (#1330)
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

// TestAccDebianRemoteIndexPropagatesAQueryWhenSet (#1349): a remote
// repository with Propagate Query Params set asks the upstream for a
// Release file or an index with the request's query, as 6.20's HttpRepo
// appends it then, and caches the answer apart from the one without it.
func TestAccDebianRemoteIndexPropagatesAQueryWhenSet(t *testing.T) {
	files, _ := queryArchive(t)
	w, up := queryRemote(t, files)
	w.Repo.PropagateQueryParams = new(true)
	w.Update()
	for _, p := range []string{
		"dists/stable/Release", "dists/stable/main/binary-amd64/Packages.gz",
		"dists/stable/main/source/Sources",
	} {
		for _, target := range []string{p + "?unknown=1", p} {
			r := w.Request(http.MethodGet, "/artifactory/remote/"+target, "reader", "")
			if r.Code != http.StatusOK || r.Body.String() != string(files[p]) {
				t.Errorf("%s answered %d %q, want 200 and the upstream's bytes",
					target, r.Code, r.Body)
			}
			if got := up.take(); !slices.Equal(got, []string{"/" + target}) {
				t.Errorf("%s: the upstream was asked %q, want %q", target, got, "/"+target)
			}
		}
	}
}

// TestAccDebianRemoteIndexByHashCopyNeedsReadOnEveryListedPackage: a
// remote Debian repository's Packages and Sources indexes' by-hash copies
// are served, once fetched, to a caller who may read every package they
// list, from the cache and its -cache alias, where every caller, an
// administrator too, was refused 403 once the copy was fetched (found while
// fixing #1349). A caller who may read the remote but not the packages is
// refused the copy as the index itself, 403.
func TestAccDebianRemoteIndexByHashCopyNeedsReadOnEveryListedPackage(t *testing.T) {
	files, _ := queryArchive(t)
	sources := files["dists/stable/main/source/Sources"]
	byHash := map[string][]byte{
		"dists/stable/main/binary-amd64/Packages": files["dists/stable/main/binary-amd64/Packages"],
		"dists/stable/main/source/Sources":        sources,
	}
	files[fmt.Sprintf("dists/stable/main/source/by-hash/SHA256/%x", sha256.Sum256(sources))] = sources
	w, up := queryRemote(t, files)
	if err := w.Runtime.Store.Users().Create(t.Context(), accesstest.User{
		Name: "indexonly", PasswordHash: support.CheapHash,
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.Runtime.Store.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "index-only", Resources: map[api.ResourceType]accesstest.Resource{
			api.ResourceArtifact: {
				Actions: accesstest.Grants{
					Users: map[string]api.Actions{"indexonly": api.ActionRead},
				},
				Targets: map[string]accesstest.Target{"remote": {
					Include: []string{"**"}, Exclude: []string{"pool/**"},
				}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	for index, body := range byHash {
		copyPath := fmt.Sprintf("%s/by-hash/SHA256/%x", index[:strings.LastIndex(index, "/")],
			sha256.Sum256(body))
		t.Run(copyPath, func(t *testing.T) {
			up.take() // what an earlier subtest's refused caller fetched
			for _, caller := range []string{"reader", "admin"} {
				for _, key := range []string{"remote", "remote-cache"} {
					r := w.Request(http.MethodGet, "/artifactory/"+key+"/"+copyPath, caller, "")
					if r.Code != http.StatusOK || r.Body.String() != string(body) {
						t.Errorf("%s %s answered %d %q, want 200 and the upstream's bytes",
							caller, key, r.Code, r.Body)
					}
				}
			}
			if got := up.take(); !slices.Equal(got, []string{"/" + copyPath}) {
				t.Errorf("the upstream was asked %q, want %q once", got, "/"+copyPath)
			}
			for _, target := range []string{copyPath, index} {
				queryRefusal(t, w.Request(http.MethodGet, "/artifactory/remote/"+target,
					"indexonly", ""), http.StatusForbidden, "Forbidden")
			}
		})
	}
}
