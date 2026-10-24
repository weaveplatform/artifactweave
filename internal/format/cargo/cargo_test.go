package cargo

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"strings"
	"testing"
)

func crate(t *testing.T, name, version string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	w := tar.NewWriter(z)
	manifest := []byte("[package]\nname = \"" + name + "\"\nversion = \"" + version + "\"\n")
	if err := w.WriteHeader(
		&tar.Header{
			Name: name + "-" + version + "/Cargo.toml",
			Mode: 0o644,
			Size: int64(len(manifest)),
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(manifest); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestUnitSparsePathsAndCrateIdentity(t *testing.T) {
	for _, tc := range []struct{ name, path string }{{"a", "1/a"}, {"ab", "2/ab"}, {"abc", "3/a/abc"}, {"Cargo", "ca/rg/cargo"}} {
		if got := IndexPath(tc.name); got != tc.path {
			t.Errorf("%s: %s", tc.name, got)
		}
	}
	content := crate(t, "hello_world", "1.2.3")
	if n, v, err := Inspect(
		context.Background(),
		bytes.NewReader(content),
		int64(len(content)),
		CratePath("hello_world", "1.2.3"),
	); err != nil || n != "hello_world" ||
		v != "1.2.3" {
		t.Fatalf("inspect: %s %s %v", n, v, err)
	}
	for _, p := range []string{"crates/other/other-1.2.3.crate", "crates/hello_world/hello_world-1.2.4.crate", "hello_world-1.2.3.crate", "crates/hello_world/../../hello_world-1.2.3.crate"} {
		if _, _, err := Inspect(
			context.Background(),
			bytes.NewReader(content),
			int64(len(content)),
			p,
		); err == nil {
			t.Errorf("accepted %s", p)
		}
	}
	if _, _, err := Inspect(
		context.Background(),
		strings.NewReader("bad"),
		3,
		CratePath("hello_world", "1.2.3"),
	); err == nil {
		t.Fatal("accepted invalid gzip")
	}
}

func TestUnitRecordBounds(t *testing.T) {
	r := Record{
		Name:     "safe",
		Version:  "0.1.0",
		Checksum: strings.Repeat("a", 64),
		Deps:     []Dependency{},
		Features: map[string][]string{},
	}
	if _, err := Encode(r); err != nil {
		t.Fatal(err)
	}
	r.Checksum = strings.Repeat("z", 64)
	if _, err := Encode(r); err == nil {
		t.Fatal("accepted invalid digest")
	}
	r.Checksum = strings.Repeat("a", 64)
	r.Deps = []Dependency{{Name: "bad/path", Req: "^1"}}
	if _, err := Encode(r); err == nil {
		t.Fatal("accepted invalid dependency")
	}
}

func TestUnitCrateManifestValidation(t *testing.T) {
	path := CratePath("hello_world", "1.2.3")
	build := func(entry, manifest string, kind byte) []byte {
		t.Helper()
		var b bytes.Buffer
		z := gzip.NewWriter(&b)
		w := tar.NewWriter(z)
		if err := w.WriteHeader(
			&tar.Header{Name: entry, Typeflag: kind, Mode: 0o644, Size: int64(len(manifest))},
		); err != nil {
			t.Fatal(err)
		}
		if len(manifest) != 0 {
			if _, err := w.Write([]byte(manifest)); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	root := "hello_world-1.2.3/"
	for _, tc := range []struct {
		name, entry, manifest string
		kind                  byte
	}{
		{"missing manifest", root + "README.md", "readme", tar.TypeReg},
		{"wrong package", root + "Cargo.toml", "[package]\nname = \"different\"\nversion = \"1.2.3\"\n", tar.TypeReg},
		{"wrong version", root + "Cargo.toml", "[package]\nname = \"hello_world\"\nversion = \"1.2.4\"\n", tar.TypeReg},
		{"metadata outside package", root + "Cargo.toml", "[dependencies]\nname = \"hello_world\"\nversion = \"1.2.3\"\n", tar.TypeReg},
		{"empty manifest", root + "Cargo.toml", "", tar.TypeReg},
		{"non-file manifest", root + "Cargo.toml", "", tar.TypeSymlink},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := build(tc.entry, tc.manifest, tc.kind)
			if _, _, err := Inspect(
				t.Context(),
				bytes.NewReader(body),
				int64(len(body)),
				path,
			); err == nil {
				t.Fatal("accepted invalid manifest")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	good := crate(t, "hello_world", "1.2.3")
	if _, _, err := Inspect(ctx, bytes.NewReader(good), int64(len(good)), path); err == nil {
		t.Fatal("ignored canceled validation")
	}
}
