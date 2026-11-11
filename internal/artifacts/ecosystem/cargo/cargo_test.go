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

func TestUnitCrateRejectsUnsafeOrCorruptTail(t *testing.T) {
	path := CratePath("hello_world", "1.2.3")
	root := "hello_world-1.2.3/"
	manifest := []byte("[package]\nname = \"hello_world\"\nversion = \"1.2.3\"\n")
	archive := func(extra tar.Header, content []byte) []byte {
		t.Helper()
		var b bytes.Buffer
		z := gzip.NewWriter(&b)
		w := tar.NewWriter(z)
		if err := w.WriteHeader(
			&tar.Header{
				Name:     root + "Cargo.toml",
				Typeflag: tar.TypeReg,
				Mode:     0o644,
				Size:     int64(len(manifest)),
			},
		); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(manifest); err != nil {
			t.Fatal(err)
		}
		if err := w.WriteHeader(&extra); err != nil {
			t.Fatal(err)
		}
		if len(content) > 0 {
			if _, err := w.Write(content); err != nil {
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
	for _, tc := range []struct {
		name   string
		header tar.Header
		body   []byte
	}{
		{"traversal", tar.Header{Name: root + "../escape", Typeflag: tar.TypeReg, Size: 1}, []byte("x")},
		{"absolute", tar.Header{Name: "/escape", Typeflag: tar.TypeReg, Size: 1}, []byte("x")},
		{"duplicate manifest", tar.Header{Name: root + "Cargo.toml", Typeflag: tar.TypeReg, Size: int64(len(manifest))}, manifest},
		{"symlink", tar.Header{Name: root + "link", Typeflag: tar.TypeSymlink, Linkname: "../../escape"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := archive(tc.header, tc.body)
			if _, _, err := Inspect(
				t.Context(),
				bytes.NewReader(body),
				int64(len(body)),
				path,
			); err == nil {
				t.Fatal("accepted unsafe archive tail")
			}
		})
	}
	for _, tc := range []struct {
		name   string
		header tar.Header
		body   []byte
	}{
		{"directory", tar.Header{Name: root + "src/", Typeflag: tar.TypeDir}, nil},
		{"source file", tar.Header{Name: root + "src/lib.rs", Typeflag: tar.TypeReg, Size: 8}, []byte("fn a(){}")},
	} {
		t.Run("valid "+tc.name, func(t *testing.T) {
			body := archive(tc.header, tc.body)
			if _, _, err := Inspect(
				t.Context(),
				bytes.NewReader(body),
				int64(len(body)),
				path,
			); err != nil {
				t.Fatal(err)
			}
		})
	}
	good := crate(t, "hello_world", "1.2.3")
	truncated := good[:len(good)-4]
	if _, _, err := Inspect(
		t.Context(),
		bytes.NewReader(truncated),
		int64(len(truncated)),
		path,
	); err == nil {
		t.Fatal("accepted truncated gzip tail")
	}
	var oversized bytes.Buffer
	z := gzip.NewWriter(&oversized)
	w := tar.NewWriter(z)
	if err := w.WriteHeader(
		&tar.Header{Name: root + "Cargo.toml", Typeflag: tar.TypeReg, Size: int64(len(manifest))},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(manifest); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteHeader(
		&tar.Header{Name: root + "huge.bin", Typeflag: tar.TypeReg, Size: maxExpanded + 1},
	); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Inspect(
		t.Context(),
		bytes.NewReader(oversized.Bytes()),
		int64(oversized.Len()),
		path,
	); err == nil {
		t.Fatal("accepted crate declaring excessive expanded size")
	}
	var trailing bytes.Buffer
	z = gzip.NewWriter(&trailing)
	w = tar.NewWriter(z)
	if err := w.WriteHeader(
		&tar.Header{Name: root + "Cargo.toml", Typeflag: tar.TypeReg, Size: int64(len(manifest))},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(manifest); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := z.Write([]byte("unexpected data")); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Inspect(
		t.Context(),
		bytes.NewReader(trailing.Bytes()),
		int64(trailing.Len()),
		path,
	); err == nil {
		t.Fatal("accepted nonzero tar tail")
	}
	var unterminated bytes.Buffer
	z = gzip.NewWriter(&unterminated)
	w = tar.NewWriter(z)
	if err := w.WriteHeader(
		&tar.Header{Name: root + "Cargo.toml", Typeflag: tar.TypeReg, Size: int64(len(manifest))},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(manifest); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Inspect(
		t.Context(),
		bytes.NewReader(unterminated.Bytes()),
		int64(unterminated.Len()),
		path,
	); err == nil {
		t.Fatal("accepted missing tar end marker")
	}
}
