package terraform_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/mod/sumdb/dirhash"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/formats"
	"github.com/weaveplatform/artifactweave/internal/format/terraform"
	"github.com/weaveplatform/artifactweave/internal/service/trash"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// terraformH1 is the h1: hash Terraform records for the zip body: its own
// call, dirhash.HashZip over the file.
func terraformH1(t testing.TB, body []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "archive.zip")
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := dirhash.HashZip(p, dirhash.Hash1)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// terraform repositories have the Terraform format; the backend's type has
// a format of its own to come, not this one.
func TestUnitRegistered(t *testing.T) {
	if _, ok := formats.For(repo.Terraform).(terraform.Handler); !ok {
		t.Fatalf("%T", formats.For(repo.Terraform))
	}
	if terraform.Serves(repo.TerraformBackend) {
		t.Fatal("serves terraformbackend")
	}
}

// A Terraform provider package's archives, SHA256SUMS and signature,
// deployed by separate PUTs, are one version of the package, whatever
// order they arrive in, with each archive's platform and h1: hash in its
// metadata, as Terraform computes the hash.
func TestUnitReleaseIsOneVersion(t *testing.T) {
	w := newWorld(t)
	c := w.repo("tf-providers", repo.TerraformProvider)
	r := newRelease(t, "hashicorp/null/3.3.2", "null", "3.3.2", "linux_amd64", "darwin_arm64")
	order := []string{"SHA256SUMS.sig", "linux_amd64.zip", "SHA256SUMS", "darwin_arm64.zip"}
	for i, s := range order {
		w.must(w.deploy(c, r.path(s), r.files[r.name(s)]))
		v, ok := w.version(c, "hashicorp/null", "3.3.2")
		if !ok || len(v.files) != i+1 {
			t.Fatalf("after %s: %v %v", s, v.files, ok)
		}
	}
	v, _ := w.version(c, "hashicorp/null", "3.3.2")
	want := terraform.Meta{Archives: map[string]terraform.Archive{}}
	for _, p := range []string{"linux_amd64", "darwin_arm64"} {
		body := r.files[r.name(p+".zip")]
		goos, arch, _ := strings.Cut(p, "_")
		want.Archives[p] = terraform.Archive{
			Path: r.path(p + ".zip"), OS: goos, Arch: arch,
			SHA256: sha256Hex(body), H1: terraformH1(t, body),
		}
	}
	if got := w.meta(v); !equalMeta(got, want) {
		t.Fatalf("metadata %+v, want %+v", got, want)
	}
	if got := w.packages(c); !slices.Equal(got, []string{"hashicorp/null"}) {
		t.Fatalf("packages %v", got)
	}
}

func equalMeta(a, b terraform.Meta) bool {
	if len(a.Archives) != len(b.Archives) {
		return false
	}
	for k, v := range a.Archives {
		if b.Archives[k] != v {
			return false
		}
	}
	return true
}

// A release deployed file by file reads each archive once: the hash
// Inspect computed is reused, and so is the one the version's metadata
// records while the archive keeps its content. An overwrite with other
// content is hashed anew.
func TestUnitArchivesAreHashedOnce(t *testing.T) {
	w := newWorld(t)
	c := w.repo("tf-providers", repo.TerraformProvider)
	r := newRelease(t, "hashicorp/null/3.3.2", "null", "3.3.2", "linux_amd64", "darwin_arm64")
	for _, s := range []string{"linux_amd64.zip", "darwin_arm64.zip", "SHA256SUMS", "SHA256SUMS.sig"} {
		w.must(w.deploy(c, r.path(s), r.files[r.name(s)]))
	}
	for _, p := range []string{"linux_amd64", "darwin_arm64"} {
		if n := w.blobs.count(sha256Hex(r.files[r.name(p+".zip")])); n != 1 {
			t.Errorf("%s opened %d times", p, n)
		}
	}
	other := zipOf(t, [2]string{"terraform-provider-null_v3.3.2", "rebuilt"})
	w.must(w.deploy(c, r.path("linux_amd64.zip"), other))
	v, _ := w.version(c, "hashicorp/null", "3.3.2")
	if a := w.meta(v).Archives["linux_amd64"]; a.SHA256 != sha256Hex(other) ||
		a.H1 != terraformH1(t, other) {
		t.Fatalf("after the overwrite: %+v", a)
	}
}

