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
	// A refusal answers it with CheckDownload's message in the protocol's
	// envelope, never the protocol's own "not found" (RTDEV-47144).
	route string
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
			route: "swift/swift-local/acme/demo/1.0.0.zip",
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
	w.storeItem(key, p, body, false)
}

// storeCached puts body at p in the remote repository key's cache, as a
// fetch from its upstream would have.
func (w *itemWorld) storeCached(key, p, body string) {
	w.t.Helper()
	w.storeItem(key, p, body, true)
}

func (w *itemWorld) storeItem(key, p, body string, cached bool) {
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
		RepoKey: key, Cached: cached, Path: strings.TrimSuffix(dir, "/"), Name: name,
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
// Nix and Puppet applied the policy themselves before the kit did. Vagrant's
// box files are served through Kit.ServeItem too, and the Vagrant package's
// own tests check the policy on that route (#1259).
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
			served()
			w.patchRepo(c.repo.Key, func(rc *repo.Config) { rc.BlackedOut = ptr(true) })
			w.wantPolicyRefused(route, c.repo.Key, blackoutMessage(c.repo.Key, c.file), c.denied)
			w.patchRepo(c.repo.Key, func(rc *repo.Config) {
				rc.BlackedOut, rc.ExcludesPattern = ptr(false), ptr(c.file)
			})
			w.wantPolicyRefused(route, c.repo.Key, patternsMessage(c.repo.Key, c.file), c.denied)
			w.patchRepo(c.repo.Key, func(rc *repo.Config) { rc.ExcludesPattern = ptr("") })
			served()
		})
	}
}

// blackoutMessage and patternsMessage are the item routes' refusals of path
// in the repository key, which every protocol answers in its own envelope.
func blackoutMessage(key, path string) string {
	return fmt.Sprintf("The repository '%s' is blacked out and cannot serve artifact '%s:%s'.",
		key, key, path)
}

func patternsMessage(key, path string) string {
	return fmt.Sprintf("The repository '%s' rejected the artifact '%s:%s' due to its "+
		"include/exclude pattern settings.", key, key, path)
}

// wantPolicyRefused checks that GET route answers 404 with message for an
// administrator and for a reader, and denied (403 when 0) for a caller who
// may not read, whose body names nothing of how the repository key is set:
// read permission comes first (DV-057).
func (w *itemWorld) wantPolicyRefused(route, key, message string, denied int) {
	w.t.Helper()
	for _, as := range []string{"admin", "reader"} {
		rec := w.do(http.MethodGet, route, as, "")
		wantStatus(w.t, rec, http.StatusNotFound)
		if !strings.Contains(rec.Body.String(), message) {
			w.t.Fatalf("GET %s as %s: body %s, want %q", route, as, rec.Body, message)
		}
	}
	rec := w.do(http.MethodGet, route, "user", "")
	wantStatus(w.t, rec, cmp.Or(denied, http.StatusForbidden))
	if strings.Contains(rec.Body.String(), key+"'") {
		w.t.Fatalf("GET %s as user: body %s names the repository's setting", route, rec.Body)
	}
}

