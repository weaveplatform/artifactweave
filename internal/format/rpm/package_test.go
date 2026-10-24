package rpm

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var errRPMFixtureIO = errors.New("RPM fixture I/O failed")

type rpmFaultReader struct {
	*bytes.Reader
	readAt, seekAt     int64
	failRead, failSeek bool
}

func (r *rpmFaultReader) Read(p []byte) (int, error) {
	pos := r.Size() - int64(r.Len())
	if r.failRead {
		if pos >= r.readAt {
			return 0, errRPMFixtureIO
		}
		if remain := r.readAt - pos; int64(len(p)) > remain {
			p = p[:remain]
		}
	}
	return r.Reader.Read(p)
}

func (r *rpmFaultReader) Seek(offset int64, whence int) (int64, error) {
	if r.failSeek && whence == io.SeekStart && offset == r.seekAt {
		return 0, errRPMFixtureIO
	}
	return r.Reader.Seek(offset, whence)
}

const fixtureName = "artifactweave-alpha-1.0-1.fc42.noarch.rpm"

func TestUnitReadPackageFromRpmbuild(t *testing.T) {
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

func TestUnitReadPackageRejectsStorageFailuresAtEachBoundary(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile(filepath.Join("testdata", fixtureName))
	if err != nil {
		t.Fatal(err)
	}
	_, signatureEnd, err := readHeader(bytes.NewReader(body), leadSize, int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	mainOffset := (signatureEnd + 7) &^ 7
	_, payloadOffset, err := readHeader(bytes.NewReader(body), mainOffset, int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name           string
		readAt, seekAt int64
		failRead       bool
		failSeek       bool
		want           string
	}{
		{"lead read", 0, 0, true, false, "read lead"},
		{"header seek", 0, leadSize, false, true, "seek header"},
		{"header intro read", leadSize, 0, true, false, "read header intro"},
		{"header index read", leadSize + headerIntroLen, 0, true, false, "read header"},
		{"payload seek", 0, payloadOffset, false, true, "seek payload"},
		{"payload read", payloadOffset, 0, true, false, "hash payload"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &rpmFaultReader{
				Reader: bytes.NewReader(body), readAt: tc.readAt, seekAt: tc.seekAt,
				failRead: tc.failRead, failSeek: tc.failSeek,
			}
			_, err := ReadPackage(t.Context(), reader, int64(len(body)), "pool/"+fixtureName)
			if !errors.Is(err, errRPMFixtureIO) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("I/O failure returned %v, want %q", err, tc.want)
			}
		})
	}
	if _, err := ReadPackage(
		t.Context(), bytes.NewReader(body), payloadOffset, "pool/"+fixtureName,
	); err == nil || !strings.Contains(err.Error(), "missing payload") {
		t.Fatalf("RPM with no payload returned %v", err)
	}
}
