package debian

import (
	"bytes"
	"crypto/md5" //nolint:gosec // Debian Packages uses MD5 as a wire checksum.
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format/terraform/terraformtest"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestParsePackagesIndexBindsRealPackage(t *testing.T) {
	body, err := os.ReadFile("testdata/alpha_1.0_amd64_0.deb")
	if err != nil {
		t.Fatal(err)
	}
	binary, err := ReadBinary(t.Context(), bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	index, err := Packages([]IndexedBinary{{
		Binary: binary, Path: "pool/alpha_1.0_amd64_0.deb", Size: int64(len(body)),
		Checksums: types.Checksums{
			SHA256: fmt.Sprintf("%x", sha256.Sum256(body)),
			MD5:    fmt.Sprintf("%x", md5.Sum(body)),
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := ParsePackagesIndex(index)
	if err != nil || len(rows) != 1 || rows[0].Filename != "pool/alpha_1.0_amd64_0.deb" ||
		rows[0].SHA256 != fmt.Sprintf("%x", sha256.Sum256(body)) ||
		!bytes.Equal(rows[0].Raw, index) {
		t.Fatalf("real package index: %+v, %v", rows, err)
	}
	compressed, err := GzipIndex(index)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePackagesIndex(compressed)
	if err != nil || len(decoded) != 1 || decoded[0].SHA256 != rows[0].SHA256 {
		t.Fatalf("gzip Packages index: %+v, %v", decoded, err)
	}
	for _, bad := range [][]byte{
		[]byte(strings.Replace(string(index), "pool/alpha_1.0_amd64_0.deb", "../escape.deb", 1)),
		[]byte(strings.Replace(string(index), "SHA256: ", "SHA256: xyz", 1)),
		bytes.TrimSuffix(index, []byte("\n")),
		append(bytes.Clone(index), index...),
	} {
		if _, err := ParsePackagesIndex(bad); err == nil {
			t.Errorf("malformed Packages index was accepted: %q", bad[:min(len(bad), 90)])
		}
	}
}

func TestValidateReleaseMetadataAndByHashPath(t *testing.T) {
	packages := []byte("Package: alpha\nVersion: 1.0\nArchitecture: amd64\nFilename: pool/alpha.deb\nSize: 4\nSHA256: " +
		strings.Repeat("a", 64) + "\n\n")
	release, err := Release("stable", []string{"main"}, []string{"amd64"},
		[]ReleaseEntry{{Path: "main/binary-amd64/Packages", Body: packages}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	key, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	signed, err := SignRelease(release, key.Private, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		body []byte
	}{
		{"dists/stable/Release", release},
		{"dists/stable/InRelease", signed.InRelease},
		{"dists/stable/Release.gpg", signed.ReleaseGPG},
	} {
		if err := ValidateReleaseMetadata(tc.path, tc.body); err != nil {
			t.Errorf("%s: genuine metadata rejected: %v", tc.path, err)
		}
	}
	if err := ValidateReleaseMetadata("dists/stable/Release",
		[]byte("Suite: stable\nSHA256:\n bad 1 ../escape\n")); err == nil {
		t.Fatal("unsafe Release checksum accepted")
	}
	sha := fmt.Sprintf("%x", sha256.Sum256(packages))
	path := "dists/stable/main/binary-amd64/by-hash/SHA256/" + sha
	if got, ok := ByHashMetadataPath(path); !ok || got != sha {
		t.Fatalf("valid by-hash path: %q %v", got, ok)
	}
	if _, ok := ByHashMetadataPath("dists/stable/main/binary-amd64/by-hash/SHA256/nope"); ok {
		t.Fatal("invalid by-hash digest accepted")
	}
}
