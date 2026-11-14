package debian_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"
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
// anonymous caller 401 before the upstream is asked (DV-057).
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
						queryRefusal(t, r, http.StatusForbidden, "Forbidden")
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
// without read permission is refused 403 and an anonymous caller 401. A
// Packages index's by-hash copy is a kept case: the index route served it
// on the alias before this change, and answers a miss "Not Found", as it
// did.
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
			miss := "Could not find resource"
			if strings.Contains(p, "/binary-amd64/by-hash/") {
				miss = "Not Found"
			}
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
					queryRefusal(t, r, http.StatusForbidden, "Forbidden")
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
