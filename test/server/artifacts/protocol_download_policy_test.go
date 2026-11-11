package artifacts_test

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"net/http"
	"path"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

// protocolDownload is one native protocol's file download from a local
// repository, for the download policy's table.
type protocolDownload struct {
	// name is the protocol's, and the subtest's.
	name string
	// repo is the local repository the file is stored in.
	repo repo.Config
	// file is the stored file's path, and body its content.
	file, body string
	// route is the native download of the file, after /artifactory/api/.
	route string
	// own is the protocol's 404 message for a file it does not find, where
	// it answers a refused file with it; otherwise the answer carries
	// CheckDownload's message.
	own string
	// archive is a download that packs the file rather than serving its
	// bytes, as Bower's binary does.
	archive bool
	// deploy seeds the files through the item routes' deploy, for a
	// download that finds its version in the package index the deploy
	// fills; also are the version's other files, seeded before file.
	deploy bool
	also   []seeded
	// denied is the answer to a caller who may not read the file, when the
	// protocol hides it as a missing one rather than answering 403.
	denied int
}

func protocolDownloads() []protocolDownload {
	return []protocolDownload{
		{
			name: "hex", repo: repo.Config{Key: "hex-local", PackageType: repo.Hex},
			file: "tarballs/decimal-2.0.0.tar", body: "hex tarball",
			route: "hex/hex-local/tarballs/decimal-2.0.0.tar",
		},
		{
			name: "npm", repo: repo.Config{Key: "npm-local", PackageType: repo.NPM},
			file: "left-pad/-/left-pad-1.3.0.tgz", body: "npm tarball",
			route: "npm/npm-local/left-pad/-/left-pad-1.3.0.tgz",
		},
		{
			name: "gitlfs", repo: repo.Config{Key: "lfs-local", PackageType: repo.GitLFS},
			file:  lfsPath("git lfs object"),
			body:  "git lfs object",
			route: "lfs/lfs-local/objects/" + sha256Hex("git lfs object"),
		},
		{
			name: "helm", repo: repo.Config{Key: "helm-local", PackageType: repo.Helm},
			file: "demo-1.0.0.tgz", body: "helm chart",
			route: "helm/helm-local/demo-1.0.0.tgz",
		},
		{
			name: "gems", repo: repo.Config{Key: "gems-local", PackageType: repo.Gems},
			file: "gems/rake-12.3.1.gem", body: "ruby gem",
			route: "gems/gems-local/gems/rake-12.3.1.gem",
		},
		{
			name: "pypi", repo: repo.Config{Key: "pypi-local", PackageType: repo.PyPI},
			file: "demo-pkg/1.0/demo_pkg-1.0.tar.gz", body: "python sdist",
			route: "pypi/pypi-local/demo-pkg/1.0/demo_pkg-1.0.tar.gz",
		},
		{
			name: "luarocks", repo: repo.Config{Key: "luarocks-local", PackageType: repo.LuaRocks},
			file: "demo-1.0-1.src.rock", body: "lua rock",
			route: "luarocks/luarocks-local/demo-1.0-1.src.rock",
		},
		{
			name: "cargo", repo: repo.Config{Key: "cargo-local", PackageType: repo.Cargo},
			file: "crates/demo/demo-1.0.0.crate", body: "rust crate",
			route: "cargo/cargo-local/api/v1/crates/demo/1.0.0/download",
		},
		{
			name: "agentpackages",
			repo: repo.Config{Key: "apm-local", PackageType: repo.AgentPackages},
			file: "acme/demo/demo-1.0.0.zip", body: "agent package",
			route: "agentpackages/apm-local/v1/packages/acme/demo/versions/1.0.0/download",
		},
		{
			name: "swift", repo: repo.Config{Key: "swift-local", PackageType: repo.Swift},
			file: "acme/demo/demo-1.0.0.zip", body: "swift source archive",
			route: "swift/swift-local/acme/demo/1.0.0.zip", own: "Swift release not found",
		},
		{
			name:   "huggingface",
			repo:   repo.Config{Key: "hf-local", PackageType: repo.HuggingFaceML},
			file:   "models--acme--demo/snapshots/" + hfRevision + "/config.json",
			body:   `{"model":"demo"}`,
			route:  "huggingfaceml/hf-local/acme/demo/resolve/" + hfRevision + "/config.json",
			denied: http.StatusNotFound,
		},
		{
			name: "bower", repo: repo.Config{Key: "bower-local", PackageType: repo.Bower},
			file: "hello/1.0.0/bower.json", body: `{"name":"hello","version":"1.0.0"}`,
			route:   "bower/bower-local/binaries/bower-local/hello.git/v1.0.0",
			archive: true, deploy: true,
		},
		{
			name: "golang", repo: repo.Config{Key: "go-local", PackageType: repo.Go},
			file: "example.com/demo/@v/v1.0.0.zip", body: "go module zip",
			also:   []seeded{{"example.com/demo/@v/v1.0.0.mod", "module example.com/demo\n"}},
			route:  "go/go-local/example.com/demo/@v/v1.0.0.zip",
			deploy: true,
		},
	}
}

