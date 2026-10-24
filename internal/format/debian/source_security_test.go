package debian

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // Debian Sources wire checksum.
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/terraform/terraformtest"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitSourceDescriptorRejectsCorruptDeclarations(t *testing.T) {
	body, err := os.ReadFile("testdata/alpha_1.0-1.dsc")
	if err != nil {
		t.Fatal(err)
	}
	// Work from LF text when constructing corrupt variants. Git's text=auto
	// checks this genuine .dsc fixture out with CRLF on Windows.
	valid := strings.ReplaceAll(string(body), "\r\n", "\n")
	for _, tc := range []struct {
		name, body string
	}{
		{"unsafe source name", strings.Replace(valid, "Source: alpha", "Source: ../alpha", 1)},
		{"missing format", strings.Replace(valid, "Format: 3.0 (quilt)\n", "", 1)},
		{"missing maintainer", strings.Replace(valid, "Maintainer:", "Former-Maintainer:", 1)},
		{"unsafe version", strings.Replace(valid, "Version: 1.0-1", "Version: 1.0/../2", 1)},
		{"invalid checksum hex", strings.Replace(valid, "Checksums-Sha256:\n ", "Checksums-Sha256:\n z", 1)},
		{"missing source archive", strings.Replace(valid, "Checksums-Sha256:", "Other-Checksums:", 1)},
		{"duplicate checksum row", valid + "Files:\n " + strings.Repeat("a", 32) + " 1 archive.tar.gz\n"},
		{"embedded NUL", valid + "X-Note: \x00\n"},
		{"orphan continuation", " orphan\n" + valid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadSource(
				t.Context(),
				strings.NewReader(tc.body),
				int64(len(tc.body)),
			); err == nil {
				t.Fatal("corrupt .dsc accepted")
			}
		})
	}
	if _, err := ReadSource(
		t.Context(),
		bytes.NewReader(body[:len(body)-1]),
		int64(len(body)),
	); err == nil {
		t.Fatal("truncated descriptor accepted")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ReadSource(
		canceled,
		bytes.NewReader(body),
		int64(len(body)),
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("canceled descriptor read: %v", err)
	}
}

func TestUnitSourcesIndexRejectsCorruptArchiveBinding(t *testing.T) {
	entry := dpkgSourceIndexEntry(t)
	valid, err := Sources([]IndexedSource{entry})
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := DecodeSourcesIndex(
		valid,
	); err != nil || len(rows) != 1 ||
		len(rows[0].Files) != 3 {
		t.Fatalf("generated source index: %+v, %v", rows, err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*IndexedSource)
	}{
		{"descriptor path", func(e *IndexedSource) { e.Path = "../alpha.dsc" }},
		{"archive missing", func(e *IndexedSource) { e.Archives = e.Archives[:1] }},
		{"archive renamed", func(e *IndexedSource) { e.Archives[0].Name = "forged.tar.gz" }},
		{"archive size", func(e *IndexedSource) { e.Archives[0].Size++ }},
		{"archive MD5", func(e *IndexedSource) { e.Archives[0].Checksums.MD5 = strings.Repeat("0", 32) }},
		{"archive SHA256", func(e *IndexedSource) { e.Archives[0].Checksums.SHA256 = strings.Repeat("0", 64) }},
		{"descriptor checksum", func(e *IndexedSource) { e.Checksums.SHA256 = "invalid" }},
		{"extra source stanza", func(e *IndexedSource) { e.Source.Control = append(e.Source.Control, '\n') }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := entry
			copy.Archives = append([]SourceArchive(nil), entry.Archives...)
			copy.Source.Control = bytes.Clone(entry.Source.Control)
			tc.mutate(&copy)
			if _, err := Sources([]IndexedSource{copy}); err == nil {
				t.Fatal("untrusted source archive binding accepted")
			}
		})
	}
	for _, tc := range []struct{ name, body string }{
		{"path traversal", strings.Replace(string(valid), "Directory: pool/main/a/alpha",
			"Directory: ../alpha", 1)},
		{"missing descriptor", strings.ReplaceAll(string(valid), ".dsc", ".txt")},
		{"checksum mismatch", strings.Replace(string(valid), "Checksums-Sha256:\n ",
			"Checksums-Sha256:\n "+strings.Repeat("0", 64)+" 1 extra.tar.gz\n ", 1)},
		{"incomplete stanza", strings.TrimSuffix(string(valid), "\n")},
		{"bad version", strings.Replace(string(valid), "Version: 1.0-1", "Version: ../2", 1)},
		{"bad package", strings.Replace(string(valid), "Package: alpha", "Package: ../alpha", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeSourcesIndex([]byte(tc.body)); err == nil {
				t.Fatal("unsafe Sources index accepted")
			}
		})
	}
}

