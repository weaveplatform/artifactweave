package debian

import (
	"bytes"
	"compress/gzip"
	"crypto/md5" //nolint:gosec // Debian Release and Packages wire checksum.
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestPackagesUsesStoredDigestsAndStableOrder(t *testing.T) {
	body := fixture(t, validControl)
	meta, err := ReadBinary(t.Context(), bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(body)
	m := md5.Sum(body) //nolint:gosec // Debian Packages wire checksum.
	path := "pool/main/h/hello/hello_1.0-1_amd64.deb"
	entry := IndexedBinary{
		Binary: meta, Path: path, Size: int64(len(body)),
		Checksums: types.Checksums{SHA256: fmt.Sprintf("%x", h), MD5: fmt.Sprintf("%x", m)},
	}
	index, err := Packages([]IndexedBinary{entry})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"Package: hello\n", "Version: 1.0-1\n", "Architecture: amd64\n",
		"Filename: " + path + "\n", fmt.Sprintf("Size: %d\n", len(body)),
		fmt.Sprintf("MD5sum: %x\n", m), fmt.Sprintf("SHA256: %x\n", h),
	} {
		if !strings.Contains(string(index), expected) {
			t.Fatalf("Packages index lacks %q: %s", expected, index)
		}
	}
	meta.Control = []byte(strings.Replace(
		validControl,
		"Description: test binary\n",
		"Filename: ../../poison\nSHA256: "+strings.Repeat(
			"0",
			64,
		)+"\nDescription: test binary\n",
		1,
	))
	entry.Binary = meta
	index, err = Packages([]IndexedBinary{entry})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(index), "../../poison") ||
		strings.Contains(string(index), strings.Repeat("0", 64)) {
		t.Fatalf("untrusted control fields entered Packages: %s", index)
	}
	entry.Path = "../escape.deb"
	if _, err := Packages([]IndexedBinary{entry}); err == nil {
		t.Fatal("unsafe Filename accepted")
	}
	for _, bad := range []string{
		"pool/a\nPackage: forged.deb", "pool/a\rFilename: forged.deb",
		"pool/a\tforged.deb", "pool/a b.deb", "pool/../escape.deb",
	} {
		entry.Path = bad
		if _, err := Packages([]IndexedBinary{entry}); err == nil {
			t.Errorf("unsafe Debian Filename %q accepted", bad)
		}
	}
	entry.Path = "pool/main/h/hello/hello-dbgsym_1.0-1_amd64.ddeb"
	index, err = Packages([]IndexedBinary{entry})
	if err != nil || !strings.Contains(string(index), "Filename: "+entry.Path+"\n") {
		t.Fatalf("valid Debian debug package missing from Packages: %v %s", err, index)
	}
}

func TestReleaseChecksumsBindDeterministicIndex(t *testing.T) {
	index := []byte("Package: hello\nVersion: 1.0-1\n\n")
	compressed, err := GzipIndex(index)
	if err != nil {
		t.Fatal(err)
	}
	second, err := GzipIndex(index)
	if err != nil || !bytes.Equal(compressed, second) {
		t.Fatal("gzip index is not reproducible")
	}
	z, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(z)
	if err != nil || !bytes.Equal(decoded, index) {
		t.Fatalf("gzip index content: %v", err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, time.October, 24, 12, 0, 0, 0, time.UTC)
	entries := []ReleaseEntry{
		{Path: "main/binary-amd64/Packages.gz", Body: compressed},
		{Path: "main/binary-amd64/Packages", Body: index},
	}
	release, err := Release("stable", []string{"main"}, []string{"amd64"}, entries, at)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(index)
	for _, expected := range []string{
		"Suite: stable\n", "Architectures: amd64\n", "Components: main\n",
		"Acquire-By-Hash: yes\n", fmt.Sprintf("%x", sum),
		"main/binary-amd64/Packages\n",
	} {
		if !strings.Contains(string(release), expected) {
			t.Fatalf("Release lacks %q: %s", expected, release)
		}
	}
	entries[0].Path = "../Packages.gz"
	if _, err := Release("stable", []string{"main"}, []string{"amd64"}, entries, at); err == nil {
		t.Fatal("Release accepted unsafe index path")
	}
}
