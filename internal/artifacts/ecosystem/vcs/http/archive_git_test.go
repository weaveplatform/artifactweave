package vcshttp

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"strings"
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
				body, err := SelectFile(t.Context(), data, ext, name)
				if err != nil || string(body) != want {
					t.Fatalf("selected %s: %q %v", name, body, err)
				}
			}
			if _, err := SelectFile(
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

// TestUnitVCSPeerSelectedFileIsBounded: a peer's selected file, which 7.x
// answers as the file itself (RTFACT-29495), is admitted whole within
// MaxSelectedFileBytes, an empty one included, and refused when its length
// is not the one declared or the bound is passed.
func TestUnitVCSPeerSelectedFileIsBounded(t *testing.T) {
	for _, body := range []string{"module.exports = 1;\n", ""} {
		if err := ValidateFile(
			t.Context(), strings.NewReader(body), int64(len(body)),
		); err != nil {
			t.Fatalf("%q: %v", body, err)
		}
	}
	for _, tc := range []struct {
		name string
		size int64
	}{
		{"too large", MaxSelectedFileBytes + 1},
		{"negative", -1},
		{"wrong size", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateFile(
				t.Context(), strings.NewReader("index"), tc.size,
			); !errors.Is(err, ErrInvalidArchive) {
				t.Fatalf("invalid peer file admitted: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := ValidateFile(ctx, strings.NewReader("x"), 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled validation = %v", err)
	}
}

func TestUnitVCSCustomFileSupportsExactAndWrappedLayouts(t *testing.T) {
	for _, ext := range []string{"tar.gz", "zip"} {
		data, err := os.ReadFile("testdata/legacy-vcs-archive." + ext)
		if err != nil {
			t.Fatal(err)
		}
		got, err := SelectCustomFile(t.Context(), data, ext, "README.md")
		if err != nil {
			t.Fatal(err)
		}
		exact, err := SelectFile(t.Context(), data, ext, "README.md")
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
			_, err := SelectCustomFile(t.Context(), tc.archive, "zip", "README.md")
			if !errors.Is(err, tc.want) {
				t.Fatalf("custom selection: %v, want %v", err, tc.want)
			}
		})
	}
}