func TestUnitSourceChecksumsRejectAmbiguousArchiveRows(t *testing.T) {
	digest := strings.Repeat("a", 32)
	valid := "\n " + digest + " 153 alpha_1.0.orig.tar.gz"
	if rows, err := sourceChecksums(valid, 16); err != nil || len(rows) != 1 {
		t.Fatalf("valid source archive row: %+v, %v", rows, err)
	}
	for _, tc := range []struct{ name, value string }{
		{"missing continuation", digest + " 153 alpha_1.0.orig.tar.gz"},
		{"blank continuation", "\n"},
		{"missing size", "\n " + digest + " alpha_1.0.orig.tar.gz"},
		{"invalid hex", "\n " + strings.Repeat("z", 32) + " 153 alpha_1.0.orig.tar.gz"},
		{"zero size", "\n " + digest + " 0 alpha_1.0.orig.tar.gz"},
		{"oversized archive", fmt.Sprintf("\n %s %d alpha_1.0.orig.tar.gz", digest, maxBinarySize+1)},
		{"path traversal", "\n " + digest + " 153 ../alpha_1.0.orig.tar.gz"},
		{"whitespace filename", "\n " + digest + " 153 alpha 1.0.orig.tar.gz"},
		{"duplicate archive", valid + "\n " + digest + " 153 alpha_1.0.orig.tar.gz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := sourceChecksums(tc.value, 16); err == nil {
				t.Fatal("unsafe source checksum list accepted")
			}
		})
	}
}

func TestUnitSourceIndexAndDescriptorRejectCrossFileChecksumConflicts(t *testing.T) {
	entry := dpkgSourceIndexEntry(t)
	index, err := Sources([]IndexedSource{entry})
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := DecodeSourcesIndex(nil); err != nil || len(rows) != 0 {
		t.Fatalf("empty Sources index: %+v, %v", rows, err)
	}
	if _, err := DecodeSourcesIndex([]byte{0x1f, 0x8b, 0x00}); err == nil {
		t.Fatal("corrupt gzip Sources index accepted")
	}
	tooManyRows := []byte(strings.Repeat("x\n\n", maxPackageRows+1))
	if _, err := DecodeSourcesIndex(tooManyRows); err == nil {
		t.Fatal("Sources index with excessive stanzas accepted")
	}
	// The first Files row must refer to the same bytes as its matching
	// Checksums-Sha256 row; apt must never see two sizes for one archive.
	name := entry.Archives[0].Name
	badSize := strings.Replace(string(index), fmt.Sprintf(" %s %d %s",
		entry.Archives[0].Checksums.MD5, entry.Archives[0].Size, name),
		fmt.Sprintf(" %s %d %s", entry.Archives[0].Checksums.MD5,
			entry.Archives[0].Size+1, name), 1)
	if _, err := DecodeSourcesIndex([]byte(badSize)); err == nil {
		t.Fatal("Sources index with mismatched archive sizes accepted")
	}
	badName := strings.Replace(string(index), "Checksums-Sha256:\n ",
		"Checksums-Sha256:\n "+strings.Repeat("a", 64)+" 1 extra.tar.gz\n ", 1)
	if _, err := DecodeSourcesIndex([]byte(badName)); err == nil {
		t.Fatal("Sources index with unmatched archive accepted")
	}
	missingFiles := strings.Replace(string(index), "Files:\n", "Unverified-Files:\n", 1)
	if _, err := DecodeSourcesIndex([]byte(missingFiles)); err == nil {
		t.Fatal("Sources index without MD5 Files declarations accepted")
	}
	descriptor := strings.ReplaceAll(string(entry.Source.Control), "\r\n", "\n")
	badDescriptor := strings.Replace(descriptor, "Checksums-Sha256:\n ",
		"Checksums-Sha256:\n "+strings.Repeat("a", 64)+" 1 extra.tar.gz\n ", 1)
	if _, err := ReadSource(
		t.Context(),
		strings.NewReader(badDescriptor),
		int64(len(badDescriptor)),
	); err == nil {
		t.Fatal("descriptor with inconsistent archive lists accepted")
	}
	malformedSigned := "-----BEGIN PGP SIGNED MESSAGE-----\nHash: SHA256\n\n" + descriptor
	if _, err := ReadSource(t.Context(), strings.NewReader(malformedSigned),
		int64(len(malformedSigned))); err == nil {
		t.Fatal("incomplete signed descriptor accepted")
	}
	badFiles := strings.Replace(descriptor, "Files:\n ", "Files:\n not-a-digest ", 1)
	if _, err := ReadSource(
		t.Context(),
		strings.NewReader(badFiles),
		int64(len(badFiles)),
	); err == nil {
		t.Fatal("descriptor with invalid MD5 archive row accepted")
	}
	badSHA := strings.Replace(descriptor, "Checksums-Sha256:\n ",
		"Checksums-Sha256:\n not-a-digest ", 1)
	if _, err := ReadSource(
		t.Context(),
		strings.NewReader(badSHA),
		int64(len(badSHA)),
	); err == nil {
		t.Fatal("descriptor with invalid SHA256 archive row accepted")
	}
}