// Where paths under different folders, or in another case, claim one
// place in a version, the path that sorts first holds it and the other is
// stored and not indexed; deleting the holder hands its place to the next.
// The namespace and type are indexed in lower case, as Terraform asks for
// them.
func TestUnitOnePathHoldsEachPlace(t *testing.T) {
	w := newWorld(t)
	c := w.repo("tf-providers", repo.TerraformProvider)
	lower := newRelease(t, "mirror/hashicorp/null/3.3.2", "null", "3.3.2", "linux_amd64")
	upper := newRelease(t, "HashiCorp/Null/3.3.2", "null", "3.3.2", "linux_amd64")
	// The second archive's content differs, so its hash tells which holds.
	upper.files[upper.name("linux_amd64.zip")] = zipOf(t, [2]string{"x", "the upper one"})
	for _, r := range []release{lower, upper} {
		w.must(w.deploy(c, r.path("linux_amd64.zip"), r.files[r.name("linux_amd64.zip")]))
		w.must(w.deploy(c, r.path("SHA256SUMS"), r.files[r.name("SHA256SUMS")]))
	}
	v, ok := w.version(c, "hashicorp/null", "3.3.2")
	want := []string{upper.path("SHA256SUMS"), upper.path("linux_amd64.zip")}
	if !ok || !slices.Equal(v.files, want) {
		t.Fatalf("files %v, want %v", v.files, want)
	}
	if a := w.meta(v).Archives["linux_amd64"]; a.Path != upper.path("linux_amd64.zip") {
		t.Fatalf("archive %+v", a)
	}
	w.remove(c, upper.path("linux_amd64.zip"))
	v, _ = w.version(c, "hashicorp/null", "3.3.2")
	want = []string{upper.path("SHA256SUMS"), lower.path("linux_amd64.zip")}
	if !slices.Equal(v.files, want) {
		t.Fatalf("after the delete: files %v, want %v", v.files, want)
	}
	if a := w.meta(v).Archives["linux_amd64"]; a.Path != lower.path("linux_amd64.zip") ||
		a.H1 != terraformH1(t, lower.files[lower.name("linux_amd64.zip")]) {
		t.Fatalf("after the delete: archive %+v", a)
	}
}

// An archive that is not a zip is indexed with no hash, which the network
// mirror then leaves out.
func TestUnitArchiveThatIsNoZip(t *testing.T) {
	w := newWorld(t)
	c := w.repo("tf-providers", repo.TerraformProvider)
	p := "hashicorp/null/3.3.2/terraform-provider-null_3.3.2_linux_amd64.zip"
	w.must(w.deploy(c, p, []byte("not a zip")))
	v, ok := w.version(c, "hashicorp/null", "3.3.2")
	if a := w.meta(v).Archives["linux_amd64"]; !ok || a.Path != p || a.H1 != "" {
		t.Fatalf("archive %+v %v", a, ok)
	}
}

