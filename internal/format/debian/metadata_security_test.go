package debian

import (
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitReleaseRejectsUntrustedChecksumsAndArmor(t *testing.T) {
	const path = "dists/stable/Release"
	digest := strings.Repeat("a", 64)
	valid := "Suite: stable\nArchitectures: amd64\nComponents: main\nSHA256:\n " +
		digest + " 4 main/binary-amd64/Packages\n"
	if err := ValidateReleaseMetadata(path, []byte(valid)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body string
	}{
		{"missing suite", strings.Replace(valid, "Suite: stable\n", "", 1)},
		{"missing checksums", strings.Replace(valid, "SHA256:\n "+digest+" 4 main/binary-amd64/Packages\n", "", 1)},
		{"bad digest", strings.Replace(valid, digest, strings.Repeat("z", 64), 1)},
		{"short digest", strings.Replace(valid, digest, "short", 1)},
		{"negative size", strings.Replace(valid, " 4 main/", " -1 main/", 1)},
		{"bad size", strings.Replace(valid, " 4 main/", " x main/", 1)},
		{"path traversal", strings.Replace(valid, "main/binary-amd64/Packages", "../secret", 1)},
		{"missing path", strings.Replace(valid, " 4 main/binary-amd64/Packages", " 4", 1)},
		{"extra stanza", valid + "\nSuite: other\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateReleaseMetadata(path, []byte(tc.body)); err == nil {
				t.Fatal("unsafe Release accepted")
			}
		})
	}
	for _, tc := range []struct {
		name, path, body string
	}{
		{"unsigned detached signature", "dists/stable/Release.gpg", "not PGP armor"},
		{"malformed clear signature", "dists/stable/InRelease", "-----BEGIN PGP SIGNED MESSAGE-----\nforged"},
		{"invalid path", "dists/../Release", valid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateReleaseMetadata(tc.path, []byte(tc.body)); err == nil {
				t.Fatal("unsafe Release metadata accepted")
			}
		})
	}
	if _, err := ReleaseIndexRefs("../escape", []byte(valid)); err == nil {
		t.Fatal("unsafe distribution accepted for index refs")
	}
}

func TestUnitReleaseSelectsBestAvailablePackageRepresentation(t *testing.T) {
	plain := []byte(
		"Package: alpha\nVersion: 1.0\nArchitecture: amd64\nFilename: pool/alpha.deb\nSize: 4\nSHA256: " +
			strings.Repeat(
				"a",
				64,
			) + "\n\n",
	)
	gzip, err := GzipIndex(plain)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		entries []ReleaseEntry
		want    string
	}{
		{
			"gzip fallback",
			[]ReleaseEntry{{"main/binary-amd64/Packages.gz", gzip}},
			"dists/stable/main/binary-amd64/Packages.gz",
		},
		{"plain preferred", []ReleaseEntry{
			{"main/binary-amd64/Packages.gz", gzip},
			{"main/binary-amd64/Packages", plain},
		}, "dists/stable/main/binary-amd64/Packages"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release, err := Release("stable", []string{"main"}, []string{"amd64"}, tc.entries,
				timeZero())
			if err != nil {
				t.Fatal(err)
			}
			refs, err := ReleasePackageIndexes("stable", release)
			if err != nil || len(refs) != 1 || refs[0].Path != tc.want {
				t.Fatalf("selected indexes = %+v, %v; want %s", refs, err, tc.want)
			}
		})
	}
}

