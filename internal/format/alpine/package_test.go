package alpine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // APKINDEX v2 checksum is SHA-1.
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
)

var testSigningKey = sync.OnceValue(func() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return key
})

func segment(t *testing.T, name string, body []byte, cut bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}
	if !strings.HasPrefix(name, ".") {
		parts := strings.Split(name, "/")
		for i := 1; i < len(parts); i++ {
			if err := tw.WriteHeader(&tar.Header{
				Name: strings.Join(parts[:i], "/") + "/", Typeflag: tar.TypeDir, Mode: 0o755,
			}); err != nil {
				t.Fatal(err)
			}
		}
		h := sha1.Sum(body) //nolint:gosec // APK v2 file checksum.
		hdr.PAXRecords = map[string]string{"APK-TOOLS.checksum.SHA1": hex.EncodeToString(h[:])}
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if cut {
		if err := tw.Flush(); err != nil {
			t.Fatal(err)
		}
	} else if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func packageWithData(t *testing.T, data []byte, cutControl bool) ([]byte, []byte) {
	t.Helper()
	h := sha256.Sum256(data)
	info := strings.Join([]string{
		"pkgname = hello",
		"pkgver = 1.0-r0",
		"arch = x86_64",
		"size = 5",
		"pkgdesc = Hello Alpine",
		"depend = so:libc.musl-x86_64.so.1",
		"datahash = " + hex.EncodeToString(h[:]),
		"",
	}, "\n")
	control := segment(t, ".PKGINFO", []byte(info), cutControl)
	controlHash := sha1.Sum(control) //nolint:gosec // APK v2 signature wire format.
	sig, err := rsa.SignPKCS1v15(rand.Reader, testSigningKey(), crypto.SHA1, controlHash[:])
	if err != nil {
		t.Fatal(err)
	}
	signature := segment(t, ".SIGN.RSA.test.rsa.pub", sig, true)
	return bytes.Join([][]byte{signature, control, data}, nil), control
}

func fixture(t *testing.T) ([]byte, []byte) {
	t.Helper()
	return packageWithData(t, segment(t, "usr/bin/hello", []byte("hello"), false), true)
}

func TestUnitReadPackageBindsArchiveToPathAndData(t *testing.T) {
	body, control := fixture(t)
	got, err := ReadPackage(
		context.Background(),
		bytes.NewReader(body),
		int64(len(body)),
		"v3.20/main/x86_64/hello-1.0-r0.apk",
	)
	if err != nil {
		t.Fatal(err)
	}
	controlHash := sha1.Sum(control) //nolint:gosec // APKINDEX v2 wire value.
	if got.Name != "hello" || got.Version != "1.0-r0" ||
		got.Branch != "v3.20" || got.Component != "main" ||
		got.Architecture != "x86_64" || got.InstalledSize != 5 ||
		got.ControlSHA1 != base64.StdEncoding.EncodeToString(controlHash[:]) ||
		len(got.Depends) != 1 {
		t.Fatalf("unexpected package metadata: %+v", got)
	}
	for _, tc := range []struct {
		name string
		body []byte
		path string
	}{
		{"wrong filename", body, "v3.20/main/x86_64/other-1.0-r0.apk"},
		{"wrong architecture", body, "v3.20/main/aarch64/hello-1.0-r0.apk"},
		{"traversal", body, "v3.20/../x86_64/hello-1.0-r0.apk"},
		{"damaged data", append(append([]byte(nil), body[:len(body)-1]...), 'x'), "v3.20/main/x86_64/hello-1.0-r0.apk"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadPackage(
				context.Background(),
				bytes.NewReader(tc.body),
				int64(len(tc.body)),
				tc.path,
			); err == nil {
				t.Fatal("invalid APK was accepted")
			}
		})
	}
}

func TestUnitReadPackageRejectsArchivesThatApkCannotInstall(t *testing.T) {
	valid := segment(t, "usr/bin/hello", []byte("hello"), false)
	badFileChecksum := func() []byte {
		var out bytes.Buffer
		z := gzip.NewWriter(&out)
		tw := tar.NewWriter(z)
		for _, name := range []string{"usr/", "usr/bin/"} {
			if err := tw.WriteHeader(&tar.Header{
				Name: name, Typeflag: tar.TypeDir, Mode: 0o755,
			}); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: "usr/bin/hello", Mode: 0o644, Size: 5,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("hello")); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}()
	for _, tc := range []struct {
		name string
		data []byte
		cut  bool
	}{
		{"control has end-of-tar records", valid, false},
		{"data has no parent directories", segmentWithoutDirs(t), true},
		{"data has no per-file checksum", badFileChecksum, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive, _ := packageWithData(t, tc.data, tc.cut)
			if _, err := ReadPackage(t.Context(), bytes.NewReader(archive), int64(len(archive)),
				"v3.20/main/x86_64/hello-1.0-r0.apk"); err == nil {
				t.Fatal("uninstallable APK was admitted")
			}
		})
	}
}

func segmentWithoutDirs(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	z := gzip.NewWriter(&out)
	tw := tar.NewWriter(z)
	h := sha1.Sum([]byte("hello")) //nolint:gosec // APK v2 per-file checksum.
	if err := tw.WriteHeader(&tar.Header{
		Name: "usr/bin/hello", Mode: 0o644, Size: 5,
		PAXRecords: map[string]string{"APK-TOOLS.checksum.SHA1": hex.EncodeToString(h[:])},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