// terraformType gates the index: a module zip in a PROVIDER repository,
// files of a Terraform provider package in a MODULE repository, and any
// file in a repository without terraformType are stored and not indexed
// (decision 0040). A reindex of a repository without it leaves no index.
func TestUnitTypeGates(t *testing.T) {
	w := newWorld(t)
	moduleRepo := w.repo("tf-modules", repo.TerraformModule)
	providerRepo := w.repo("tf-providers", repo.TerraformProvider)
	untyped := w.repo("tf-untyped", "")
	module := zipOf(t, [2]string{"main.tf", "# vpc\n"})
	r := newRelease(t, "hashicorp/null/3.3.2", "null", "3.3.2", "linux_amd64")
	archive := r.files[r.name("linux_amd64.zip")]
	for _, c := range []repo.Config{moduleRepo, providerRepo, untyped} {
		w.must(w.deploy(c, "acme/vpc/aws/1.0.0.zip", module))
		w.must(w.deploy(c, r.path("linux_amd64.zip"), archive))
	}
	for _, tc := range []struct {
		c        repo.Config
		expected []string
	}{
		{moduleRepo, []string{"acme/vpc/aws"}},
		{providerRepo, []string{"hashicorp/null"}},
		{untyped, nil},
	} {
		if got := w.packages(tc.c); !slices.Equal(got, tc.expected) {
			t.Errorf("%s: packages %v, want %v", tc.c.Key, got, tc.expected)
		}
	}
	// A stale entry, as an index kept before the type was known would
	// leave, goes with a reindex.
	_, err := w.st.Packages().Ensure(w.ctx, types.Package{
		RepoKey: untyped.Key, Name: "stale", Type: repo.Terraform,
	})
	w.must(err)
	w.must(w.items.Reindex(w.ctx, untyped))
	if got := w.packages(untyped); len(got) != 0 {
		t.Fatalf("untyped after a reindex: %v", got)
	}
}

// A module version is its zip, under either spelling of its version, the
// one without the "v" first. A file at any other depth or with a name
// Terraform would refuse is stored and not indexed.
func TestUnitModuleVersions(t *testing.T) {
	w := newWorld(t)
	c := w.repo("tf-modules", repo.TerraformModule)
	body := zipOf(t, [2]string{"main.tf", "# vpc\n"})
	for _, p := range []string{
		"acme/vpc/aws/v1.2.0.zip", "acme/vpc/aws/1.2.0.zip", "acme/vpc/aws/2.0.0-rc.1+b5.zip",
		"acme/vpc/AWS/1.0.0.zip", "acme/vpc/aws/1.0.zip", "acme/vpc/aws/x/1.0.0.zip",
		"acme/vpc/aws/1.0.0.tgz", "-acme/vpc/aws/1.0.0.zip",
	} {
		w.must(w.deploy(c, p, body))
	}
	if got := w.packages(c); !slices.Equal(got, []string{"acme/vpc/aws"}) {
		t.Fatalf("packages %v", got)
	}
	for v, want := range map[string]string{
		"1.2.0": "acme/vpc/aws/1.2.0.zip", "2.0.0-rc.1+b5": "acme/vpc/aws/2.0.0-rc.1+b5.zip",
	} {
		got, ok := w.version(c, "acme/vpc/aws", v)
		if !ok || !slices.Equal(got.files, []string{want}) || got.meta != nil {
			t.Errorf("%s: %+v %v", v, got, ok)
		}
	}
	w.remove(c, "acme/vpc/aws/1.2.0.zip")
	if got, _ := w.version(c, "acme/vpc/aws", "1.2.0"); !slices.Equal(
		got.files, []string{"acme/vpc/aws/v1.2.0.zip"}) {
		t.Fatalf("after deleting 1.2.0.zip: %v", got.files)
	}
	// A reindex reads the tree the same way.
	w.must(w.items.Reindex(w.ctx, c))
	if got := w.packages(c); !slices.Equal(got, []string{"acme/vpc/aws"}) {
		t.Fatalf("after a reindex: packages %v", got)
	}
	for v, want := range map[string]string{
		"1.2.0": "acme/vpc/aws/v1.2.0.zip", "2.0.0-rc.1+b5": "acme/vpc/aws/2.0.0-rc.1+b5.zip",
	} {
		if got, ok := w.version(
			c,
			"acme/vpc/aws",
			v,
		); !ok ||
			!slices.Equal(got.files, []string{want}) {
			t.Errorf("after a reindex, %s: %+v %v", v, got, ok)
		}
	}
}

