package hex

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
)

// This is the original decimal 2.0.0 package from repo.hex.pm, unchanged.
// SHA256: 34666e9c55dea81013e77d9d87370fe6cb6291d1ef32f46a1600230b1d44f577.
func TestAuthenticDecimalTarball(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "decimal-2.0.0.tar"))
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := InspectTarball(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Name != "decimal" || pkg.Version != "2.0.0" || len(pkg.Requirements) != 0 {
		t.Fatalf("unexpected package metadata: %+v", pkg)
	}
	if pkg.OuterChecksum != sha256.Sum256(body) {
		t.Fatal("outer checksum differs from original tarball")
	}
	if p, ok := TarballPath(pkg.Name, pkg.Version); !ok || p != "tarballs/decimal-2.0.0.tar" {
		t.Fatalf("invalid canonical tarball path: %q, %v", p, ok)
	}
}

func TestNestedHexArchiveRejectsTraversalDuplicatesAndBadLinks(t *testing.T) {
	archive := func(headers ...tar.Header) []byte {
		t.Helper()
		var compressed bytes.Buffer
		gz := gzip.NewWriter(&compressed)
		tr := tar.NewWriter(gz)
		for _, hdr := range headers {
			if err := tr.WriteHeader(&hdr); err != nil {
				t.Fatal(err)
			}
			if hdr.Size > 0 {
				if _, err := tr.Write(bytes.Repeat([]byte("a"), int(hdr.Size))); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := tr.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		return compressed.Bytes()
	}
	file := tar.Header{Name: "lib/file.ex", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}
	if err := validateContents(archive(file)); err != nil {
		t.Fatalf("valid nested content: %v", err)
	}
	for _, tc := range []struct {
		name    string
		headers []tar.Header
	}{
		{"duplicate file", []tar.Header{file, file}},
		{"traversal file", []tar.Header{{Name: "../secret", Typeflag: tar.TypeReg}}},
		{"escaping symlink", []tar.Header{{Name: "lib/link", Linkname: "../../secret", Typeflag: tar.TypeSymlink}}},
		{"hard link", []tar.Header{{Name: "lib/link", Linkname: "lib/file.ex", Typeflag: tar.TypeLink}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateContents(archive(tc.headers...)); err == nil {
				t.Fatal("accepted unsafe nested archive")
			}
		})
	}
	if safeLink("lib/link", "../../secret") || safeLink("lib/link", "/etc/passwd") ||
		safeLink("lib/link", `..\secret`) || !safeLink("lib/link", "file.ex") {
		t.Fatal("symlink path boundary is wrong")
	}
	badCRC := bytes.Clone(archive(file))
	badCRC[len(badCRC)-1] ^= 0xff
	for _, body := range [][]byte{nil, []byte("not gzip"), badCRC} {
		if err := validateContents(body); err == nil {
			t.Fatal("accepted corrupt nested gzip")
		}
	}
}

// This is the original ecto 3.12.5 archive from repo.hex.pm. It exercises
// the real keyword-list requirement grammar and optional dependencies.
// SHA256: 6eb18e80bef8bb57e17f5a7f068a1719fbda384d40fc37acb8eb8aeca493b6ea.
func TestAuthenticEctoRequirements(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "ecto-3.12.5.tar"))
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := InspectTarball(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Name != "ecto" || pkg.Version != "3.12.5" || len(pkg.Requirements) != 3 {
		t.Fatalf("unexpected Ecto metadata: %+v", pkg)
	}
	want := map[string]struct {
		requirement string
		optional    bool
	}{
		"telemetry": {"~> 0.4 or ~> 1.0", false},
		"decimal":   {"~> 2.0", false},
		"jason":     {"~> 1.0", true},
	}
	for _, dep := range pkg.Requirements {
		expected, ok := want[dep.Package]
		if !ok || dep.Requirement != expected.requirement || dep.Optional != expected.optional ||
			dep.Repository != "hexpm" {
			t.Errorf("unexpected Ecto requirement: %+v", dep)
		}
	}
}

func TestTarballRejectsCorruptionAndPaths(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "decimal-2.0.0.tar"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"empty", nil},
		{"truncated outer tar", body[:len(body)-7000]},
		{"corrupt nested archive", corrupt(body, 4096)},
		{"corrupt checksum", corrupt(body, 1536)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := InspectTarball(bytes.NewReader(tc.body)); err == nil {
				t.Fatal("corrupt tarball accepted")
			}
		})
	}
	for _, p := range []string{"../x", "x/../../y", "/x", `x\y`, ""} {
		if safePath(p) {
			t.Errorf("unsafe path %q accepted", p)
		}
	}
}

func corrupt(body []byte, at int) []byte {
	copyBody := bytes.Clone(body)
	copyBody[at] ^= 0xff
	return copyBody
}

func TestMetadataRequirements(t *testing.T) {
	data := []byte(`{<<"name">>,<<"example">>}.
{<<"version">>,<<"1.2.3">>}.
{<<"requirements">>,[{<<"decimal">>,[{<<"app">>,<<"decimal">>},
{<<"optional">>,false},{<<"requirement">>,<<"~> 2.0">>}]}]}.
`)
	fields, err := parseMetadata(data)
	if err != nil {
		t.Fatal(err)
	}
	reqs, err := parseRequirements(fields["requirements"])
	if err != nil || len(reqs) != 1 || reqs[0].Package != "decimal" ||
		reqs[0].Requirement != "~> 2.0" {
		t.Fatalf("unexpected requirements: %+v, %v", reqs, err)
	}
}

func TestMetadataRequirementsRejectMalformedEntries(t *testing.T) {
	valid := []any{
		tuple{"name", "decimal"},
		tuple{"app", "decimal"},
		tuple{"optional", false},
		tuple{"requirement", "~> 2.0"},
	}
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"wrong container", "decimal"},
		{"wrong entry", []any{42}},
		{"wrong tuple", []any{tuple{"decimal"}}},
		{"wrong attributes", []any{[]any{"decimal"}}},
		{"duplicate attribute", []any{append(append([]any{}, valid...), tuple{"name", "again"})}},
		{"missing app", []any{[]any{tuple{"name", "decimal"}, tuple{"optional", false}, tuple{"requirement", "~> 2.0"}}}},
		{"unsafe package", []any{[]any{tuple{"name", "../secret"}, tuple{"app", "decimal"}, tuple{"optional", false}, tuple{"requirement", "~> 2.0"}}}},
		{"duplicate dependency", []any{valid, valid}},
		{"mismatched legacy name", []any{tuple{"other", valid}}},
		{"invalid repository", []any{append(append([]any{}, valid...), tuple{"repository", "../secret"})}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseRequirements(tc.value); err == nil {
				t.Fatal("accepted malformed dependency metadata")
			}
		})
	}
}
