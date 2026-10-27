package conda

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureName = "artifactweave-conda-fixture-1.0.0-0.tar.bz2"

func TestUnitParsePaths(t *testing.T) {
	for _, valid := range []string{
		"noarch/" + fixtureName,
		"main/noarch/" + fixtureName,
		"linux-64/demo-1.0-py311_0.conda",
	} {
		if _, ok := ParsePackagePath(valid); !ok {
			t.Errorf("valid package path refused: %q", valid)
		}
	}
	for _, invalid := range []string{
		"../noarch/" + fixtureName,
		"noarch/../../" + fixtureName,
		"noarch/a.zip",
		"linux-64/demo-1.0.conda",
		"unknown/demo-1.0-0.conda",
		"noarch/DEMO-1.0-0.conda",
		"main//noarch/demo-1.0-0.conda",
	} {
		if _, ok := ParsePackagePath(invalid); ok {
			t.Errorf("invalid package path accepted: %q", invalid)
		}
	}
	if _, ok := ParseMetadataPath("main/noarch/repodata.json.zst"); !ok {
		t.Fatal("valid subchannel metadata refused")
	}
	if _, ok := ParseMetadataPath("main/noarch/notices.json"); !ok {
		t.Fatal("valid notices path refused")
	}
}

func TestUnitPackageIndexAndRepodata(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", fixtureName))
	if err != nil {
		t.Fatal(err)
	}
	id, ok := ParsePackagePath("noarch/" + fixtureName)
	if !ok {
		t.Fatal("fixture path rejected")
	}
	rec, err := ReadPackage(t.Context(), bytes.NewReader(data), int64(len(data)), id)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Name != id.Name || rec.Version != id.Version || rec.Build != id.Build ||
		rec.Subdir != "noarch" || rec.Noarch != "generic" {
		t.Fatalf("package identity %+v", rec)
	}
	digest := sha256.Sum256(data)
	rec.SHA256 = hex.EncodeToString(digest[:])
	rec.Size = int64(len(data))
	index := EmptyRepodata("noarch")
	index.Packages[id.Filename] = rec
	encoded, err := EncodeRepodata(index, "", "noarch")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"repodata.json", "repodata.json.zst", "current_repodata.json"} {
		mp, ok := ParseMetadataPath("noarch/" + name)
		if !ok {
			t.Fatalf("metadata path %s refused", name)
		}
		got, err := DecodeRepodata(encoded[name], mp)
		if err != nil || got.Packages[id.Filename].SHA256 != rec.SHA256 {
			t.Fatalf("decode %s: %+v, %v", name, got, err)
		}
	}
	// Repodata must never disclose an arbitrary filename or a digest that
	// does not describe the package selected by the channel path.
	bad := bytes.ReplaceAll(encoded["repodata.json"], []byte(fixtureName), []byte("../bad.tar.bz2"))
	mp, _ := ParseMetadataPath("noarch/repodata.json")
	if _, err := DecodeRepodata(bad, mp); err == nil {
		t.Fatal("traversal package in repodata was accepted")
	}
	bad = bytes.ReplaceAll(encoded["repodata.json"], []byte(rec.SHA256), []byte("bad"))
	if _, err := DecodeRepodata(bad, mp); err == nil {
		t.Fatal("bad package digest was accepted")
	}
}

func TestUnitPackageIdentityCannotBeForged(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", fixtureName))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := ParsePackagePath("noarch/other-1.0.0-0.tar.bz2")
	if _, err := ReadPackage(t.Context(), bytes.NewReader(data), int64(len(data)), id); err == nil {
		t.Fatal("package indexed under another name")
	}
}

func TestUnitRealModernCondaPackage(t *testing.T) {
	const name = "ca-certificates-2026.7.22-hbd8a1cb_0.conda"
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	id, ok := ParsePackagePath("noarch/" + name)
	if !ok {
		t.Fatal("real .conda path rejected")
	}
	rec, err := ReadPackage(t.Context(), bytes.NewReader(data), int64(len(data)), id)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Name != "ca-certificates" || rec.Version != "2026.7.22" ||
		rec.Build != "hbd8a1cb_0" || rec.Subdir != "noarch" {
		t.Fatalf("real .conda identity: %+v", rec)
	}
}