func TestUnitPackagesIndexRejectsAmbiguousCoordinates(t *testing.T) {
	sha := strings.Repeat("a", 64)
	base := "Package: alpha\nVersion: 1.0\nArchitecture: amd64\nFilename: pool/alpha.deb\n" +
		"Size: 4\nSHA256: " + sha + "\n\n"
	if rows, err := ParsePackagesIndex([]byte(base)); err != nil || len(rows) != 1 {
		t.Fatalf("valid package index: %+v, %v", rows, err)
	}
	for _, tc := range []struct {
		name, body string
	}{
		{"missing size", strings.Replace(base, "Size: 4\n", "", 1)},
		{"negative size", strings.Replace(base, "Size: 4", "Size: -4", 1)},
		{"bad name", strings.Replace(base, "Package: alpha", "Package: ../alpha", 1)},
		{"bad version", strings.Replace(base, "Version: 1.0", "Version: 1.0/../2", 1)},
		{"bad architecture", strings.Replace(base, "Architecture: amd64", "Architecture: ../amd64", 1)},
		{"path traversal", strings.Replace(base, "Filename: pool/alpha.deb", "Filename: ../alpha.deb", 1)},
		{"short SHA256", strings.Replace(base, sha, "short", 1)},
		{"nonhex SHA256", strings.Replace(base, sha, strings.Repeat("z", 64), 1)},
		{"duplicate filename", base + base},
		{"incomplete stanza", strings.TrimSuffix(base, "\n")},
		{"embedded NUL", base + "\x00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParsePackagesIndex([]byte(tc.body)); err == nil {
				t.Fatal("unsafe Packages index accepted")
			}
		})
	}
	for _, body := range [][]byte{
		{0x1f, 0x8b, 0x00},
		{0xfd, '7', 'z', 'X', 'Z', 0x00, 0x00},
		{0x28, 0xb5, 0x2f, 0xfd, 0x00},
	} {
		if _, err := DecodePackagesIndex(body); err == nil {
			t.Fatalf("corrupt compressed Packages index accepted: %x", body)
		}
	}
}

func TestUnitPackageIndexRejectsForgedServerDigests(t *testing.T) {
	entry := IndexedBinary{
		Binary: Binary{
			Name: "alpha", Version: "1.0", Architecture: "amd64",
			Control: []byte("Package: alpha\nVersion: 1.0\nArchitecture: amd64\n"),
		},
		Path: "pool/alpha.deb", Size: 4,
		Checksums: types.Checksums{SHA256: strings.Repeat("a", 64), MD5: strings.Repeat("b", 32)},
	}
	for _, tc := range []struct {
		name   string
		mutate func(*IndexedBinary)
	}{
		{"unsafe path", func(e *IndexedBinary) { e.Path = "../alpha.deb" }},
		{"bad size", func(e *IndexedBinary) { e.Size = -1 }},
		{"bad SHA256", func(e *IndexedBinary) { e.Checksums.SHA256 = strings.Repeat("z", 64) }},
		{"bad MD5", func(e *IndexedBinary) { e.Checksums.MD5 = strings.Repeat("z", 32) }},
		{"second control stanza", func(e *IndexedBinary) { e.Binary.Control = append(e.Binary.Control, '\n') }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := entry
			tc.mutate(&copy)
			if _, err := Packages([]IndexedBinary{copy}); err == nil {
				t.Fatal("unsafe server package record accepted")
			}
		})
	}
	if _, err := Release("stable", []string{"main"}, []string{"amd64"},
		[]ReleaseEntry{{Path: "../outside", Body: []byte("unsafe")}}, timeZero()); err == nil {
		t.Fatal("Release included an escaping member")
	}
	if _, err := Release(
		"",
		[]string{"main"},
		[]string{"amd64"},
		[]ReleaseEntry{
			{Path: "main/binary-amd64/Packages", Body: []byte("x")},
		},
		timeZero(),
	); err == nil {
		t.Fatal("Release without a distribution accepted")
	}
	if _, err := Packages([]IndexedBinary{entry}); err != nil {
		t.Fatalf("valid server-computed digests rejected: %v", err)
	}
}

func timeZero() time.Time { return time.Unix(0, 0) }

