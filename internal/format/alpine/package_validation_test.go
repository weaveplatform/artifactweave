package alpine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

const testPackagePath = "v3.20/main/x86_64/hello-1.0-r0.apk"

func dataArchive(t *testing.T, entries ...tar.Header) []byte {
	t.Helper()
	var out bytes.Buffer
	z := gzip.NewWriter(&out)
	tw := tar.NewWriter(z)
	for _, h := range entries {
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := tw.Write(bytes.Repeat([]byte("x"), int(h.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func packageFromInfo(t *testing.T, info []byte, data []byte) []byte {
	t.Helper()
	return append(segment(t, ".PKGINFO", info, true), data...)
}

func TestUnitReadPackageRejectsInvalidControlMetadata(t *testing.T) {
	data := segment(t, "usr/bin/hello", []byte("hello"), false)
	h := sha256.Sum256(data)
	base := "pkgname = hello\npkgver = 1.0-r0\narch = x86_64\nsize = 5\ndatahash = " + hex.EncodeToString(
		h[:],
	) + "\n"
	for _, tc := range []struct{ name, info string }{
		{"missing name", strings.Replace(base, "pkgname = hello\n", "", 1)},
		{"wrong version", strings.Replace(base, "1.0-r0", "2.0-r0", 1)},
		{"wrong architecture", strings.Replace(base, "x86_64", "aarch64", 1)},
		{"bad installed size", strings.Replace(base, "size = 5", "size = -1", 1)},
		{"missing datahash", strings.Replace(base, "datahash = "+hex.EncodeToString(h[:])+"\n", "", 1)},
		{"invalid hex datahash", strings.Replace(base, "datahash = "+hex.EncodeToString(h[:]), "datahash = "+strings.Repeat("z", 64), 1)},
		{"duplicate identity", base + "pkgname = hello\n"},
		{"malformed field", base + "bad line\n"},
		{"NUL field", base + "bad = a\x00b\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := packageFromInfo(t, []byte(tc.info), data)
			if _, err := ReadPackage(
				t.Context(),
				bytes.NewReader(archive),
				int64(len(archive)),
				testPackagePath,
			); err == nil {
				t.Fatal("invalid package metadata accepted")
			}
		})
	}
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"no control", data},
		{"signature without control", segment(t, ".SIGN.RSA.test.rsa.pub", []byte("sig"), true)},
		{"missing PKGINFO", append(segment(t, ".OTHER", []byte("other"), true), data...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadPackage(
				t.Context(),
				bytes.NewReader(tc.body),
				int64(len(tc.body)),
				testPackagePath,
			); err == nil {
				t.Fatal("package without control metadata accepted")
			}
		})
	}
}

func TestUnitValidateDataRejectsUnsafeArchiveEntries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []tar.Header
	}{
		{"parent traversal", []tar.Header{{Name: "../escape", Typeflag: tar.TypeReg, Mode: 0o644}}},
		{"absolute path", []tar.Header{{Name: "/escape", Typeflag: tar.TypeReg, Mode: 0o644}}},
		{"backslash path", []tar.Header{{Name: `bad\\name`, Typeflag: tar.TypeReg, Mode: 0o644}}},
		{"empty symlink target", []tar.Header{{Name: "link", Typeflag: tar.TypeSymlink, Mode: 0o777}}},
		{"unsupported device", []tar.Header{{Name: "device", Typeflag: tar.TypeChar, Mode: 0o644}}},
		{"incorrect digest", []tar.Header{{
			Name: "file", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1,
			PAXRecords: map[string]string{"APK-TOOLS.checksum.SHA1": strings.Repeat("0", 40)},
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := dataArchive(t, tc.entries...)
			if err := validateData(
				t.Context(),
				bytes.NewReader(body),
				int64(len(body)),
			); err == nil {
				t.Fatal("unsafe Alpine data archive accepted")
			}
		})
	}
	valid := dataArchive(t)
	if err := validateData(t.Context(), bytes.NewReader(valid), int64(len(valid)+1)); err == nil {
		t.Fatal("mismatched compressed length accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := validateData(ctx, bytes.NewReader(valid), int64(len(valid))); err == nil {
		t.Fatal("canceled data validation succeeded")
	}
}

func TestUnitReadSegmentRejectsUnsafeControlEntries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []tar.Header
	}{
		{"directory", []tar.Header{{Name: "bad/", Typeflag: tar.TypeDir}}},
		{"unhidden member", []tar.Header{{Name: "PKGINFO", Typeflag: tar.TypeReg}}},
		{"duplicate member", []tar.Header{{Name: ".PKGINFO", Typeflag: tar.TypeReg}, {Name: ".PKGINFO", Typeflag: tar.TypeReg}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := dataArchive(t, tc.entries...)
			if _, err := readSegment(t.Context(), bytes.NewReader(body)); err == nil {
				t.Fatal("unsafe control segment accepted")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readSegment(
		ctx,
		bytes.NewReader(segment(t, ".PKGINFO", []byte("x"), true)),
	); err == nil {
		t.Fatal("canceled control parse succeeded")
	}
}
