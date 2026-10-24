package alpine_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // APK v2 file checksum.
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/alpine"
	"github.com/weaveplatform/artifactweave/internal/format/formattest"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

var contractSigningKey = sync.OnceValue(func() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return key
})

func apkSegment(t *testing.T, name string, body []byte, cut bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := gzip.NewWriter(&buf)
	tw := tar.NewWriter(z)
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
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func contractAPK(t *testing.T, name, version string, n int) []byte {
	t.Helper()
	data := apkSegment(t, "usr/bin/"+name, []byte(fmt.Sprintf("binary %d", n)), false)
	h := sha256.Sum256(data)
	info := []byte(fmt.Sprintf(
		"pkgname = %s\npkgver = %s\narch = x86_64\nsize = 8\npkgdesc = controlled package\ndatahash = %s\n",
		name,
		version,
		hex.EncodeToString(h[:]),
	))
	control := apkSegment(t, ".PKGINFO", info, true)
	controlHash := sha1.Sum(control) //nolint:gosec // APK v2 signature wire format.
	signature, err := rsa.SignPKCS1v15(rand.Reader, contractSigningKey(), crypto.SHA1,
		controlHash[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := apkSegment(t, ".SIGN.RSA.test.rsa.pub", signature, true)
	return bytes.Join([][]byte{sig, control, data}, nil)
}

func TestUnitFormatContract(t *testing.T) {
	formattest.Run(t, formattest.Format{
		Type: repo.Alpine, RepoWork: []string{alpine.KindIndex},
		Version: func(name, v string, n int) formattest.Version {
			version := v + "-r0"
			path := "v3.20/main/x86_64/" + name + "-" + version + ".apk"
			return formattest.Version{
				Package: name, Version: version,
				Files: []formattest.File{{Path: path, Body: contractAPK(t, name, version, n)}},
			}
		},
	})
}
