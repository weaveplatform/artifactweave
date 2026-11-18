package debian_test

import (
	"bytes"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// componentFiles are files an upstream Debian archive publishes under
// dists/ that a local repository would derive: a component's Release in a
// binary-<arch> and a source folder, a Packages.diff/Index, and the Release
// of a distribution named with a slash.
var componentFiles = map[string][]byte{
	"dists/stable/main/binary-amd64/Release": []byte(
		"Archive: stable\nComponent: main\nArchitecture: amd64\n"),
	"dists/stable/main/binary-amd64/Packages.diff/Index": []byte("SHA256-Current: " +
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 0\n"),
	"dists/stable/main/source/Release": []byte(
		"Archive: stable\nComponent: main\nArchitecture: source\n"),
	"dists/buster/updates/Release": []byte("Suite: buster\nCodename: buster\n"),
}

// TestAccDebianRemoteServesComponentFiles (#1375): a remote Debian
// repository asks its upstream for a component's Release, a
// Packages.diff/Index and a slash distribution's Release, which it
// answered 404 "Not Found" without asking, and a virtual repository over
// it serves them, where it answered 404 "Could not find resource". JFrog's
// Debian guide has a remote's index files "stored and renewed according
// to the Retrieval Cache Period", so the first reader's GET asks the
// upstream and the next GET, on the remote or the virtual, is served from
// the cache. A user without read permission is refused 403 and an
// anonymous caller 401 before the upstream is asked (DV-057), the user on
// the remote with 7.x's download answer (#1572).
func TestAccDebianRemoteServesComponentFiles(t *testing.T) {
	files, _ := queryArchive(t)
	for p, body := range componentFiles {
		files[p] = body
	}
	for p, body := range componentFiles {
		t.Run(p, func(t *testing.T) {
			w, up := queryRemote(t, files)
			for _, key := range []string{"remote", "deb-virtual"} {
				target := "/artifactory/" + key + "/" + p
				for _, caller := range []string{"denied", "", "reader", "admin"} {
					r := w.Request(http.MethodGet, target, caller, "")
					switch caller {
					case "denied":
						refusal := "Forbidden"
						if key == "remote" {
							refusal = remoteRefused(key, p, caller)
						}
						queryRefusal(t, r, http.StatusForbidden, refusal)
					case "":
						queryRefusal(t, r, http.StatusUnauthorized, "Authentication is required")
					default:
						if r.Code != http.StatusOK || r.Body.String() != string(body) {
							t.Errorf("%s on %s answered %d %q, want 200 and the upstream's bytes",
								caller, key, r.Code, r.Body)
						}
					}
					want := []string(nil)
					if caller == "reader" && key == "remote" {
						want = []string{"/" + p}
					}
					if got := up.take(); !slices.Equal(got, want) {
						t.Errorf("%s on %s: the upstream was asked %q, want %q",
							caller, key, got, want)
					}
				}
			}
		})
	}
}

// TestAccDebianRemoteCacheAliasServesCachedMetadata (#1382): once a remote
// Debian repository has fetched a Release, an InRelease, an installer's
// Packages index, a component's Release or a Contents index's by-hash
// copy, its -cache alias serves the cached bytes without asking the
// upstream, where it answered 404 "Could not find resource": JFrog's
// Remote Repositories page reads what is "already stored in the cache" at
// <remote-repository-name>-cache "to avoid remote update checks". Before
// the remote's GET the alias still answers 404 and asks nothing. A user
// without read permission is refused 403 and an anonymous caller 401; the
// miss and the user's refusal with 7.x's download answers (#1572). A
// Packages index's by-hash copy, which the Debian overlay serves on the
// alias, answers them too (#1599), where it answered a miss "Not Found" and
// a user "Forbidden".
func TestAccDebianRemoteCacheAliasServesCachedMetadata(t *testing.T) {
	files, _ := queryArchive(t)
	const installer = "dists/stable/main/debian-installer/binary-amd64/Packages.gz"
	files[installer] = debianForm(t, []byte(stanza("pool/main/a/alpha/alpha_1.0_amd64.udeb",
		[]byte("udeb"))), ".gz")
	const component = "dists/stable/main/binary-amd64/Release"
	files[component] = componentFiles[component]
	paths := []string{"dists/stable/Release", "dists/stable/InRelease", installer, component}
	for p := range files {
		if strings.Contains(p, "/by-hash/") {
			paths = append(paths, p)
		}
	}
	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			w, up := queryRemote(t, files)
			alias := "/artifactory/remote-cache/" + p
			miss := "File not found.; Path: 'remote-cache:" + p + "'"
			refusal := remoteRefused("remote-cache", p, "denied")
			queryRefusal(
				t,
				w.Request(http.MethodGet, alias, "admin", ""),
				http.StatusNotFound,
				miss,
			)
			if got := up.take(); got != nil {
				t.Errorf("the alias asked the upstream %q before anything was cached", got)
			}
			r := w.Request(http.MethodGet, "/artifactory/remote/"+p, "reader", "")
			if r.Code != http.StatusOK || r.Body.String() != string(files[p]) {
				t.Fatalf("remote answered %d %q, want 200 and the upstream's bytes", r.Code, r.Body)
			}
			if got := up.take(); !slices.Equal(got, []string{"/" + p}) {
				t.Errorf("the remote asked the upstream %q, want %q", got, "/"+p)
			}
			for _, caller := range []string{"denied", "", "reader", "admin"} {
				r := w.Request(http.MethodGet, alias, caller, "")
				switch caller {
				case "denied":
					queryRefusal(t, r, http.StatusForbidden, refusal)
				case "":
					queryRefusal(t, r, http.StatusUnauthorized, "Authentication is required")
				default:
					if r.Code != http.StatusOK || r.Body.String() != string(files[p]) {
						t.Errorf("%s on the alias answered %d %q, want 200 and the cached bytes",
							caller, r.Code, r.Body)
					}
				}
				if got := up.take(); got != nil {
					t.Errorf("%s on the alias asked the upstream %q", caller, got)
				}
			}
		})
	}
}