// The reindex operation's queued run (#516) rebuilds the index from the
// tree: a wiped index comes back as the hooks kept it.
func TestUnitQueuedReindexRebuilds(t *testing.T) {
	w := newWorld(t)
	c := w.repo("tf-providers", repo.TerraformProvider)
	r := newRelease(t, "hashicorp/null/3.3.2", "null", "3.3.2", "linux_amd64")
	for name, body := range r.files {
		w.must(w.deploy(c, r.dir+"/"+name, body))
	}
	kept, _ := w.version(c, "hashicorp/null", "3.3.2")
	p, err := w.st.Packages().Find(w.ctx, c.Key, "hashicorp/null")
	w.must(err)
	w.must(w.st.Packages().Delete(w.ctx, p.ID))
	q, err := work.New(w.st, work.Options{})
	w.must(err)
	w.must(w.items.RegisterWork(q))
	w.must(q.Enqueue(w.ctx, format.KindReindex, c.Key, nil))
	w.must(q.EnsureFresh(w.ctx, format.KindReindex, c.Key))
	rebuilt, ok := w.version(c, "hashicorp/null", "3.3.2")
	if !ok || !slices.Equal(rebuilt.files, kept.files) ||
		string(rebuilt.meta) != string(kept.meta) {
		t.Fatalf("rebuilt %+v, kept %+v", rebuilt, kept)
	}
}

// A restore runs no Inspect, so the hook hashes the restored archive
// itself.
func TestUnitRestoreHashesTheArchive(t *testing.T) {
	w := newWorld(t)
	c := w.repo("tf-providers", repo.TerraformProvider)
	r := newRelease(t, "hashicorp/null/3.3.2", "null", "3.3.2", "linux_amd64")
	archive := r.files[r.name("linux_amd64.zip")]
	w.must(w.deploy(c, r.path("linux_amd64.zip"), archive))
	w.remove(c, r.path("linux_amd64.zip"))
	if _, ok := w.version(c, "hashicorp/null", "3.3.2"); ok {
		t.Fatal("indexed after the delete")
	}
	_, err := w.trash.Restore(w.ctx, c.Key, r.path("linux_amd64.zip"),
		trash.Destination{Repo: c}, "test", nil)
	w.must(err)
	v, ok := w.version(c, "hashicorp/null", "3.3.2")
	if a := w.meta(v).Archives["linux_amd64"]; !ok || a.H1 != terraformH1(t, archive) {
		t.Fatalf("restored %+v %v", a, ok)
	}
}

// Inspect reads only an archive written to a PROVIDER repository.
func TestUnitInspectReadsOnlyArchives(t *testing.T) {
	w := newWorld(t)
	h := terraform.Handler{}
	env := format.Env{Blobs: w.blobs}
	r := newRelease(t, "hashicorp/null/3.3.2", "null", "3.3.2", "linux_amd64")
	sha := types.Checksums{SHA256: sha256Hex([]byte("x"))}
	for _, tc := range []struct {
		tt   repo.TerraformType
		path string
		sums types.Checksums
	}{
		{repo.TerraformModule, r.path("linux_amd64.zip"), sha},
		{repo.TerraformProvider, r.path("SHA256SUMS"), sha},
		{repo.TerraformProvider, r.path("linux_amd64.zip"), types.Checksums{}},
		{repo.TerraformProvider, "acme/vpc/aws/1.0.0.zip", sha},
	} {
		got, err := h.Inspect(context.Background(), env, repo.Config{TerraformType: tc.tt},
			format.Write{Op: format.OpDeploy, Path: tc.path, Checksums: tc.sums})
		if got != nil || err != nil {
			t.Errorf("%s %s: %v %v", tc.tt, tc.path, got, err)
		}
	}
	if len(w.blobs.opens) != 0 {
		t.Fatalf("opened %v", w.blobs.opens)
	}
}
