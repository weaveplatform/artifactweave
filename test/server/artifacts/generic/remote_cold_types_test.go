package generic_test

// This file holds the cold read suite of #1507: a download at
// /artifactory/<remote>/<path> of a file the remote's cache does not hold
// asks the upstream for <url>/<path> on a remote of every type, as
// 7.161.26's RemoteRepoBase.getInfo and HttpRepo.retrieveInfo do whatever
// the package type, and as JFrog's remote repositories page says the URL
// "returns the remote artifact and stores it in the cache if it isn't
// stored yet".

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/cargo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/chef"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/gitlfs"
	hexfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/hex"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/pub"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/puppet"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// coldFile is a file the upstream of a remote of type pt holds at path,
// with body as its bytes.
type coldFile struct {
	pt   repo.PackageType
	path string
	body func(t *testing.T) []byte
}

// payload is the body of a file whose type's cache keeps any bytes.
func payload(*testing.T) []byte { return []byte("cold file bytes") }

// gitLFSObject is a Git LFS object and its ObjectPath, which names its
// SHA-256 as the cache of a Git LFS remote requires.
func gitLFSObject() (string, []byte) {
	body := []byte("git lfs object bytes")
	sum := sha256.Sum256(body)
	p, _ := gitlfs.ObjectPath(hex.EncodeToString(sum[:]))
	return p, body
}

// swiftArchive is a Swift source archive holding only its manifest.
func swiftArchive(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	f, err := z.Create("Package.swift")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("// swift-tools-version:5.9\nimport PackageDescription\n" +
		"let package = Package(name: \"Widget\")\n")); err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// coldFiles are a file per remote type whose item route reads a cold file
// through the cache lookup first (#1507), at a path the type's cache
// admits. A Vagrant, Machine Learning or Agent Plugins remote cannot be
// created (repo.PackageType's remote class), and a Nix remote refuses the
// route (TestAccRemoteColdReadKeepsRefusals).
func coldFiles() []coldFile {
	lfsPath, lfsBody := gitLFSObject()
	hexPath, _ := hexfmt.TarballPath("decimal", "2.0.0")
	return []coldFile{
		{repo.NPM, "widget/-/widget-1.0.0.tgz", func(*testing.T) []byte {
			return support.NPMTarballOf("widget", "1.0.0")
		}},
		{repo.PyPI, "packages/ab/cd/widget-1.0.0.tar.gz", payload},
		{repo.Conda, "noarch/widget-1.0.0.txt", payload},
		{repo.Chef, chef.Path("widget", "1.0.0"), func(t *testing.T) []byte {
			return []byte(support.TarGz(t, map[string]string{
				"widget/metadata.json": `{"name":"widget","version":"1.0.0"}`,
			}))
		}},
		{repo.Helm, "widget-1.0.0.tgz", payload},
		{repo.Gems, "docs/widget-1.0.0.txt", payload},
		{repo.NuGet, "widget/widget.1.0.0.nupkg", payload},
		{repo.Go, "example.com/widget/@v/v1.0.0.zip", payload},
		{repo.Cargo, cargo.CratePath("widget", "1.0.0"), func(t *testing.T) []byte {
			return []byte(support.TarGz(t, map[string]string{
				"widget-1.0.0/Cargo.toml": "[package]\nname = \"widget\"\nversion = \"1.0.0\"\n",
			}))
		}},
		{repo.Composer, "acme/widget/widget-1.0.0.zip", payload},
		{repo.Terraform, "acme/widget/aws/1.0.0.zip", payload},
		{repo.Conan, "acme/widget/1.0.0/_/0/export/conanfile.py", payload},
		{repo.Puppet, puppet.Path("acme-widget", "1.0.0"), func(t *testing.T) []byte {
			return repopath.Read(t, "test/acceptance/fixtures/puppet/acme-widget-1.0.0.tar.gz")
		}},
		{repo.Swift, "acme/widget/widget-1.0.0.zip", swiftArchive},
		{repo.Pub, pub.Path("widget_pub", "1.2.3"), func(t *testing.T) []byte {
			return []byte(support.TarGz(t, map[string]string{
				"pubspec.yaml": "name: widget_pub\nversion: 1.2.3\n",
			}))
		}},
		{repo.Hex, hexPath, func(t *testing.T) []byte {
			return repopath.Read(t, "internal/artifacts/ecosystem/hex/testdata/decimal-2.0.0.tar")
		}},
		{repo.CocoaPods, "pods/widget-1.0.0.tar.gz", payload},
		{repo.Bower, "widget/1.0.0/widget.js", payload},
		{repo.GitLFS, lfsPath, func(*testing.T) []byte { return lfsBody }},
		{repo.VCS, "acme/widget/tags/v1.0.0.tar.gz", payload},
		{repo.HuggingFaceML, "models/acme/widget/main/config.json", payload},
		{repo.Ansible, "acme-widget-1.0.0.tar.gz", payload},
		{repo.BazelModules, "modules/widget/1.0.0/MODULE.bazel", payload},
		{repo.LuaRocks, "docs/widget-1.0.0.txt", payload},
		{repo.Docker, "library/widget/1.0.0/manifest.json", payload},
		{repo.OCI, "acme/widget/1.0.0/manifest.json", payload},
		{repo.HelmOCI, "acme/widget/1.0.0/manifest.json", payload},
		{repo.AIEditorExtensions, "acme/widget/1.0.0/widget.vsix", payload},
		{repo.JetBrainsPlugins, "widget/1.0.0/widget.zip", payload},
		{repo.NIMModel, "acme/widget/1.0.0/model.bin", payload},
	}
}

