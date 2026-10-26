package vcs

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"testing"
)

func tarGZ(t *testing.T, name string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestUnitValidateArchiveAdmitsProviderTarAndZIP(t *testing.T) {
	archive := tarGZ(t, "repo-abc/README.md")
	if err := ValidateArchive(
		t.Context(),
		bytes.NewReader(archive),
		int64(len(archive)),
		"tar.gz",
	); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	w, err := zw.Create("repo-abc/README.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if err = zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = ValidateArchive(
		t.Context(),
		bytes.NewReader(b.Bytes()),
		int64(b.Len()),
		"zip",
	); err != nil {
		t.Fatal(err)
	}
}

func TestUnitValidateArchiveRejectsTraversalAndCorruption(t *testing.T) {
	for name, body := range map[string][]byte{
		"parent":  tarGZ(t, "../secret"),
		"root":    tarGZ(t, "/secret"),
		"windows": tarGZ(t, `repo\\secret`),
		"crc": func() []byte {
			b := bytes.Clone(tarGZ(t, "repo/README.md"))
			b[len(b)-5] ^= 0xff
			return b
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateArchive(
				context.Background(),
				bytes.NewReader(body),
				int64(len(body)),
				"tar.gz",
			); err == nil {
				t.Fatal("accepted unsafe or corrupt archive")
			}
		})
	}
}

func TestUnitFileZIPSelectsExactRepositoryRelativePath(t *testing.T) {
	files := []struct{ name, body string }{
		{"repo-abc/README.md", "root"},
		{"repo-abc/vendor/pkg/README.md", "nested"},
	}
	var tarBuf bytes.Buffer
	gz := gzip.NewWriter(&tarBuf)
	tw := tar.NewWriter(gz)
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	for _, file := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: file.name, Mode: 0o644, Size: int64(len(file.body)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(file.body)); err != nil {
			t.Fatal(err)
		}
		writer, err := zw.Create(file.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(file.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	for _, archive := range []struct {
		ext  string
		data []byte
	}{
		{"tar.gz", tarBuf.Bytes()},
		{"zip", zipBuf.Bytes()},
	} {
		for _, selected := range []struct{ path, body string }{
			{"README.md", "root"},
			{"vendor/pkg/README.md", "nested"},
		} {
			t.Run(archive.ext+"/"+selected.path, func(t *testing.T) {
				data, err := FileZIP(t.Context(), archive.data, archive.ext, selected.path)
				if err != nil {
					t.Fatal(err)
				}
				out, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
				if err != nil || len(out.File) != 1 || out.File[0].Name != selected.path {
					t.Fatalf("selected ZIP mismatch: %v", err)
				}
				reader, err := out.File[0].Open()
				if err != nil {
					t.Fatal(err)
				}
				content, err := io.ReadAll(reader)
				if err != nil {
					t.Fatal(err)
				}
				if err := reader.Close(); err != nil {
					t.Fatal(err)
				}
				if string(content) != selected.body {
					t.Fatalf("selected content = %q, want %q", content, selected.body)
				}
			})
		}
	}
}

func TestUnitFileZIPAcceptsRootlessFile(t *testing.T) {
	data, err := FileZIP(t.Context(), tarGZ(t, "README.md"), "tar.gz", "README.md")
	if err != nil {
		t.Fatal(err)
	}
	out, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(out.File) != 1 || out.File[0].Name != "README.md" {
		t.Fatalf("rootless selected ZIP mismatch: %v", err)
	}
}
