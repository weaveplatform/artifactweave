package terraformtest_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/terraform"
	"github.com/weaveplatform/artifactweave/internal/format/terraform/terraformtest"
)

// A release's files are what the format's registry check accepts: the
// SHA256SUMS lists each archive's SHA-256, and the key signed it.
func TestRelease(t *testing.T) {
	k, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if id, err := terraform.KeyID(k.Public); err != nil || id != k.KeyID() || len(id) != 16 {
		t.Fatalf("key ID %q %v, want %q", id, err, k.KeyID())
	}
	if !strings.Contains(k.Private, "PGP PRIVATE KEY BLOCK") {
		t.Fatalf("private half %q", k.Private)
	}
	r := terraformtest.Release{
		Type: "null", Version: "3.3.2", Platforms: []string{"linux_amd64", "darwin_arm64"},
		Protocols: []string{"5.0", "6.0"},
	}
	files := r.Files(k)
	if len(files) != 5 ||
		!strings.Contains(string(files[r.Name("manifest.json")]), `["5.0","6.0"]`) {
		t.Fatalf("files %d, manifest %s", len(files), files[r.Name("manifest.json")])
	}
	archive := r.Name("linux_amd64.zip")
	sum := sha256.Sum256(files[archive])
	v, err := terraform.Check(terraform.Release{
		Repo: "r", Archive: archive, ArchiveSHA256: hex.EncodeToString(sum[:]),
		SumsName: r.Name("SHA256SUMS"), Sums: files[r.Name("SHA256SUMS")],
		SignatureName: r.Name("SHA256SUMS.sig"), Signature: files[r.Name("SHA256SUMS.sig")],
	}, []string{k.Public})
	if err != nil || v.KeyID != k.KeyID() {
		t.Fatalf("check: %+v %v", v, err)
	}
	if bytes.Equal(files[archive], files[r.Name("darwin_arm64.zip")]) {
		t.Fatal("each platform's archive must differ")
	}
	bare := terraformtest.Release{Type: "null", Version: "1.0.0"}
	if _, ok := bare.Files(k)[bare.Name("manifest.json")]; ok {
		t.Fatal("a release without protocols has no manifest")
	}
}

// The zips are stable, so their h1: hashes are, and the provider's binary
// is executable.
func TestZips(t *testing.T) {
	if !bytes.Equal(terraformtest.ProviderZip("null", "1.0.0", "linux_amd64"),
		terraformtest.ProviderZip("null", "1.0.0", "linux_amd64")) {
		t.Fatal("not stable")
	}
	z := read(t, terraformtest.ProviderZip("null", "1.0.0", "linux_amd64"))
	if len(z.File) != 1 || z.File[0].Name != "terraform-provider-null_v1.0.0_x5" ||
		z.File[0].Mode().Perm() != 0o755 {
		t.Fatalf("provider zip %+v", z.File)
	}
	m := read(t, terraformtest.ModuleZip(map[string]string{"main.tf": "# m", "b.tf": "# b"}))
	if len(m.File) != 2 || m.File[0].Name != "b.tf" {
		t.Fatalf("module zip %+v", m.File)
	}
	rc, err := m.File[1].Open()
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := io.ReadAll(rc); string(body) != "# m" {
		t.Fatalf("main.tf %q", body)
	}
}

func read(t *testing.T, b []byte) *zip.Reader {
	t.Helper()
	z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	return z
}