func TestUnitReadSourceAcceptsSignedDpkgDescriptor(t *testing.T) {
	body, err := os.ReadFile("testdata/alpha_1.0-1.dsc")
	if err != nil {
		t.Fatal(err)
	}
	key, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	signed, err := SignRelease(body, key.Private, "")
	if err != nil {
		t.Fatal(err)
	}
	src, err := ReadSource(
		t.Context(),
		bytes.NewReader(signed.InRelease),
		int64(len(signed.InRelease)),
	)
	if err != nil || src.Name != "alpha" || len(src.Parts) != 2 {
		t.Fatalf("valid signed source descriptor: %+v, %v", src, err)
	}
}

func TestUnitSourcesSortsAcrossNamesVersionsAndPaths(t *testing.T) {
	base := dpkgSourceIndexEntry(t)
	control := bytes.ReplaceAll(base.Source.Control, []byte("\r\n"), []byte("\n"))
	makeEntry := func(name, version, descriptorPath string) IndexedSource {
		copy := base
		copy.Source.Name = name
		copy.Source.Version = version
		copy.Source.Control = bytes.ReplaceAll(control,
			[]byte("Source: alpha\n"), []byte("Source: "+name+"\n"))
		copy.Source.Control = bytes.ReplaceAll(copy.Source.Control,
			[]byte("Version: 1.0-1\n"), []byte("Version: "+version+"\n"))
		copy.Path = descriptorPath
		return copy
	}
	entries := []IndexedSource{
		makeEntry("beta", "1.0-1", "pool/beta/beta.dsc"),
		makeEntry("alpha", "2.0-1", "pool/alpha/alpha2.dsc"),
		makeEntry("alpha", "1.0-1", "pool/alpha/alpha1b.dsc"),
		makeEntry("alpha", "1.0-1", "pool/alpha/alpha1a.dsc"),
	}
	index, err := Sources(entries)
	if err != nil {
		t.Fatal(err)
	}
	previous := -1
	for _, descriptorPath := range []string{
		"pool/alpha/alpha1a.dsc", "pool/alpha/alpha1b.dsc",
		"pool/alpha/alpha2.dsc", "pool/beta/beta.dsc",
	} {
		at := strings.Index(string(index), " "+filepath.Base(descriptorPath)+"\n")
		if at <= previous {
			t.Fatalf("unstable Sources order for %s: %s", descriptorPath, index)
		}
		previous = at
	}
}

func dpkgSourceIndexEntry(t *testing.T) IndexedSource {
	t.Helper()
	const descriptor = "alpha_1.0-1.dsc"
	body, err := os.ReadFile(filepath.Join("testdata", descriptor))
	if err != nil {
		t.Fatal(err)
	}
	src, err := ReadSource(t.Context(), bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	entry := IndexedSource{
		Source: src, Path: "pool/main/a/alpha/" + descriptor, Size: int64(len(body)),
		Checksums: sourceTestChecksums(body),
	}
	for _, part := range src.Parts {
		body, err := os.ReadFile(filepath.Join("testdata", part.Name))
		if err != nil {
			t.Fatal(err)
		}
		entry.Archives = append(entry.Archives, SourceArchive{
			Name: part.Name, Size: int64(len(body)), Checksums: sourceTestChecksums(body),
		})
	}
	return entry
}

func sourceTestChecksums(body []byte) types.Checksums {
	sha := sha256.Sum256(body)
	md5sum := md5.Sum(body) //nolint:gosec // Debian Sources wire checksum.
	return types.Checksums{SHA256: fmt.Sprintf("%x", sha), MD5: fmt.Sprintf("%x", md5sum)}
}
