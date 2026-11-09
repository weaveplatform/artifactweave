package cran

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestUnitIndexLatestVersionAndRoundTrip(t *testing.T) {
	entries := []Package{
		{
			Name:    "demo",
			Version: "1.9",
			Path:    "src/contrib/demo_1.9.tar.gz",
			Feed:    "src/contrib",
			Fields:  map[string]string{"Imports": "stats"},
		},
		{
			Name:    "demo",
			Version: "1.10",
			Path:    "src/contrib/demo_1.10.tar.gz",
			Feed:    "src/contrib",
			Fields:  map[string]string{"Imports": "utils"},
		},
		{
			Name:    "other",
			Version: "2.0",
			Path:    "src/contrib/other_2.0.tar.gz",
			Feed:    "src/contrib",
			Fields:  map[string]string{},
		},
	}
	encoded, err := EncodeIndex(entries, "src/contrib")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded[PackagesName], []byte("Version: 1.9")) ||
		!bytes.Contains(encoded[PackagesName], []byte("Version: 1.10")) {
		t.Fatalf("incorrect newest version:\n%s", encoded[PackagesName])
	}
	for _, name := range []string{PackagesName, PackagesGzipName} {
		decoded, err := DecodeIndex(encoded[name], "src/contrib/"+name)
		if err != nil || len(decoded) != 2 || decoded[0].Name != "demo" ||
			decoded[0].Version != "1.10" {
			t.Fatalf("%s decoded = %+v, err = %v", name, decoded, err)
		}
	}
}

func TestUnitIndexEmptyFeedAndMetadataSizeLimits(t *testing.T) {
	for _, name := range []string{PackagesName, PackagesGzipName} {
		encoded, err := EncodeIndex(nil, "src/contrib")
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeIndex(encoded[name], "src/contrib/"+name)
		if err != nil || len(decoded) != 0 {
			t.Fatalf("empty %s index: packages=%v err=%v", name, decoded, err)
		}
	}
	if _, err := DecodeIndex(bytes.Repeat([]byte("x"), maxIndexBytes+1),
		"src/contrib/PACKAGES"); !errors.Is(err, ErrPackage) {
		t.Fatalf("oversized index admitted: %v", err)
	}
	pkg := Package{
		Name: "demo", Version: "1.0", Feed: "src/contrib",
		Path:   "src/contrib/demo_1.0.tar.gz",
		Fields: map[string]string{"Imports": strings.Repeat("x", 4097)},
	}
	if _, err := EncodeIndex([]Package{pkg}, "src/contrib"); !errors.Is(err, ErrPackage) {
		t.Fatalf("oversized field admitted: %v", err)
	}
	pkg.Fields["Imports"] = "stats\nunsafe"
	if _, err := EncodeIndex([]Package{pkg}, "src/contrib"); !errors.Is(err, ErrPackage) {
		t.Fatalf("line break in metadata admitted: %v", err)
	}
}

func TestUnitIndexRejectsExcessPackageRecords(t *testing.T) {
	body := []byte(strings.Repeat("Package: demo\nVersion: 1.0\n\n", maxIndexEntries+1))
	if _, err := DecodeIndex(body, "src/contrib/PACKAGES"); !errors.Is(err, ErrPackage) {
		t.Fatalf("oversized package list accepted: %v", err)
	}
}

func TestUnitIndexRejectsUntrustedPathsAndDuplicates(t *testing.T) {
	for _, raw := range []string{
		"Package: demo\nVersion: 1.0\nFile: ../demo_1.0.tar.gz\n\n",
		"Package: demo\nVersion: 1.0\nPath: ../outside\n\n",
		"Package: demo\nVersion: 1.0\nFile: other_1.0.tar.gz\n\n",
		"Package: demo\nVersion: 1.0\n\nPackage: demo\nVersion: 2.0\n\n",
		"Package: demo\nVersion: 1.0\nMD5sum: bad\n\n",
	} {
		if _, err := DecodeIndex([]byte(raw), "src/contrib/PACKAGES"); err == nil {
			t.Errorf("unsafe index admitted: %q", raw)
		}
	}
	valid := "Package: demo\nVersion: 1.0\nPath: Archive/demo\nFile: demo_1.0.tar.gz\n\n"
	decoded, err := DecodeIndex([]byte(valid), "src/contrib/PACKAGES")
	if err != nil || len(decoded) != 1 || !strings.Contains(decoded[0].Path, "Archive/demo/") {
		t.Fatalf("safe subdirectory = %+v, err = %v", decoded, err)
	}
}
