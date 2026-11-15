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
	"fmt"
	"net/http"
	"strings"
	"testing"

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
			// A remote that bypasses HEAD requests, as every AI Editor
			// Extensions remote does (#1515), sends the upstream its GET
			// alone.
			heads := 1
			if w.c.BypassHeadRequests != nil && *w.c.BypassHeadRequests {
				heads = 0
			}
			if n := w.up.Count(http.MethodGet, "/"+tc.path); n != 1 ||
				w.up.Count(http.MethodHead, "/"+tc.path) != heads {
				t.Fatal("want one upstream GET and HEAD", n, heads, w.up.Requests())
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
// [I].
func TestAccRemoteColdReadKeepsRefusals(t *testing.T) {
	const notFound = `{"errors":[{"message":"Could not find resource","status":404}]}` + "\n"
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
			http.StatusNotFound, notFound,
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
		{"helm index", repo.Helm, nil, "reader", "remote/index.yaml", http.StatusNotFound, notFound},
		{"gems index", repo.Gems, nil, "reader", "remote/specs.4.8.gz", http.StatusNotFound, notFound},
		{"npm document", repo.NPM, nil, "reader", "remote/widget", http.StatusNotFound, notFound},
		{
			"npm scoped document", repo.NPM, nil, "reader", "remote/@acme/widget",
			http.StatusNotFound, notFound,
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
// upstream does not have, answers 404 "Not Found" as on a generic remote,
// where every type but those answered 404 "Could not find resource".
func TestAccRemoteColdReadMissAnswersAsGeneric(t *testing.T) {
	const notFound = `{"errors":[{"message":"Not Found","status":404}]}` + "\n"
	for _, pt := range []repo.PackageType{repo.Generic, repo.Helm, repo.PyPI} {
		for _, offline := range []bool{true, false} {
			w := newRemoteWorld(t)
			remoteOfType(t, w, pt, func(c *repo.Config) { c.Offline = new(offline) })
			if offline {
				w.up.Set("/pkg/file.bin", declaring([]byte("upstream bytes"), true))
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

// layoutFile is a file outside the layout of a remote of type pt, whose
// deploy checks that layout: a plain file, and one named like the files
// the layout checks (archive), which the format's inspection reads at a
// path inside it.
type layoutFile struct {
	pt      repo.PackageType
	archive string
}

// layoutTypes are the remote types whose deploy checks the type's layout
// (#1575).
func layoutTypes() []layoutFile {
	return []layoutFile{
		{repo.Chef, "docs/notes.tar.gz"},
		{repo.Cargo, "docs/notes.crate"},
		{repo.Puppet, "docs/notes.tar.gz"},
		{repo.Swift, "docs/notes.zip"},
		{repo.Pub, "docs/notes.tar.gz"},
		{repo.Hex, "docs/notes.tar"},
		{repo.Bower, "docs/bower.json"},
		// An object's name in another object's folder.
		{repo.GitLFS, "objects/00/00/" + hexDigest("not at its path")},
	}
}

// TestAccRemoteColdReadKeepsAFileOutsideTheLayout (#1575): a remote of a
// type whose deploy checks the type's layout, such as Cargo's
// crates/<name>/<name>-<version>.crate, answers a cold HEAD and GET of a
// file outside it with the upstream's file and keeps it as a plain file,
// so a second GET is answered from the cache; one that stores nothing
// streams it and keeps nothing. These answered 502 "Bad Gateway" after
// fetching it, keeping nothing. JFrog's remote repositories page says the
// download "stores it in the cache if it isn't stored yet", and 7.161.26
// checks a layout only for a local repository's own deploys
// (PackageStorageInterceptorImpl.beforeCreate skips a cache) [I].
func TestAccRemoteColdReadKeepsAFileOutsideTheLayout(t *testing.T) {
	for _, tc := range layoutTypes() {
		for _, path := range []string{"pkg/file.bin", tc.archive} {
			for _, store := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/store=%t", tc.pt, path, store), func(t *testing.T) {
					w := newRemoteWorld(t)
					remoteOfType(t, w, tc.pt, func(c *repo.Config) {
						c.StoreArtifactsLocally = new(store)
					})
					body := []byte("not a package")
					w.up.Set("/"+path, declaring(body, false))
					head := w.request(http.MethodHead, "/artifactory/remote/"+path, "reader", "")
					remoteStatus(t, head, http.StatusOK)
					for range 2 {
						got := w.request(http.MethodGet, "/artifactory/remote/"+path, "reader", "")
						remoteStatus(t, got, http.StatusOK)
						if !bytes.Equal(got.Body.Bytes(), body) {
							t.Fatal("body", got.Body)
						}
					}
					it, err := w.rt.Store.Items().Get(t.Context(), "remote", true, path)
					if !store {
						if !errors.Is(err, kernel.ErrNotFound) {
							t.Fatal("kept", err)
						}
						if n := w.up.Count(http.MethodGet, "/"+path); n != 2 {
							t.Fatal("want an upstream GET a request", n, w.up.Requests())
						}
						return
					}
					if err != nil || it.Folder || it.Size != int64(len(body)) {
						t.Fatal("not kept", it, err)
					}
					if n := w.up.Count(http.MethodGet, "/"+path); n != 1 ||
						w.up.Count(http.MethodHead, "/"+path) != 1 {
						t.Fatal("want one upstream HEAD and GET", n, w.up.Requests())
					}
				})
			}
		}
	}
}

// TestAccRemoteColdReadHoldsALayoutFileToItsChecks (#1575): a file the
// upstream serves at a path inside its type's layout still meets every
// check a deploy there meets, an archive's identity and a Git LFS
// object's SHA-256, so one that fails them answers 502 "Bad Gateway",
// the answer a native route gives for an upstream file the cache refuses,
// and is not kept.
func TestAccRemoteColdReadHoldsALayoutFileToItsChecks(t *testing.T) {
	hexPath, _ := hexfmt.TarballPath("other", "2.0.0")
	lfsPath, _ := gitlfs.ObjectPath(hexDigest("other bytes"))
	for _, tc := range []coldFile{
		{repo.Chef, chef.Path("widget", "1.0.0"), func(t *testing.T) []byte {
			return []byte(support.TarGz(t, map[string]string{
				"other/metadata.json": `{"name":"other","version":"1.0.0"}`,
			}))
		}},
		{repo.Cargo, cargo.CratePath("widget", "1.0.0"), func(t *testing.T) []byte {
			return []byte(support.TarGz(t, map[string]string{
				"other-1.0.0/Cargo.toml": "[package]\nname = \"other\"\nversion = \"1.0.0\"\n",
			}))
		}},
		{repo.Puppet, puppet.Path("acme-other", "1.0.0"), func(t *testing.T) []byte {
			return repopath.Read(t, "test/acceptance/fixtures/puppet/acme-widget-1.0.0.tar.gz")
		}},
		{repo.Swift, "acme/widget/widget-1.0.0.zip", payload},
		{repo.Pub, pub.Path("other_pub", "1.2.3"), func(t *testing.T) []byte {
			return []byte(support.TarGz(t, map[string]string{
				"pubspec.yaml": "name: widget_pub\nversion: 1.2.3\n",
			}))
		}},
		{repo.Hex, hexPath, func(t *testing.T) []byte {
			return repopath.Read(t, "internal/artifacts/ecosystem/hex/testdata/decimal-2.0.0.tar")
		}},
		{repo.Bower, "widget/1.0.0/bower.json", func(*testing.T) []byte {
			return []byte(`{"name":"other","version":"1.0.0"}`)
		}},
		{repo.GitLFS, lfsPath, func(*testing.T) []byte { return []byte("git lfs object bytes") }},
	} {
		t.Run(string(tc.pt), func(t *testing.T) {
			w := newRemoteWorld(t)
			remoteOfType(t, w, tc.pt, nil)
			w.up.Set("/"+tc.path, declaring(tc.body(t), false))
			got := w.request(http.MethodGet, "/artifactory/remote/"+tc.path, "reader", "")
			remoteStatus(t, got, http.StatusBadGateway)
			if got.Body.String() != `{"errors":[{"message":"Bad Gateway","status":502}]}`+"\n" {
				t.Fatal(got.Body)
			}
			if _, err := w.rt.Store.Items().
				Get(t.Context(), "remote", true, tc.path); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("kept", err)
			}
		})
	}
}

// TestAccRemoteColdTraceFetchesAsTheDownloadDoes (#1576): a trace,
// <remote>/<path>?trace, of a file the cache does not hold reads it from
// the upstream on a remote of any type, where the download reads it, and
// reports the resource found, kept or streamed, without counting a
// download. On every type but generic it reported the resource not found.
// 7.161.26's RepoFilter answers ?trace by running the download itself
// (doGetOrHeadRequest wraps its response in a TraceLoggingResponse).
func TestAccRemoteColdTraceFetchesAsTheDownloadDoes(t *testing.T) {
	for _, tc := range []coldFile{
		{repo.Maven, "com/acme/widget/1.0/widget-1.0.jar", payload},
		{repo.Debian, "pool/main/w/widget/widget.txt", payload},
		{repo.NPM, "widget/-/widget-1.0.0.tgz", func(*testing.T) []byte {
			return support.NPMTarballOf("widget", "1.0.0")
		}},
		{repo.Helm, "widget-1.0.0.tgz", payload},
	} {
		for _, store := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/store=%t", tc.pt, store), func(t *testing.T) {
				w := newRemoteWorld(t)
				remoteOfType(t, w, tc.pt, func(c *repo.Config) {
					c.StoreArtifactsLocally = new(store)
				})
				w.up.Set("/"+tc.path, declaring(tc.body(t), false))
				got := w.request(http.MethodGet, "/artifactory/remote/"+tc.path+"?trace", "reader", "")
				remoteStatus(t, got, http.StatusOK)
				found := "Returning validated transient resource"
				if store {
					found = "Returning found resource from remote-cache:" + tc.path
				}
				for _, step := range []string{found, "Responding with found resource", "Request succeeded"} {
					if !strings.Contains(got.Body.String(), step) {
						t.Fatal(step, got.Body)
					}
				}
				if n := w.up.Count(http.MethodGet, "/"+tc.path); n != 1 {
					t.Fatal("want one upstream GET", n, w.up.Requests())
				}
				it, err := w.rt.Store.Items().Get(t.Context(), "remote", true, tc.path)
				if !store {
					if !errors.Is(err, kernel.ErrNotFound) {
						t.Fatal("kept", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if stats, err := w.rt.Store.Items().Stats(t.Context(), it.ID); err != nil ||
					stats.DownloadCount != 0 {
					t.Fatal("trace counted a download", stats, err)
				}
			})
		}
	}
}

// TestAccRemoteColdChecksumTraceAnswersAsTheDownload (#1576): a trace of
// the checksum of a file the cache does not hold takes the answer the
// download gives it (#1495), after the same upstream HEAD: under
// pass-thru the checksum is found, and under the other policies the trace
// reports 404 "Checksum not found for <file>". Nothing is downloaded or
// kept. It reported the checksum not found under every policy, without
// asking the upstream.
func TestAccRemoteColdChecksumTraceAnswersAsTheDownload(t *testing.T) {
	for _, pt := range []repo.PackageType{repo.Generic, repo.Helm} {
		for _, policy := range []repo.RemoteChecksumPolicy{repo.PassThru, repo.GenerateIfAbsent} {
			t.Run(string(pt)+"/"+string(policy), func(t *testing.T) {
				w := newRemoteWorld(t)
				remoteOfType(t, w, pt, func(c *repo.Config) {
					c.RemoteRepoChecksumPolicyType = policy
				})
				w.up.Set("/pkg/file.bin", declaring([]byte("upstream bytes"), true))
				got := w.request(
					http.MethodGet, "/artifactory/remote/pkg/file.bin.sha1?trace", "reader", "",
				)
				remoteStatus(t, got, http.StatusOK)
				steps := []string{
					"Request is for a checksum",
					"Responding to checksum request",
					"Request succeeded",
				}
				if policy != repo.PassThru {
					steps = []string{
						"Request is for a checksum",
						"Sending a response with the status '404' and the message " +
							"'Checksum not found for pkg/file.bin'",
					}
				}
				for _, step := range steps {
					if !strings.Contains(got.Body.String(), step) {
						t.Fatal(step, got.Body)
					}
				}
				if n := w.up.Count(http.MethodHead, "/pkg/file.bin"); n != 1 {
					t.Fatal("want one upstream HEAD", n, w.up.Requests())
				}
				wantKeptNothing(t, w, "pkg/file.bin")
			})
		}
	}
}

// TestAccRemoteColdTraceKeepsTheLookupsAnswers (#1576): a trace keeps the
// answer of a download that reads the cache alone, without asking the
// upstream: a "-cache" key (R8) and an npm package's document path [I].
func TestAccRemoteColdTraceKeepsTheLookupsAnswers(t *testing.T) {
	for _, tc := range []struct {
		name string
		pt   repo.PackageType
		path string
	}{
		{"cache key", repo.Helm, "remote-cache/widget-1.0.0.tgz"},
		{"npm document", repo.NPM, "remote/widget"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newRemoteWorld(t)
			remoteOfType(t, w, tc.pt, nil)
			_, file, _ := strings.Cut(tc.path, "/")
			w.up.Set("/"+file, declaring([]byte("upstream bytes"), true))
			got := w.request(http.MethodGet, "/artifactory/"+tc.path+"?trace", "reader", "")
			remoteStatus(t, got, http.StatusOK)
			if !strings.Contains(got.Body.String(), "Sending a response with the status '404'") {
				t.Fatal(got.Body)
			}
			if n := len(w.up.Requests()); n != 0 {
				t.Fatal("asked the upstream", w.up.Requests())
			}
		})
	}
}

// hexDigest is the hex SHA-256 of s.
func hexDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
