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