// TestAccRemoteColdReadFetchesEveryType (#1507): on a remote of every type
// that stores artifacts locally, a HEAD and then a GET of a file the cache
// does not hold answer 200 with the upstream's file, and a second GET and
// the file's checksum are answered from the copy kept, with no upstream
// request. These answered 404 "Could not find resource" without asking
// the upstream.
func TestAccRemoteColdReadFetchesEveryType(t *testing.T) {
	for _, tc := range coldFiles() {
		t.Run(string(tc.pt), func(t *testing.T) {
			w := newRemoteWorld(t)
			remoteOfType(t, w, tc.pt, nil)
			body := tc.body(t)
			w.up.Set("/"+tc.path, declaring(body, false))
			head := w.request(http.MethodHead, "/artifactory/remote/"+tc.path, "reader", "")
			remoteStatus(t, head, http.StatusOK)
			if head.Body.Len() != 0 {
				t.Fatal("HEAD body", head.Body)
			}
			for range 2 {
				got := w.request(http.MethodGet, "/artifactory/remote/"+tc.path, "reader", "")
				remoteStatus(t, got, http.StatusOK)
				if !bytes.Equal(got.Body.Bytes(), body) {
					t.Fatal("body", got.Body)
				}
			}
			sum := w.request(http.MethodGet, "/artifactory/remote/"+tc.path+".sha1", "reader", "")
			remoteStatus(t, sum, http.StatusOK)
			if sum.Body.Len() != 40 {
				t.Fatal("checksum", sum.Body)
			}
			if n := w.up.Count(http.MethodGet, "/"+tc.path); n != 1 ||
				w.up.Count(http.MethodHead, "/"+tc.path) != 1 {
				t.Fatal("want one upstream HEAD and GET", n, w.up.Requests())
			}
		})
	}
}

// TestAccRemoteColdReadStreamsWithoutStoring (#1507): a remote of each type
// that stores nothing answers a cold GET and HEAD with the upstream's file
// and keeps nothing, so a second GET asks the upstream again, as a generic
// remote does.
func TestAccRemoteColdReadStreamsWithoutStoring(t *testing.T) {
	for _, tc := range coldFiles() {
		t.Run(string(tc.pt), func(t *testing.T) {
			w := newRemoteWorld(t)
			remoteOfType(t, w, tc.pt, func(c *repo.Config) { c.StoreArtifactsLocally = new(false) })
			body := tc.body(t)
			w.up.Set("/"+tc.path, declaring(body, false))
			remoteStatus(
				t,
				w.request(http.MethodHead, "/artifactory/remote/"+tc.path, "reader", ""),
				http.StatusOK,
			)
			for range 2 {
				got := w.request(http.MethodGet, "/artifactory/remote/"+tc.path, "reader", "")
				remoteStatus(t, got, http.StatusOK)
				if !bytes.Equal(got.Body.Bytes(), body) {
					t.Fatal("body", got.Body)
				}
			}
			if n := w.up.Count(http.MethodGet, "/"+tc.path); n != 2 {
				t.Fatal("want an upstream GET a request", n, w.up.Requests())
			}
			if _, err := w.rt.Store.Items().
				Get(t.Context(), "remote", true, tc.path); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("kept", err)
			}
		})
	}
}

