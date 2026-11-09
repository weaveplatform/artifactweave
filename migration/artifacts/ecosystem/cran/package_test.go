package cran

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"testing"
)

func sourceArchive(t *testing.T, name, version, descriptionPath string, extra bool) []byte {
	t.Helper()
	var body bytes.Buffer
	gz := gzip.NewWriter(&body)
	tw := tar.NewWriter(gz)
	data := []byte(
		"Package: " + name + "\nVersion: " + version + "\nDescription: Native test\nOS_type: unix\n",
	)
	if err := tw.WriteHeader(
		&tar.Header{Name: descriptionPath, Mode: 0o644, Size: int64(len(data))},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if extra {
		data = []byte("hello")
		if err := tw.WriteHeader(
			&tar.Header{Name: name + "/R/demo.R", Mode: 0o644, Size: int64(len(data))},
		); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

type cranBrokenUpload struct {
	*bytes.Reader
	seekFails bool
	readFails bool
}

func (r *cranBrokenUpload) Seek(offset int64, whence int) (int64, error) {
	if r.seekFails {
		return 0, errors.New("upload seek failed")
	}
	r.readFails = true
	return r.Reader.Seek(offset, whence)
}

func (r *cranBrokenUpload) Read(p []byte) (int, error) {
	if r.readFails {
		return 0, errors.New("upload read failed")
	}
	return r.Reader.Read(p)
}

func TestUnitNativeUploadRejectsFailedSeekAndRead(t *testing.T) {
	source := sourceArchive(t, "demo", "1.0", "demo/DESCRIPTION", false)
	zipBody, err := os.ReadFile("testdata/assertthat_0.2.1.zip")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, feed, suffix string
		body               []byte
		seekFails          bool
	}{
		{"source rewind", "src/contrib", ".tar.gz", source, true},
		{"zip seek", "bin/windows/contrib/4.5", ".zip", zipBody, true},
		{"zip read", "bin/windows/contrib/4.5", ".zip", zipBody, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			broken := &cranBrokenUpload{Reader: bytes.NewReader(tc.body), seekFails: tc.seekFails}
			if _, err := ReadUploaded(t.Context(), broken, int64(len(tc.body)),
				tc.feed, tc.suffix); err == nil {
				t.Fatal("failed upload transport was accepted")
			}
		})
	}
}

func TestUnitNativeUploadRejectsEmptyAndInvalidArchiveRoots(t *testing.T) {
	var empty bytes.Buffer
	zw := gzip.NewWriter(&empty)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	for _, body := range [][]byte{
		empty.Bytes(),
		sourceArchive(t, "_unsafe", "1.0", "_unsafe/DESCRIPTION", false),
	} {
		if _, err := ReadUploaded(t.Context(), bytes.NewReader(body), int64(len(body)),
			"src/contrib", ".tar.gz"); !errors.Is(err, ErrPackage) {
			t.Fatalf("unsafe archive root accepted: %v", err)
		}
	}
	if _, err := readTar(
		t.Context(),
		bytes.NewReader(empty.Bytes()),
		"upload.tar",
		"demo",
	); !errors.Is(
		err,
		ErrPackage,
	) {
		t.Fatalf("unsupported tar encoding accepted: %v", err)
	}
	if _, ok := Feed("bin/linux/extra/contrib/4.5"); ok {
		t.Fatal("unrecognized binary feed accepted")
	}
}

func TestUnitReadSourceArchiveIdentityAndIntegrity(t *testing.T) {
	good := sourceArchive(t, "demo", "1.2-3", "demo/DESCRIPTION", true)
	pkg, err := ReadPackage(context.Background(), bytes.NewReader(good), int64(len(good)),
		"src/contrib/demo_1.2-3.tar.gz")
	if err != nil || pkg.Name != "demo" || pkg.Version != "1.2-3" ||
		pkg.Fields["OS_type"] != "unix" {
		t.Fatalf("package = %+v, err = %v", pkg, err)
	}
	for _, tc := range []struct {
		name, path string
		body       []byte
	}{
		{"identity", "src/contrib/other_1.2-3.tar.gz", good},
		{"traversal", "src/contrib/demo_1.2-3.tar.gz", sourceArchive(t, "demo", "1.2-3", "demo/../DESCRIPTION", false)},
		{"truncated", "src/contrib/demo_1.2-3.tar.gz", good[:len(good)-8]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadPackage(
				context.Background(),
				bytes.NewReader(tc.body),
				int64(len(tc.body)),
				tc.path,
			)
			if !errors.Is(err, ErrPackage) {
				t.Fatalf("expected invalid package, got %v", err)
			}
		})
	}
}