func TestUnitAptMetadataPathsAndReleaseReferences(t *testing.T) {
	digest := strings.Repeat("a", 64)
	if PackagesIndexPath("dists/stable/main/binary-amd64/Packages.zip") ||
		SourcesIndexPath("dists/stable/main/source/Sources.zip") {
		t.Fatal("unsupported apt index extension accepted")
	}
	if got, ok := ByHashMetadataPath("dists/stable/main/binary-amd64/by-hash/SHA256/" +
		strings.ToUpper(digest)); !ok || got != digest {
		t.Fatalf("valid by-hash metadata path: %q, %v", got, ok)
	}
	if _, ok := ByHashMetadataPath("dists/stable/main/binary-amd64/by-hash/SHA256/" +
		strings.Repeat("z", 64)); ok {
		t.Fatal("nonhex by-hash digest accepted")
	}
	if err := ValidateReleaseMetadata("dists/stable/Release.gpg",
		[]byte("-----BEGIN PGP SIGNATURE-----\nproof\n-----END PGP SIGNATURE-----")); err != nil {
		t.Fatalf("signature armor rejected: %v", err)
	}
	if _, err := ReleasePackageIndexes("stable", []byte("unsafe")); err == nil {
		t.Fatal("unverified Release yielded package references")
	}
	entries := []ReleaseEntry{
		{Path: "main/binary-amd64/Packages", Body: []byte("first")},
		{Path: "main/binary-arm64/Packages", Body: []byte("second")},
		{Path: "main/source/Sources", Body: []byte("source")},
	}
	release, err := Release("stable", []string{"main"}, []string{"amd64", "arm64", "source"},
		entries, timeZero())
	if err != nil {
		t.Fatal(err)
	}
	refs, err := ReleasePackageIndexes("stable", release)
	if err != nil || len(refs) != 2 || refs[0].Path != "dists/stable/main/binary-amd64/Packages" ||
		refs[1].Path != "dists/stable/main/binary-arm64/Packages" {
		t.Fatalf("binary package references: %+v, %v", refs, err)
	}
	if empty, err := ParsePackagesIndex(nil); err != nil || len(empty) != 0 {
		t.Fatalf("empty Packages index: %+v, %v", empty, err)
	}
	if _, err := ParsePackagesIndex([]byte("\n\n")); err == nil {
		t.Fatal("empty Packages stanza accepted")
	}
	if _, err := ParsePackagesIndex([]byte("not a control stanza\n\n")); err == nil {
		t.Fatal("malformed Packages stanza accepted")
	}
	tooManyRows := []byte(strings.Repeat("x\n\n", maxPackageRows+1))
	if _, err := ParsePackagesIndex(tooManyRows); err == nil {
		t.Fatal("Packages index with excessive stanzas accepted")
	}
}

func TestUnitPackagesSortsMultipleNamesVersionsAndFiles(t *testing.T) {
	entry := func(name, version, path string) IndexedBinary {
		return IndexedBinary{
			Binary: Binary{
				Name: name, Version: version, Architecture: "amd64",
				Control: []byte("Package: " + name + "\nVersion: " + version +
					"\nArchitecture: amd64\nDescription: test\n"),
			},
			Path: path,
			Size: 4,
			Checksums: types.Checksums{
				SHA256: strings.Repeat("a", 64),
				MD5:    strings.Repeat("b", 32),
			},
		}
	}
	entries := []IndexedBinary{
		entry("beta", "1.0", "pool/beta.deb"),
		entry("alpha", "2.0", "pool/alpha2.deb"),
		entry("alpha", "1.0", "pool/alpha1b.deb"),
		entry("alpha", "1.0", "pool/alpha1a.deb"),
	}
	index, err := Packages(entries)
	if err != nil {
		t.Fatal(err)
	}
	var last int
	for _, path := range []string{
		"pool/alpha1a.deb", "pool/alpha1b.deb", "pool/alpha2.deb", "pool/beta.deb",
	} {
		at := strings.Index(string(index), "Filename: "+path)
		if at <= last {
			t.Fatalf("unstable apt package order for %s: %s", path, index)
		}
		last = at
	}
}