// These native index routes prepare their own source requests rather than using
// the ordinary item-resource builder. They must retain repository-file origin
// misses while API metadata continues to answer its upstream's miss.
func TestAccDebianExpiredMetadataRetainsRepositoryFiles(t *testing.T) {
	files, _ := queryArchive(t)
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var mutate []func(*config.Config)
			if backend == "postgres" {
				dsn := sqltest.ScratchDSN(t, sqltest.PostgresDSN(t))
				mutate = append(mutate, func(c *config.Config) { c.Database = config.Database{Driver: "postgres", DSN: dsn} })
			}
			w := harness.NewRemoteWorld(t, mutate...)
			w.Repo.PackageType = repo.Debian
			w.Repo.RetrievalCachePeriodSecs = new(0)
			w.Update()
			for _, name := range []string{queryRelease, queryPackages} {
				body := files[name]
				w.Upstream.Set("/"+name, fakeupstream.Resource{Body: body})
				before := w.Request(http.MethodGet, "/artifactory/remote/"+name, "admin", "")
				if before.Code != http.StatusOK || !bytes.Equal(before.Body.Bytes(), body) {
					t.Fatal(name, before.Code, before.Body.String())
				}
				w.Upstream.Set("/"+name, fakeupstream.Resource{Status: http.StatusNotFound})
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					after := w.Request(method, "/artifactory/remote/"+name, "admin", "")
					if after.Code != http.StatusOK || (method == http.MethodGet && !bytes.Equal(after.Body.Bytes(), body)) {
						t.Fatal("physical Debian metadata adopted API miss semantics", name, method, after.Code, after.Body.String())
					}
				}
			}
		})
	}
}