func TestUnitRepodataRejectsMalformedUpstreamMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
	}{
		{"unsupported path", "noarch/repodata.txt", `{}`},
		{"invalid zstd", "noarch/repodata.json.zst", "not zstd"},
		{"invalid bzip2", "noarch/repodata.json.bz2", "not bzip2"},
		{"wrong subdir", "noarch/repodata.json", `{"info":{"subdir":"linux-64"}}`},
		{"unknown version", "noarch/repodata.json", `{"info":{"subdir":"noarch"},"repodata_version":2}`},
		{"invalid modern package", "noarch/repodata.json", `{"info":{"subdir":"noarch"},"packages.conda":{"demo-1-0.conda":{"name":"wrong"}}}`},
		{"removed traversal", "noarch/repodata.json", `{"info":{"subdir":"noarch"},"removed":["../escape-1-0.tar.bz2"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, ok := ParseMetadataPath(tc.path)
			if !ok {
				// An unsupported wire filename must still be rejected by the decoder.
				path = MetadataPath{Subdir: "noarch", Filename: "repodata.txt"}
			}
			if _, err := DecodeRepodata(
				[]byte(tc.body),
				path,
			); !errors.Is(
				err,
				ErrInvalidRepodata,
			) {
				t.Fatalf("unsafe upstream metadata accepted: %v", err)
			}
		})
	}
	index := EmptyRepodata("noarch")
	index.PackagesConda["demo-1-0.conda"] = Record{Name: "wrong"}
	if _, err := EncodeRepodata(index, "", "noarch"); !errors.Is(err, ErrInvalidRepodata) {
		t.Fatalf("invalid modern package entered generated metadata: %v", err)
	}
	index = EmptyRepodata("noarch")
	index.Removed = nil
	encoded, err := EncodeRepodata(index, "", "noarch")
	if err != nil || !bytes.Contains(encoded["repodata.json"], []byte(`"removed":[]`)) {
		t.Fatalf("empty removed list was not emitted as an array: %v", err)
	}
}

func TestUnitCondaArchivesRejectUnsafeEnvelopeAndEntries(t *testing.T) {
	id, ok := ParsePackagePath("noarch/demo-1-0.conda")
	if !ok {
		t.Fatal("test package path rejected")
	}
	for _, tc := range []struct {
		name, reason string
		entries      []struct{ path, body string }
	}{
		{"duplicate info archives", "duplicate info archive", []struct{ path, body string }{{"info-a.tar.zst", ""}, {"info-b.tar.zst", ""}, {"pkg-a.tar.zst", ""}}},
		{"duplicate payload archives", "duplicate pkg archive", []struct{ path, body string }{{"info-a.tar.zst", ""}, {"pkg-a.tar.zst", ""}, {"pkg-b.tar.zst", ""}}},
		{"oversized metadata", "metadata.json size", []struct{ path, body string }{{"metadata.json", strings.Repeat("x", 4097)}, {"info-a.tar.zst", ""}, {"pkg-a.tar.zst", ""}}},
		{"wrong format version", "metadata.json format version", []struct{ path, body string }{{"metadata.json", `{"conda_pkg_format_version":1}`}, {"info-a.tar.zst", ""}, {"pkg-a.tar.zst", ""}}},
		{"corrupt compressed member", "tar entry", []struct{ path, body string }{{"info-a.tar.zst", "not zstd"}, {"pkg-a.tar.zst", "not zstd"}}},
		{"unsafe member path", "zip entry", []struct{ path, body string }{{"../info-a.tar.zst", ""}, {"pkg-a.tar.zst", ""}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			zw := zip.NewWriter(&out)
			for _, entry := range tc.entries {
				w, err := zw.Create(entry.path)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := w.Write([]byte(entry.body)); err != nil {
					t.Fatal(err)
				}
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadPackage(
				t.Context(),
				bytes.NewReader(out.Bytes()),
				int64(out.Len()),
				id,
			); !errors.Is(err, ErrInvalidPackage) ||
				!strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("unsafe Conda archive accepted: %v", err)
			}
		})
	}
	legacy, _ := ParsePackagePath("noarch/demo-1-0.tar.bz2")
	for _, tc := range []struct {
		name    string
		headers []tar.Header
	}{
		{"missing index", []tar.Header{{Name: "share/readme", Mode: 0o644, Size: 0}}},
		{"unsafe symlink", []tar.Header{{Name: "share/link", Typeflag: tar.TypeSymlink, Linkname: "../escape"}}},
		{"duplicate index", []tar.Header{{Name: "info/index.json", Mode: 0o644, Size: 0}, {Name: "info/index.json", Mode: 0o644, Size: 0}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			tw := tar.NewWriter(&out)
			for _, h := range tc.headers {
				if err := tw.WriteHeader(&h); err != nil {
					t.Fatal(err)
				}
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := readTar(
				t.Context(),
				bytes.NewReader(out.Bytes()),
				legacy,
			); !errors.Is(
				err,
				ErrInvalidPackage,
			) {
				t.Fatalf("unsafe legacy archive accepted: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := readTar(ctx, bytes.NewReader(nil), legacy); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled archive was processed: %v", err)
	}
}