// TestAccRemoteColdChecksumOfEveryType (#1507): on a remote of every type
// that stores artifacts locally, a checksum request for a file the cache
// does not hold takes the answer #1495 gave the types above: under
// pass-thru, the value the upstream declared, from one upstream HEAD;
// under the other policies, 404 "Checksum not found for <file>". Nothing
// is downloaded or kept. These answered 404 "Could not find resource"
// without asking the upstream.
func TestAccRemoteColdChecksumOfEveryType(t *testing.T) {
	for _, tc := range coldFiles() {
		for _, policy := range []repo.RemoteChecksumPolicy{repo.PassThru, repo.GenerateIfAbsent} {
			t.Run(string(tc.pt)+"/"+string(policy), func(t *testing.T) {
				w := newRemoteWorld(t)
				remoteOfType(t, w, tc.pt, func(c *repo.Config) {
					c.RemoteRepoChecksumPolicyType = policy
				})
				w.up.Set("/"+tc.path, declaring(tc.body(t), true))
				sha1 := declared["sha1"].value
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					got := w.request(method, "/artifactory/remote/"+tc.path+".sha1", "reader", "")
					switch {
					case policy != repo.PassThru:
						remoteStatus(t, got, http.StatusNotFound)
						if method == http.MethodGet &&
							got.Body.String() != checksumNotFound(tc.path) {
							t.Fatal(got.Body)
						}
					case method == http.MethodGet:
						remoteStatus(t, got, http.StatusOK)
						if got.Body.String() != sha1 {
							t.Fatal(got.Body)
						}
					default:
						remoteStatus(t, got, http.StatusOK)
					}
				}
				if n := w.up.Count(http.MethodHead, "/"+tc.path); n != 2 {
					t.Fatal("want one upstream HEAD a request", n, w.up.Requests())
				}
				wantKeptNothing(t, w, tc.path)
			})
		}
	}
}

// TestAccRemoteColdReadKeepsRefusals (#1507): the cold read comes after the
// checks a generic remote's download makes and after each type's own
// refusals, which keep their answers without asking the upstream: a caller
// who may not read the path, the download policy (DV-057), a "-cache" key,
// which 7.x reads from the cache alone, a Nix remote's generic download
// and a Hugging Face Xet record, which their formats refuse, an index a
// format builds itself, which a remote reads from its cache alone (Helm's
// index.yaml, RubyGems' specs) [I], and an npm package's document path
// [I]. A miss names its path as 7.x's download does (#1572): the "-cache"
// key's DbStoringRepoMixin "File not found.", and the remote's a miss of
// the upstream's [I].
func TestAccRemoteColdReadKeepsRefusals(t *testing.T) {
	const notFound = `{"errors":[{"message":"Could not find resource","status":404}]}` + "\n"
	missed := func(reason, key, p string) string {
		return `{"errors":[{"message":"` + reason + `; Path: '` + key + `:` + p + `'","status":404}]}` + "\n"
	}
	for _, tc := range []struct {
		name   string
		pt     repo.PackageType
		tune   func(*repo.Config)
		user   string
		path   string
		status int
		body   string
	}{
		{"unreadable", repo.Helm, nil, "denied", "remote/widget-1.0.0.tgz", http.StatusForbidden, ""},
		{
			"excluded", repo.Helm, func(c *repo.Config) { c.ExcludesPattern = new("**") }, "reader",
			"remote/widget-1.0.0.tgz", http.StatusNotFound, "",
		},
		{
			"blacked out", repo.Helm, func(c *repo.Config) { c.BlackedOut = new(true) }, "reader",
			"remote/widget-1.0.0.tgz", http.StatusNotFound, "",
		},
		{
			"cache key", repo.Helm, nil, "reader", "remote-cache/widget-1.0.0.tgz",
			http.StatusNotFound, missed("File not found.", "remote-cache", "widget-1.0.0.tgz"),
		},
		{
			"nix", repo.Nix, nil, "reader", "remote/widget.narinfo", http.StatusBadRequest,
			`{"errors":[{"message":"remote Nix reads require the native binary-cache endpoint",` +
				`"status":400}]}` + "\n",
		},
		{
			"xet", repo.HuggingFaceML, nil, "reader",
			"remote/models--acme--widget/.xet/xorbs/" + hexDigest("xet"), http.StatusNotFound,
			notFound,
		},
		{
			"helm index", repo.Helm, nil, "reader", "remote/index.yaml", http.StatusNotFound,
			missed("Not Found", "remote", "index.yaml"),
		},
		{
			"gems index", repo.Gems, nil, "reader", "remote/specs.4.8.gz", http.StatusNotFound,
			missed("Not Found", "remote", "specs.4.8.gz"),
		},
		{
			"npm document", repo.NPM, nil, "reader", "remote/widget", http.StatusNotFound,
			missed("Not Found", "remote", "widget"),
		},
		{
			"npm scoped document", repo.NPM, nil, "reader", "remote/@acme/widget",
			http.StatusNotFound, missed("Not Found", "remote", "@acme/widget"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newRemoteWorld(t)
			remoteOfType(t, w, tc.pt, tc.tune)
			_, file, _ := strings.Cut(tc.path, "/")
			w.up.Set("/"+file, declaring([]byte("upstream bytes"), true))
			for _, p := range []string{tc.path, tc.path + ".sha1"} {
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					got := w.request(method, "/artifactory/"+p, tc.user, "")
					remoteStatus(t, got, tc.status)
					if method == http.MethodGet && tc.body != "" && p == tc.path &&
						got.Body.String() != tc.body {
						t.Fatal(method, p, got.Body)
					}
				}
			}
			if n := len(w.up.Requests()); n != 0 {
				t.Fatal("asked the upstream", w.up.Requests())
			}
		})
	}
}

