package rpm

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

const fixtureName = "artifactweave-alpha-1.0-1.fc42.noarch.rpm"

func TestReadPackageFromRpmbuild(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile(filepath.Join("testdata", fixtureName))
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := ReadPackage(
		t.Context(),
		bytes.NewReader(body),
		int64(len(body)),
		"pool/"+fixtureName,
	)
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Name != "artifactweave-alpha" || pkg.Version != "1.0" ||
		pkg.Release != "1.fc42" || pkg.Architecture != "noarch" ||
		pkg.Summary != "Controlled RPM acceptance package" {
		t.Fatalf("wrong RPM header identity: %+v", pkg)
	}
	for _, tc := range []struct {
		name string
		edit func([]byte) []byte
		path string
	}{
		{"lead", func(b []byte) []byte { b[0] = 0; return b }, "pool/" + fixtureName},
		{"signature", func(b []byte) []byte { b[leadSize] = 0; return b }, "pool/" + fixtureName},
		{"payload", func(b []byte) []byte { b[len(b)-100] ^= 0xff; return b }, "pool/" + fixtureName},
		{"truncated", func(b []byte) []byte { return b[:len(b)/2] }, "pool/" + fixtureName},
		{"wrong path", func(b []byte) []byte { return b }, "pool/other-1.0-1.fc42.noarch.rpm"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			corrupt := tc.edit(bytes.Clone(body))
			if _, err := ReadPackage(
				t.Context(),
				bytes.NewReader(corrupt),
				int64(len(corrupt)),
				tc.path,
			); err == nil {
				t.Fatal("invalid RPM accepted")
			}
		})
	}
}