func TestUnitReadBinaryZipAndRejectUnsafeEntry(t *testing.T) {
	archive := func(extra string) []byte {
		var body bytes.Buffer
		zw := zip.NewWriter(&body)
		for name, content := range map[string]string{
			"demo/DESCRIPTION": "Package: demo\nVersion: 1.0\nBuilt: R 4.5.0; x86_64-w64-mingw32; 2025-01-01\n",
			extra:              "payload",
		} {
			entry, err := zw.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(entry, content); err != nil {
				t.Fatal(err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return body.Bytes()
	}
	good := archive("demo/R/demo.rdb")
	path := "bin/windows/contrib/4.5/demo_1.0.zip"
	pkg, err := ReadPackage(context.Background(), bytes.NewReader(good), int64(len(good)), path)
	if err != nil || pkg.Feed != "bin/windows/contrib/4.5" {
		t.Fatalf("package = %+v, err = %v", pkg, err)
	}
	bad := archive("demo/../escape")
	if _, err := ReadPackage(
		context.Background(),
		bytes.NewReader(bad),
		int64(len(bad)),
		path,
	); !errors.Is(
		err,
		ErrPackage,
	) {
		t.Fatalf("unsafe ZIP = %v", err)
	}
}

func TestUnitCRANPathGrammar(t *testing.T) {
	for _, path := range []string{
		"src/contrib/demo_1.0.tar.gz",
		"src/contrib/Archive/demo/demo_0.9.tar.xz",
		"bin/windows/contrib/4.5/demo_1.0.zip",
		"bin/macosx/big-sur-arm64/contrib/4.5/demo_1.0.tgz",
	} {
		if !IsPackage(path) {
			t.Errorf("valid path rejected: %s", path)
		}
	}
	for _, path := range []string{
		"src/contrib/demo_1.0.zip", "bin/windows/contrib/4.5/demo_1.0.tar.gz",
		"src/contrib/x/../demo_1.0.tar.gz", "src/contrib/_demo_1.0.tar.gz",
		"src/contrib/demo_abc.tar.gz", "bin/windows/contrib/latest/demo_1.0.zip",
	} {
		if IsPackage(path) {
			t.Errorf("invalid path accepted: %s", path)
		}
	}
}

func TestUnitReadUnchangedCRANArchive(t *testing.T) {
	// The bytes were downloaded from CRAN's src/contrib/assertthat_0.2.1.tar.gz.
	// This fixture exercises real DESCRIPTION continuations and metadata tags.
	body, err := os.ReadFile("testdata/assertthat_0.2.1.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := ReadPackage(t.Context(), bytes.NewReader(body), int64(len(body)),
		"src/contrib/assertthat_0.2.1.tar.gz")
	if err != nil || pkg.Name != "assertthat" || pkg.Version != "0.2.1" ||
		pkg.Fields["Imports"] != "tools" {
		t.Fatalf("published CRAN archive = %+v, err = %v", pkg, err)
	}
}

func TestUnitReadUnchangedCRANWindowsBinary(t *testing.T) {
	// CRAN's published R 4.5 Windows ZIP is unchanged; its DESCRIPTION uses CRLF.
	body, err := os.ReadFile("testdata/assertthat_0.2.1.zip")
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := ReadPackage(t.Context(), bytes.NewReader(body), int64(len(body)),
		"bin/windows/contrib/4.5/assertthat_0.2.1.zip")
	if err != nil || pkg.Name != "assertthat" || pkg.Version != "0.2.1" ||
		pkg.Fields["Imports"] != "tools" {
		t.Fatalf("published Windows CRAN archive = %+v, err = %v", pkg, err)
	}
}

func TestUnitInferNativePOSTUploadIdentity(t *testing.T) {
	for _, tc := range []struct {
		file, feed, suffix, expected string
	}{
		{"assertthat_0.2.1.tar.gz", "src/contrib", ".tar.gz", "src/contrib/assertthat_0.2.1.tar.gz"},
		{"assertthat_0.2.1.zip", "bin/windows/contrib/4.5", ".zip", "bin/windows/contrib/4.5/assertthat_0.2.1.zip"},
	} {
		body, err := os.ReadFile("testdata/" + tc.file)
		if err != nil {
			t.Fatal(err)
		}
		pkg, err := ReadUploaded(
			t.Context(),
			bytes.NewReader(body),
			int64(len(body)),
			tc.feed,
			tc.suffix,
		)
		if err != nil || pkg.Path != tc.expected {
			t.Fatalf("%s: inferred %+v, err = %v", tc.file, pkg, err)
		}
	}
}