// TestAccRemoteColdReadMissAnswersAsGeneric (#1507): a cold file that an
// offline remote of any type reads from its cache alone (R9), or that its
// upstream does not have, answers 404 as on a generic remote, where every
// type but those answered 404 "Could not find resource": 7.x's offline
// text, or "Not Found", with the path (#1572).
func TestAccRemoteColdReadMissAnswersAsGeneric(t *testing.T) {
	for _, pt := range []repo.PackageType{repo.Generic, repo.Helm, repo.PyPI} {
		for _, offline := range []bool{true, false} {
			w := newRemoteWorld(t)
			remoteOfType(t, w, pt, func(c *repo.Config) { c.Offline = new(offline) })
			if offline {
				w.up.Set("/pkg/file.bin", declaring([]byte("upstream bytes"), true))
			}
			notFound := `{"errors":[{"message":"Not Found; Path: 'remote:pkg/file.bin'","status":404}]}` + "\n"
			if offline {
				notFound = `{"errors":[{"message":"remote: is offline, 'remote:pkg/file.bin' is not found ` +
					`at 'pkg/file.bin'.; Path: 'remote:pkg/file.bin'","status":404}]}` + "\n"
			}
			got := w.request(http.MethodGet, "/artifactory/remote/pkg/file.bin", "reader", "")
			remoteStatus(t, got, http.StatusNotFound)
			if got.Body.String() != notFound {
				t.Fatal(pt, offline, got.Body)
			}
			if n := len(w.up.Requests()); offline && n != 0 || !offline && n != 1 {
				t.Fatal(pt, offline, w.up.Requests())
			}
		}
	}
}

// TestAccRemoteColdReadOutsideTheLayoutIsRefusedByTheCache (#1507): a
// remote whose cache admits only its type's layout, such as Cargo's
// crates/<name>/<name>-<version>.crate, fetches a file outside it and
// answers 502 "Bad Gateway", the answer a native route gives for an
// upstream file the cache refuses, keeping nothing; one that stores
// nothing streams it. 7.161.26 checks a layout only for a local
// repository's own deploys (PackageStorageInterceptorImpl.beforeCreate
// skips a cache), so 7.x may store the file [I].
func TestAccRemoteColdReadOutsideTheLayoutIsRefusedByTheCache(t *testing.T) {
	const path = "pkg/file.bin"
	for _, store := range []bool{true, false} {
		w := newRemoteWorld(t)
		remoteOfType(
			t,
			w,
			repo.Cargo,
			func(c *repo.Config) { c.StoreArtifactsLocally = new(store) },
		)
		w.up.Set("/"+path, fakeupstream.Resource{Body: []byte("not a crate")})
		got := w.request(http.MethodGet, "/artifactory/remote/"+path, "reader", "")
		if store {
			remoteStatus(t, got, http.StatusBadGateway)
			if got.Body.String() !=
				`{"errors":[{"message":"Bad Gateway","status":502}]}`+"\n" {
				t.Fatal(got.Body)
			}
		} else {
			remoteStatus(t, got, http.StatusOK)
			if got.Body.String() != "not a crate" {
				t.Fatal(got.Body)
			}
		}
		if _, err := w.rt.Store.Items().
			Get(t.Context(), "remote", true, path); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatal("kept", err)
		}
	}
}

// hexDigest is the hex SHA-256 of s.
func hexDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