// hfRevision is a Hugging Face commit, which a resolve names in full.
const hfRevision = "0123456789abcdef0123456789abcdef01234567"

// seeded is a file a table entry stores before its download.
type seeded struct{ path, body string }

func lfsPath(body string) string {
	oid := sha256Hex(body)
	return "objects/" + oid[:2] + "/" + oid[2:4] + "/" + oid
}

// storeFile puts body at p in the repository key as a file its format
// stored, without the format's deploy rules, so each protocol's table entry
// needs only the file its download serves.
func (w *itemWorld) storeFile(key, p, body string) {
	w.t.Helper()
	ctx := context.Background()
	u, err := w.blobs.BeginUpload(ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := u.Append(ctx, strings.NewReader(body)); err != nil {
		w.t.Fatal(err)
	}
	info, err := u.Commit(ctx, blob.Digests{})
	if err != nil {
		w.t.Fatal(err)
	}
	sums := kernel.Checksums{
		SHA256: info.Digests.SHA256, SHA1: info.Digests.SHA1, MD5: info.Digests.MD5,
	}
	if _, err := w.db.Blobs().
		Ref(ctx, blob.Blob{Checksums: sums, Size: int64(len(body))}, 1); err != nil {
		w.t.Fatal(err)
	}
	dir, name := path.Split(p)
	if _, err := w.db.Items().Put(ctx, artmodel.Item{
		RepoKey: key, Path: strings.TrimSuffix(dir, "/"), Name: name,
		Size: int64(len(body)), Actual: sums,
	}); err != nil {
		w.t.Fatal(err)
	}
}

// Every native protocol that serves a local repository's files through the
// router kit refuses them from a blacked-out repository and where the
// repository's patterns reject them, administrators included, and serves
// them again once the repository is set back (#1237). The table holds the
// protocols whose download needs no more than a stored file, or a deploy
// that indexes it. Those that need a published package (Ansible,
// CocoaPods, Composer, Conan, NuGet, Pub, Skills, Terraform) reach the same
// kit helpers, which the kit's own tests cover; Bazel modules, Chef, Conda,
// Nix, Puppet and Vagrant applied the policy themselves before the kit did.
func TestAccProtocolDownloadPolicy(t *testing.T) {
	for _, c := range protocolDownloads() {
		t.Run(c.name, func(t *testing.T) {
			w := newItemWorld(t, false)
			c.repo.Class = repo.Local
			if err := w.db.Repos().
				Create(context.Background(), *repo.Defaults(&c.repo)); err != nil {
				t.Fatal(err)
			}
			for _, f := range append(c.also, seeded{c.file, c.body}) {
				if !c.deploy {
					w.storeFile(c.repo.Key, f.path, f.body)
					continue
				}
				wantStatus(t, w.do(http.MethodPut, "/artifactory/"+c.repo.Key+"/"+f.path, "admin",
					f.body), http.StatusCreated)
			}
			route := "/artifactory/api/" + c.route
			served := func() {
				t.Helper()
				rec := w.do(http.MethodGet, route, "admin", "")
				wantStatus(t, rec, http.StatusOK)
				if !c.archive && !bytes.Equal(rec.Body.Bytes(), []byte(c.body)) {
					t.Fatalf("GET %s: body %q, want %q", route, rec.Body, c.body)
				}
			}
			refused := func(message string) {
				t.Helper()
				for _, as := range []string{"admin", "reader"} {
					rec := w.do(http.MethodGet, route, as, "")
					wantStatus(t, rec, http.StatusNotFound)
					if c.own != "" {
						message = c.own
					}
					if !strings.Contains(rec.Body.String(), message) {
						t.Fatalf("GET %s as %s: body %s, want %q", route, as, rec.Body, message)
					}
				}
				// Read permission first (DV-057), so this caller learns
				// nothing of how the repository is set.
				rec := w.do(http.MethodGet, route, "user", "")
				wantStatus(t, rec, cmp.Or(c.denied, http.StatusForbidden))
				if strings.Contains(rec.Body.String(), c.repo.Key+"'") {
					t.Fatalf(
						"GET %s as user: body %s names the repository's setting",
						route,
						rec.Body,
					)
				}
			}
			served()
			w.patchRepo(c.repo.Key, func(rc *repo.Config) { rc.BlackedOut = ptr(true) })
			refused(
				fmt.Sprintf("The repository '%s' is blacked out and cannot serve artifact '%s:%s'.",
					c.repo.Key, c.repo.Key, c.file),
			)
			w.patchRepo(c.repo.Key, func(rc *repo.Config) {
				rc.BlackedOut, rc.ExcludesPattern = ptr(false), ptr(c.file)
			})
			refused(fmt.Sprintf("The repository '%s' rejected the artifact '%s:%s' due to its "+
				"include/exclude pattern settings.", c.repo.Key, c.repo.Key, c.file))
			w.patchRepo(c.repo.Key, func(rc *repo.Config) { rc.ExcludesPattern = ptr("") })
			served()
		})
	}
}

// A virtual repository passes over a member that refuses the file, as it
// passes over one without it, and its answer when none serves the file
// names no member's setting (decision 0032); here through Hex's search of
// its members.
func TestAccProtocolDownloadPolicyVirtual(t *testing.T) {
	w := newItemWorld(t, false)
	const file = "tarballs/decimal-2.0.0.tar"
	for _, c := range []repo.Config{
		{Key: "hex-a", Class: repo.Local},
		{Key: "hex-b", Class: repo.Local},
		{Key: "hex-virtual", Class: repo.Virtual, Repositories: []string{"hex-a", "hex-b"}},
	} {
		c.PackageType = repo.Hex
		if err := w.db.Repos().Create(context.Background(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
		if c.Class == repo.Local {
			w.storeFile(c.Key, file, "from "+c.Key)
		}
	}
	route := "/artifactory/api/hex/hex-virtual/" + file
	served := func(from string) {
		t.Helper()
		rec := w.do(http.MethodGet, route, "reader", "")
		wantStatus(t, rec, http.StatusOK)
		if rec.Body.String() != "from "+from {
			t.Fatalf("GET %s: body %q, want the file from %s", route, rec.Body, from)
		}
	}
	none := func() {
		t.Helper()
		rec := w.do(http.MethodGet, route, "admin", "")
		wantStatus(t, rec, http.StatusNotFound)
		if body := rec.Body.String(); strings.Contains(body, "hex-a") ||
			strings.Contains(body, "hex-b") {
			t.Fatalf("GET %s: body %s names a member", route, body)
		}
	}
	served("hex-a")
	w.patchRepo("hex-a", func(rc *repo.Config) { rc.BlackedOut = ptr(true) })
	served("hex-b")
	w.patchRepo("hex-a", func(rc *repo.Config) {
		rc.BlackedOut, rc.ExcludesPattern = ptr(false), ptr("tarballs/**")
	})
	served("hex-b")
	w.patchRepo("hex-b", func(rc *repo.Config) { rc.BlackedOut = ptr(true) })
	none()
	w.patchRepo("hex-a", func(rc *repo.Config) { rc.ExcludesPattern = ptr("") })
	w.patchRepo("hex-b", func(rc *repo.Config) { rc.BlackedOut = ptr(false) })
	w.patchRepo("hex-virtual", func(rc *repo.Config) { rc.ExcludesPattern = ptr("tarballs/**") })
	none()
	w.patchRepo("hex-virtual", func(rc *repo.Config) { rc.ExcludesPattern = ptr("") })
	served("hex-a")
}
