package vcshttp

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"testing"
)

const gitFixtureCommit = "81305b86f97f1361e68f51397943eff6dd635520"

func TestUnitVCSRealGitArchivesValidateCommitAndRootlessPaths(t *testing.T) {
	for _, ext := range []string{"tar.gz", "zip"} {
		t.Run(ext, func(t *testing.T) {
			data, err := os.ReadFile("testdata/legacy-vcs-archive." + ext)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateCommitArchive(
				t.Context(),
				bytes.NewReader(data),
				int64(len(data)),
				ext,
				gitFixtureCommit,
			); err != nil {
				t.Fatal(err)
			}
			if err := ValidateArchive(
				t.Context(),
				bytes.NewReader(data),
				int64(len(data)),
				ext,
			); err != nil {
				t.Fatal(err)
			}
			for _, commit := range []string{"invalid", "0123456789abcdef0123456789abcdef01234567"} {
				if err := ValidateCommitArchive(
					t.Context(),
					bytes.NewReader(data),
					int64(len(data)),
					ext,
					commit,
				); !errors.Is(
					err,
					ErrInvalidArchive,
				) {
					t.Fatalf("accepted wrong commit %s: %v", commit, err)
				}
			}
			for name, want := range map[string]string{"README.md": "root readme\n", "sub/README.md": "nested readme\n"} {
				selected, err := FileZIP(t.Context(), data, ext, name)
				if err != nil {
					t.Fatal(err)
				}
				zr, err := zip.NewReader(bytes.NewReader(selected), int64(len(selected)))
				if err != nil {
					t.Fatal(err)
				}
				r, err := zr.File[0].Open()
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(r)
				_ = r.Close()
				if err != nil || string(body) != want {
					t.Fatalf("selected %s: %q %v", name, body, err)
				}
			}
			if _, err := FileZIP(
				t.Context(),
				data,
				ext,
				"ONLY.md",
			); !errors.Is(
				err,
				ErrInvalidPath,
			) {
				t.Fatalf("nested-only file selected as root: %v", err)
			}
		})
	}
}

func TestUnitVCSGlobalPAXMetadataCannotChangeEntryIdentity(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		records                map[string]string
		duplicate, late, empty bool
	}{
		{name: "invalid commit", records: map[string]string{"comment": "invalid"}},
		{name: "path override", records: map[string]string{"comment": gitFixtureCommit, "path": "other/file"}},
		{name: "missing commit", records: map[string]string{"mtime": "1"}},
		{name: "duplicate", records: map[string]string{"comment": gitFixtureCommit}, duplicate: true},
		{name: "late", records: map[string]string{"comment": gitFixtureCommit}, late: true},
		{name: "empty", records: map[string]string{"comment": gitFixtureCommit}, empty: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var data bytes.Buffer
			gz := gzip.NewWriter(&data)
			tw := tar.NewWriter(gz)
			file := func() {
				if err := tw.WriteHeader(
					&tar.Header{Name: "README.md", Mode: 0o644, Size: 1},
				); err != nil {
					t.Fatal(err)
				}
				if _, err := tw.Write([]byte("x")); err != nil {
					t.Fatal(err)
				}
			}
			global := func() {
				if err := tw.WriteHeader(
					&tar.Header{
						Name:       "pax_global_header",
						Typeflag:   tar.TypeXGlobalHeader,
						PAXRecords: tc.records,
					},
				); err != nil {
					t.Fatal(err)
				}
			}
			if tc.late {
				file()
			}
			global()
			if tc.duplicate {
				global()
			}
			if !tc.empty {
				file()
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			if err := ValidateCommitArchive(
				t.Context(),
				bytes.NewReader(data.Bytes()),
				int64(data.Len()),
				"tar.gz",
				gitFixtureCommit,
			); !errors.Is(
				err,
				ErrInvalidArchive,
			) {
				t.Fatalf("unsafe PAX admitted: %v", err)
			}
		})
	}
}

func TestUnitVCSPeerSelectedFileZIPMustMatchExactPath(t *testing.T) {
	valid, err := os.ReadFile("testdata/legacy-vcs-index.zip")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateFileZIP(
		t.Context(),
		bytes.NewReader(valid),
		int64(len(valid)),
		"index.js",
	); err != nil {
		t.Fatal(err)
	}
	nested, err := os.ReadFile("testdata/legacy-vcs-nested-readme.zip")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateFileZIP(
		t.Context(),
		bytes.NewReader(nested),
		int64(len(nested)),
		"sub/README.md",
	); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, file string
		data       []byte
		size       int64
	}{
		{"wrong file", "README.md", valid, int64(len(valid))},
		{"wrapped", "index.js", vcsZIP(t, "root/index.js"), -1},
		{"multiple entries", "index.js", vcsZIP(t, "index.js", "sub/index.js"), -1},
		{"bad ZIP", "index.js", []byte("invalid"), 7},
		{"invalid path", "../index.js", valid, int64(len(valid))},
		{"too large", "index.js", valid, MaxFileZIPBytes + 1},
		{"empty", "index.js", nil, 0},
		{"wrong size", "index.js", valid, int64(len(valid)) + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.size == -1 {
				tc.size = int64(len(tc.data))
			}
			if err := ValidateFileZIP(
				t.Context(),
				bytes.NewReader(tc.data),
				tc.size,
				tc.file,
			); !errors.Is(
				err,
				ErrInvalidArchive,
			) {
				t.Fatalf("invalid peer ZIP admitted: %v", err)
			}
		})
	}
}

func TestUnitVCSCustomFileSupportsExactAndWrappedLayouts(t *testing.T) {
	for _, ext := range []string{"tar.gz", "zip"} {
		data, err := os.ReadFile("testdata/legacy-vcs-archive." + ext)
		if err != nil {
			t.Fatal(err)
		}
		got, err := CustomFileZIP(t.Context(), data, ext, "README.md")
		if err != nil {
			t.Fatal(err)
		}
		exact, err := FileZIP(t.Context(), data, ext, "README.md")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, exact) {
			t.Fatal("custom rootless path did not take precedence")
		}
	}
	for _, tc := range []struct {
		name    string
		archive []byte
		want    error
	}{
		{name: "root-only", archive: vcsZIP(t, "README.md")},
		{name: "wrapped", archive: vcsZIP(t, "root/README.md")},
		{name: "ambiguous wrappers", archive: vcsZIP(t, "one/README.md", "two/README.md"), want: ErrInvalidArchive},
		{name: "missing", archive: vcsZIP(t, "OTHER.md"), want: ErrInvalidPath},
		{name: "corrupt", archive: []byte("invalid"), want: ErrInvalidArchive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CustomFileZIP(t.Context(), tc.archive, "zip", "README.md")
			if !errors.Is(err, tc.want) {
				t.Fatalf("custom selection: %v, want %v", err, tc.want)
			}
		})
	}
}
