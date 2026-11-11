package pub

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"testing"
)

func archiveFixture(t *testing.T, entries []tar.Header, bodies [][]byte) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tarWriter := tar.NewWriter(gz)
	for i := range entries {
		if err := tarWriter.WriteHeader(&entries[i]); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(bodies[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestUnitReadArchiveValidatesWholeTarball(t *testing.T) {
	manifest := []byte(
		"name: widget_pub\nversion: 1.2.3+build.4\ndependencies:\n  helper: ^2.0.0\n",
	)
	good := archiveFixture(t, []tar.Header{
		{Name: "pubspec.yaml", Mode: 0o644, Size: int64(len(manifest)), Typeflag: tar.TypeReg},
		{Name: "lib/widget_pub.dart", Mode: 0o644, Size: 4, Typeflag: tar.TypeReg},
	}, [][]byte{manifest, []byte("dart")})
	meta, err := ReadArchive(bytes.NewReader(good), int64(len(good)))
	if err != nil || meta.Name != "widget_pub" || meta.Version != "1.2.3+build.4" {
		t.Fatalf("valid archive: %+v, %v", meta, err)
	}
	if meta.Pubspec["dependencies"] == nil {
		t.Fatal("dependency pubspec omitted")
	}
	for name, data := range map[string][]byte{
		"truncated gzip": good[:len(good)-5],
		"trailing gzip":  append(bytes.Clone(good), 1),
		"invalid path": archiveFixture(t, []tar.Header{
			{Name: "pubspec.yaml", Mode: 0o644, Size: int64(len(manifest)), Typeflag: tar.TypeReg},
			{Name: "../secret", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg},
		}, [][]byte{manifest, []byte("x")}),
		"duplicate manifest": archiveFixture(t, []tar.Header{
			{Name: "pubspec.yaml", Mode: 0o644, Size: int64(len(manifest)), Typeflag: tar.TypeReg},
			{Name: "pubspec.yaml", Mode: 0o644, Size: int64(len(manifest)), Typeflag: tar.TypeReg},
		}, [][]byte{manifest, manifest}),
		"symlink": archiveFixture(t, []tar.Header{
			{Name: "pubspec.yaml", Mode: 0o644, Size: int64(len(manifest)), Typeflag: tar.TypeReg},
			{Name: "lib/link.dart", Linkname: "../secret", Typeflag: tar.TypeSymlink},
		}, [][]byte{manifest, nil}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadArchive(
				bytes.NewReader(data),
				int64(len(data)),
			); !errors.Is(
				err,
				ErrArchive,
			) {
				t.Fatalf("got %v, want invalid archive", err)
			}
		})
	}
}

func TestUnitReadArchiveRejectsInvalidManifest(t *testing.T) {
	for _, manifest := range []string{
		"name: Invalid-Name\nversion: 1.0.0\n",
		"name: valid_name\nversion: 1.0\n",
		"name: valid_name\nversion: 1.0.0\nname: duplicate\n",
		"name: valid_name\nversion: 1.0.0\ndependencies: &dep {foo: ^1.0.0}\n",
	} {
		data := archiveFixture(t, []tar.Header{{
			Name: "pubspec.yaml", Mode: 0o644, Size: int64(len(manifest)), Typeflag: tar.TypeReg,
		}}, [][]byte{[]byte(manifest)})
		if _, err := ReadArchive(
			bytes.NewReader(data),
			int64(len(data)),
		); !errors.Is(
			err,
			ErrArchive,
		) {
			t.Errorf("accepted %q: %v", manifest, err)
		}
	}
}

func TestUnitPathRoundTrip(t *testing.T) {
	name, version, ok := ParsePath("widget_pub/widget_pub-1.2.3-beta.4+sha.1.tar.gz")
	if !ok || name != "widget_pub" || version != "1.2.3-beta.4+sha.1" {
		t.Fatalf("name=%q version=%q ok=%t", name, version, ok)
	}
	for _, p := range []string{"Widget/Widget-1.0.0.tar.gz", "widget/other-1.0.0.tar.gz", "widget/widget-1.0.tar.gz", "widget/widget-1.0.0.tar.gz/extra"} {
		if _, _, ok := ParsePath(p); ok {
			t.Errorf("accepted %q", p)
		}
	}
}

func tarStream(t *testing.T, entries []tar.Header, bodies [][]byte) []byte {
	t.Helper()
	var out bytes.Buffer
	tarWriter := tar.NewWriter(&out)
	for i := range entries {
		if err := tarWriter.WriteHeader(&entries[i]); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(bodies[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func gzipStream(t *testing.T, plain []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	if _, err := gz.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// TestUnitReadArchiveChecksSizesEntriesAndTrailer: ReadArchive refuses a
// declared size that is not the archive's, a manifest that is missing, empty,
// cut short or not representable as JSON, a mapping key that is not a
// string, and data after the TAR end marker, and accepts folder entries and
// the zero padding after that marker.
func TestUnitReadArchiveChecksSizesEntriesAndTrailer(t *testing.T) {
	manifest := []byte("name: widget_pub\nversion: 1.2.3\n")
	pubspec := tar.Header{
		Name: "pubspec.yaml", Mode: 0o644, Size: int64(len(manifest)), Typeflag: tar.TypeReg,
	}
	folder := tar.Header{Name: "lib/", Mode: 0o755, Typeflag: tar.TypeDir}
	plain := tarStream(t, []tar.Header{folder, pubspec}, [][]byte{nil, manifest})
	// GNU tar pads an archive to a 10 KiB record with zero blocks after its
	// end marker; refusing that padding would refuse archives pub publishes.
	padded := gzipStream(t, append(bytes.Clone(plain), make([]byte, 10240-len(plain)%10240)...))
	meta, err := ReadArchive(bytes.NewReader(padded), int64(len(padded)))
	if err != nil || meta.Name != "widget_pub" || meta.Version != "1.2.3" {
		t.Fatalf("padded archive with a folder: %+v, %v", meta, err)
	}
	good := gzipStream(t, plain)
	for _, size := range []int64{0, int64(len(good)) - 1, MaxArchiveBytes + 1} {
		if _, err := ReadArchive(bytes.NewReader(good), size); !errors.Is(err, ErrArchive) {
			t.Errorf("declared size %d of a %d-byte archive: %v", size, len(good), err)
		}
	}
	manifestOf := func(text string) []byte {
		return archiveFixture(t, []tar.Header{{
			Name: "pubspec.yaml", Mode: 0o644, Size: int64(len(text)), Typeflag: tar.TypeReg,
		}}, [][]byte{[]byte(text)})
	}
	for name, data := range map[string][]byte{
		"no manifest": archiveFixture(t, []tar.Header{
			folder,
			{Name: "lib/widget_pub.dart", Mode: 0o644, Size: 4, Typeflag: tar.TypeReg},
		}, [][]byte{nil, []byte("dart")}),
		"empty manifest": archiveFixture(t, []tar.Header{{
			Name: "pubspec.yaml", Mode: 0o644, Typeflag: tar.TypeReg,
		}}, [][]byte{nil}),
		// Both header blocks survive; the manifest breaks off after five bytes.
		"manifest cut short":               gzipStream(t, plain[:2*512+5]),
		"data after the end marker":        gzipStream(t, append(bytes.Clone(plain), "trailing"...)),
		"mapping key that is not a string": manifestOf("name: widget_pub\nversion: 1.2.3\n1: one\n"),
		"value JSON cannot hold":           manifestOf("name: widget_pub\nversion: 1.2.3\nscore: .inf\n"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadArchive(
				bytes.NewReader(data),
				int64(len(data)),
			); !errors.Is(
				err,
				ErrArchive,
			) {
				t.Fatalf("got %v, want invalid archive", err)
			}
		})
	}
}