// wantPolicyAnswered checks that GET route, as an administrator, is not
// refused by the download policy: whatever the protocol answers, its body
// carries neither the blackout's nor the patterns' message.
func (w *itemWorld) wantPolicyAnswered(route string) {
	w.t.Helper()
	body := w.do(http.MethodGet, route, "admin", "").Body.String()
	if strings.Contains(body, " is blacked out and cannot serve artifact ") ||
		strings.Contains(body, "include/exclude pattern settings") {
		w.t.Fatalf("GET %s: body %s, want no policy refusal", route, body)
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

// protocolDocument is one native protocol's metadata read from a local
// repository, for the download policy's metadata table (#1251).
type protocolDocument struct {
	// name is the subtest's.
	name string
	// repo is the local repository the read is answered from. It is empty
	// unless seed deploys files to it first: the policy refuses a document
	// before looking for it, so a blacked-out repository refuses one that
	// is not stored too.
	repo repo.Config
	seed []seeded
	// route is the read, after /artifactory/.
	route string
	// document is the path the refusal names. That is where Artifactory
	// 7.x stores or caches the document, which the patterns are checked
	// at too (stored). Otherwise it is the request's own path, for
	// resolution metadata whose 7.x storage is not documented, which only
	// the blackout refuses.
	document string
	stored   bool
	// denied is the answer to a caller who may not read the repository,
	// when the protocol hides it as a missing one rather than answering 403.
	denied int
	// virtual is whether the protocol serves the document from a virtual
	// repository, whose own patterns rejecting document refuse it.
	virtual bool
}

func protocolDocuments() []protocolDocument {
	local := func(key string, typ repo.PackageType) repo.Config {
		return repo.Config{Key: key, PackageType: typ}
	}
	goModule := []seeded{
		{"example.com/demo/@v/v1.0.0.mod", "module example.com/demo\n"},
		{"example.com/demo/@v/v1.0.0.zip", "go module zip"},
	}
	// A narinfo's name is a store path's 32-character Nix base32 hash,
	// which the route checks before anything else.
	const narinfo = "fyli4q2yj6vh01bs0jxqbvj5hir5zvqc.narinfo"
	return []protocolDocument{
		{
			name: "npm packument", repo: local("npm-local", repo.NPM),
			route: "api/npm/npm-local/demo", document: ".npm/demo/package.json", stored: true,
		},
		{
			name: "pypi simple page", repo: local("pypi-local", repo.PyPI),
			route: "api/pypi/pypi-local/simple/demo/", document: ".pypi/demo.html", stored: true,
		},
		{
			name: "pypi simple index", repo: local("pypi-local", repo.PyPI),
			route: "api/pypi/pypi-local/simple/", document: ".pypi/simple.html", stored: true,
		},
		{
			name: "pypi json", repo: local("pypi-local", repo.PyPI),
			route: "api/pypi/pypi-local/pypi/demo/json", document: "pypi/demo/json",
		},
		{
			name: "nuget flat container", repo: local("nuget-local", repo.NuGet),
			route:    "api/nuget/v3/nuget-local/flatcontainer/demo/index.json",
			document: "flatcontainer/demo/index.json", denied: http.StatusNotFound,
		},
		{
			name: "nuget registration", repo: local("nuget-local", repo.NuGet),
			route:    "api/nuget/v3/nuget-local/registration/demo/index.json",
			document: "registration/demo/index.json", denied: http.StatusNotFound,
		},
		{
			name: "gems specs", repo: local("gems-local", repo.Gems),
			route: "api/gems/gems-local/specs.4.8.gz", document: "specs.4.8.gz", stored: true,
			virtual: true,
		},
		{
			name: "gems names", repo: local("gems-local", repo.Gems),
			route: "api/gems/gems-local/names", document: "names",
		},
		{
			name: "hex names", repo: local("hex-local", repo.Hex),
			route: "api/hex/hex-local/names", document: "names", stored: true,
		},
		{
			name: "hex package", repo: local("hex-local", repo.Hex),
			route: "api/hex/hex-local/packages/demo", document: "packages/demo", stored: true,
			virtual: true,
		},
		{
			name: "pub versions", repo: local("pub-local", repo.Pub),
			route: "api/pub/pub-local/api/packages/demo", document: ".pub/demo.json", stored: true,
			virtual: true,
		},
		{
			name: "cocoapods cdn", repo: local("pods-local", repo.CocoaPods),
			route: "api/pods/pods-local/all_pods.txt", document: ".pod/cdn/all_pods.txt",
			stored: true, virtual: true,
		},
		{
			name: "cargo config", repo: local("cargo-local", repo.Cargo),
			route: "api/cargo/cargo-local/index/config.json", document: "config.json", stored: true,
		},
		{
			name: "cargo index", repo: local("cargo-local", repo.Cargo),
			route: "api/cargo/cargo-local/index/de/mo/demo", document: "index/de/mo/demo",
			stored: true,
		},
		{
			name: "composer packages", repo: local("composer-local", repo.Composer),
			route: "api/composer/composer-local/packages.json", document: "packages.json",
		},
		{
			name: "composer metadata", repo: local("composer-local", repo.Composer),
			route: "api/composer/composer-local/p2/acme/demo.json", document: "p2/acme/demo.json",
		},
		{
			name: "go list", repo: local("go-local", repo.Go), seed: goModule,
			route: "api/go/go-local/example.com/demo/@v/list", document: "example.com/demo/@v/list",
			// The list holds the versions the caller may read, so one who
			// may read none is told the module has no version.
			denied: http.StatusNotFound,
		},
		{
			name: "go info", repo: local("go-local", repo.Go), seed: goModule,
			route:    "api/go/go-local/example.com/demo/@v/v1.0.0.info",
			document: "example.com/demo/@v/v1.0.0.info", stored: true,
		},
		{
			name: "docker tags list", repo: local("docker-local", repo.Docker),
			route: "api/docker/docker-local/v2/team/app/tags/list", document: "team/app",
		},
		{
			name: "helm index", repo: local("helm-local", repo.Helm),
			route: "api/helm/helm-local/index.yaml", document: "index.yaml", stored: true,
		},
		{
			name: "conda repodata", repo: local("conda-local", repo.Conda),
			route: "api/conda/conda-local/noarch/repodata.json", document: "noarch/repodata.json",
			stored: true, virtual: true,
		},
		{
			name: "conan revisions", repo: local("conan-local", repo.Conan),
			route:    "api/conan/conan-local/v2/conans/demo/1.0/_/_/revisions",
			document: "v2/conans/demo/1.0/_/_/revisions",
		},
		{
			name: "ansible collection", repo: local("ansible-local", repo.Ansible),
			route:    "api/ansible/ansible-local/api/v3/collections/acme/demo/",
			document: "api/v3/collections/acme/demo",
		},
		{
			name: "bower package", repo: local("bower-local", repo.Bower),
			route: "api/bower/bower-local/packages/demo", document: "packages/demo",
		},
		{
			name: "chef cookbook", repo: local("chef-local", repo.Chef),
			route: "api/chef/chef-local/api/v1/cookbooks/demo", document: "api/v1/cookbooks/demo",
		},
		{
			name: "bazel module metadata", repo: local("bazel-local", repo.BazelModules),
			route:    "api/bazelmodules/bazel-local/modules/demo/metadata.json",
			document: "modules/demo/metadata.json", stored: true, virtual: true,
		},
		{
			name: "debian packages", repo: local("debian-local", repo.Debian),
			route:    "debian-local/dists/stable/main/binary-amd64/Packages",
			document: "dists/stable/main/binary-amd64/Packages", stored: true,
		},
		{
			name: "debian release", repo: local("debian-local", repo.Debian),
			route: "debian-local/dists/stable/Release", document: "dists/stable/Release",
			stored: true, virtual: true,
		},
		{
			name: "rpm repomd", repo: local("rpm-local", repo.RPM),
			route: "rpm-local/repodata/repomd.xml", document: "repodata/repomd.xml", stored: true,
			virtual: true,
		},
		{
			name: "alpine index", repo: local("alpine-local", repo.Alpine),
			route:    "alpine-local/v3.20/main/x86_64/APKINDEX.tar.gz",
			document: "v3.20/main/x86_64/APKINDEX.tar.gz", stored: true, virtual: true,
		},
		{
			name: "opkg feed", repo: local("opkg-local", repo.Opkg),
			route: "opkg-local/feed/Packages", document: "feed/Packages", stored: true,
			virtual: true,
		},
		{
			name: "cran packages", repo: local("cran-local", repo.Cran),
			route: "cran-local/src/contrib/PACKAGES", document: "src/contrib/PACKAGES",
			stored: true, virtual: true,
		},
		{
			name: "machine learning manifest", repo: local("ml-local", repo.MachineLearning),
			route:    "api/machinelearning/ml-local/model/entity-manifest/demo/1.0",
			document: "models/demo/1.0/model-manifest.json", stored: true,
		},
		{
			name: "agent packages versions", repo: local("apm-local", repo.AgentPackages),
			route:    "api/agentpackages/apm-local/v1/packages/acme/demo/versions",
			document: "acme/demo",
		},
		{
			name: "agent plugins marketplace", repo: local("plugins-local", repo.AgentPlugins),
			route:    "api/agentplugins/plugins-local/marketplace.json",
			document: "marketplace.json", stored: true,
		},
		{
			name: "hugging face info", repo: local("hf-local", repo.HuggingFaceML),
			route:    "api/huggingfaceml/hf-local/api/models/acme/demo",
			document: "models--acme--demo", denied: http.StatusNotFound,
		},
		{
			name: "skills skill", repo: local("skills-local", repo.Skills),
			route: "api/skills/skills-local/api/v1/skills/demo", document: "demo",
		},
		{
			name: "luarocks manifest", repo: local("luarocks-local", repo.LuaRocks),
			route: "api/luarocks/luarocks-local/manifest-5.4", document: "manifest-5.4",
			stored: true, virtual: true,
		},
		{
			name: "swift releases", repo: local("swift-local", repo.Swift),
			route: "api/swift/swift-local/acme/Widget", document: "acme/Widget",
		},
		{
			name: "terraform module versions",
			repo: repo.Config{
				Key: "terraform-local", PackageType: repo.Terraform,
				TerraformType: repo.TerraformModule,
			},
			route:    "api/terraform/terraform-local/v1/modules/acme/demo/aws/versions",
			document: "v1/modules/acme/demo/aws/versions",
		},
		{
			name: "terraform provider versions",
			repo: repo.Config{
				Key: "terraform-local", PackageType: repo.Terraform,
				TerraformType: repo.TerraformProvider,
			},
			route:    "api/terraform/terraform-local/v1/providers/acme/demo/versions",
			document: "v1/providers/acme/demo/versions",
		},
		{
			name: "nix cache info", repo: local("nix-local", repo.Nix),
			route: "api/nix/nix-local/nix-cache-info", document: "nix-cache-info", stored: true,
		},
		{
			name: "nix narinfo", repo: local("nix-local", repo.Nix),
			route:    "api/nix/nix-local/" + narinfo,
			document: narinfo, stored: true, virtual: true,
		},
		{
			name: "puppet module", repo: local("puppet-local", repo.Puppet),
			route:    "api/puppet/puppet-local/v3/modules/acme-widget",
			document: "v3/modules/acme-widget",
		},
	}
}

// Every native protocol's metadata answers the download policy as
// Artifactory 7.x reads a stored document, through the download path (#1251,
// decision 0032). A blacked-out repository refuses the document, whether or
// not it is stored, administrators included, with the blackout's message
// naming the path 7.x stores it at. The patterns refuse it at that path,
// never at the paths of the files it lists. Resolution metadata whose 7.x
// storage is not documented is refused by the blackout alone, naming the
// request's path. Once the repository is set back the protocol answers as
// before. Each protocol's own suite covers the listings that keep a release
// whose file the patterns reject, and the remote and virtual reads.
func TestAccProtocolMetadataPolicy(t *testing.T) {
	for _, c := range protocolDocuments() {
		t.Run(c.name, func(t *testing.T) {
			w := newItemWorld(t, false)
			c.repo.Class = repo.Local
			if err := w.db.Repos().
				Create(context.Background(), *repo.Defaults(&c.repo)); err != nil {
				t.Fatal(err)
			}
			for _, f := range c.seed {
				wantStatus(t, w.do(http.MethodPut, "/artifactory/"+c.repo.Key+"/"+f.path, "admin",
					f.body), http.StatusCreated)
			}
			key, route := c.repo.Key, "/artifactory/"+c.route
			w.wantPolicyAnswered(route)
			w.patchRepo(key, func(rc *repo.Config) { rc.BlackedOut = ptr(true) })
			w.wantPolicyRefused(route, key, blackoutMessage(key, c.document), c.denied)
			w.patchRepo(key, func(rc *repo.Config) {
				rc.BlackedOut, rc.ExcludesPattern = ptr(false), ptr(c.document)
			})
			if c.stored {
				w.wantPolicyRefused(route, key, patternsMessage(key, c.document), c.denied)
			} else {
				w.wantPolicyAnswered(route)
			}
			w.patchRepo(key, func(rc *repo.Config) { rc.ExcludesPattern = ptr("") })
			w.wantPolicyAnswered(route)
		})
	}
}

// A virtual repository whose own patterns reject a stored document's path
// has no member to read it from, and answers "No repository found to serve
// the request" (6.20 VirtualRepoDownloadStrategy), naming its own key and
// the path; a virtual repository has no blackout of its own.
func TestAccProtocolMetadataPolicyVirtual(t *testing.T) {
	for _, c := range protocolDocuments() {
		if !c.virtual {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			w := newItemWorld(t, false)
			member, virtual := c.repo.Key, string(c.repo.PackageType)+"-virtual"
			for _, rc := range []repo.Config{
				{Key: member, Class: repo.Local},
				{Key: virtual, Class: repo.Virtual, Repositories: []string{member}},
			} {
				rc.PackageType = c.repo.PackageType
				if err := w.db.Repos().
					Create(context.Background(), *repo.Defaults(&rc)); err != nil {
					t.Fatal(err)
				}
			}
			route := "/artifactory/" + strings.Replace(c.route, member, virtual, 1)
			message := fmt.Sprintf("No repository found to serve the request for %s:%s",
				virtual, c.document)
			answered := func() {
				t.Helper()
				if body := w.do(http.MethodGet, route, "admin", "").Body.String(); strings.
					Contains(body, message) {
					t.Fatalf("GET %s: body %s, want no refusal", route, body)
				}
			}
			answered()
			w.patchRepo(virtual, func(rc *repo.Config) { rc.ExcludesPattern = ptr(c.document) })
			for _, as := range []string{"admin", "reader"} {
				rec := w.do(http.MethodGet, route, as, "")
				wantStatus(t, rec, http.StatusNotFound)
				if !strings.Contains(rec.Body.String(), message) {
					t.Fatalf("GET %s as %s: body %s, want %q", route, as, rec.Body, message)
				}
			}
			w.patchRepo(virtual, func(rc *repo.Config) { rc.ExcludesPattern = ptr("") })
			answered()
		})
	}
}
