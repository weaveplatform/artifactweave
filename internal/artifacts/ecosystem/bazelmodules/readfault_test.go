package bazelmodules

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

var errRead = errors.New("the store dropped the connection")

// cutSeek is stored content that can only seek, as S3's, GCS's and Azure's
// is, whose first sound reads are served and every later one fails with
// errRead, as a store whose connection drops does.
type cutSeek struct {
	r     *bytes.Reader
	sound int
}

func (c *cutSeek) cut() error {
	if c.sound == 0 {
		return errRead
	}
	c.sound--
	return nil
}

func (c *cutSeek) Read(p []byte) (int, error) {
	if err := c.cut(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

func (c *cutSeek) Seek(off int64, whence int) (int64, error) { return c.r.Seek(off, whence) }

func (*cutSeek) Close() error { return nil }

// cutAt is cutSeek with ReadAt, as a file store's content is; reads in
// order and at offsets share the sound ones.
type cutAt struct{ *cutSeek }

func (c cutAt) ReadAt(p []byte, off int64) (int, error) {
	if err := c.cut(); err != nil {
		return 0, err
	}
	return c.r.ReadAt(p, off)
}

// cutBlobs opens body as cutAt, or as cutSeek when seekOnly, under any
// checksum.
type cutBlobs struct {
	body     []byte
	sound    int
	seekOnly bool
}

func (b cutBlobs) Open(context.Context, string) (io.ReadSeekCloser, blob.Info, error) {
	content := &cutSeek{r: bytes.NewReader(b.body), sound: b.sound}
	info := blob.Info{Size: int64(len(b.body))}
	if b.seekOnly {
		return content, info, nil
	}
	return cutAt{content}, info, nil
}

func (b cutBlobs) Stat(context.Context, string) (blob.Info, error) {
	return blob.Info{Size: int64(len(b.body))}, nil
}

const helloModule = "module(name = \"hello\", version = \"1.0.0\")\n"

// helloZip is a source archive of hello 1.0.0, each entry written with
// method.
func helloZip(t *testing.T, method uint16) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, e := range [][2]string{
		{"hello-1.0.0/BUILD.bazel", "filegroup(name = \"all\", srcs = [\"message.txt\"])\n"},
		{"hello-1.0.0/message.txt", "hello from Bazel\n"},
	} {
		w, err := z.CreateHeader(&zip.FileHeader{Name: e[0], Method: method})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, e[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// helloTarGz is a source archive of hello 1.0.0 holding entries.
func helloTarGz(t *testing.T, entries ...[2]string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e[0], Mode: 0o644, Size: int64(len(e[1]))}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, e[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func inspect(t *testing.T, blobs format.Blobs, path string) (any, error) {
	t.Helper()
	c := repo.Config{Key: "bazel", PackageType: repo.BazelModules, Class: repo.Local}
	return Handler{}.Inspect(t.Context(), format.Env{Blobs: blobs}, c, format.Write{
		Path: path, Checksums: kernel.Checksums{SHA256: "sha"}, Op: format.OpDeploy,
	})
}

// An upload whose stored content fails a read is no invalid publication:
// wherever the read fails, the MODULE.bazel, the archive's sniff, its
// directory, its stream or an entry, on a store with ReadAt or one that
// can only seek, Inspect returns the read's error, which fails the write
// 500, where it returned an inspection Admit refused 409. Once the reads
// reach the end, the upload passes.
func TestUnitInspectReturnsAFailedRead(t *testing.T) {
	for _, tc := range []struct {
		path string
		body []byte
		// fails is the fewest cut points, one for each read that must
		// fail on its own.
		fails int
	}{
		{"modules/hello/1.0.0/MODULE.bazel", []byte(helloModule), 1},
		{"modules/hello/1.0.0/hello-1.0.0.zip", helloZip(t, zip.Store), 4},
		{"modules/hello/1.0.0/hello-1.0.0.zip", helloZip(t, zip.Deflate), 4},
		{"modules/hello/1.0.0/hello-1.0.0.tar.gz", helloTarGz(t,
			[2]string{"hello-1.0.0/BUILD.bazel", "filegroup(name = \"all\")\n"},
			[2]string{"hello-1.0.0/message.txt", "hello from Bazel\n"},
		), 2},
	} {
		for _, seekOnly := range []bool{false, true} {
			at := fmt.Sprintf("%s seekOnly=%v", tc.path, seekOnly)
			failed := 0
			for sound := 0; ; sound++ {
				blobs := cutBlobs{body: tc.body, sound: sound, seekOnly: seekOnly}
				in, err := inspect(t, blobs, tc.path)
				if err == nil {
					if checked, ok := in.(*inspection); !ok || checked.err != nil {
						t.Fatalf("%s, %d sound reads: %#v", at, sound, in)
					}
					break
				}
				if !errors.Is(err, errRead) || in != nil {
					t.Fatalf("%s, %d sound reads: %v %#v", at, sound, err, in)
				}
				failed++
			}
			if failed < tc.fails {
				t.Fatalf("%s: only %d reads failed", at, failed)
			}
		}
	}
}

// Content that is no valid publication is still an inspection Admit
// refuses 409 "invalid Bazel Modules manifest or archive", never an error.
func TestUnitInspectStillRefusesInvalidUploads(t *testing.T) {
	corrupt := helloZip(t, zip.Store)
	corrupt[bytes.Index(corrupt, []byte("hello from"))] ^= 1 // the directory keeps the old CRC
	var unknown bytes.Buffer
	z := zip.NewWriter(&unknown)
	w, err := z.CreateRaw(&zip.FileHeader{
		Name: "hello-1.0.0/BUILD.bazel", Method: 99, CompressedSize64: 1, UncompressedSize64: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, "x"); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	c := repo.Config{Key: "bazel", PackageType: repo.BazelModules, Class: repo.Local}
	for name, tc := range map[string]struct {
		path string
		body []byte
	}{
		"other module": {"modules/hello/1.0.0/MODULE.bazel", []byte(`module(name = "other", version = "1.0.0")`)},
		"empty module": {"modules/hello/1.0.0/MODULE.bazel", nil},
		"no zip":       {"modules/hello/1.0.0/hello.zip", []byte("not a zip archive")},
		"gzip named .zip": {
			"modules/hello/1.0.0/hello.zip",
			helloTarGz(t, [2]string{"hello-1.0.0/BUILD.bazel", "x"}),
		},
		"no gzip":      {"modules/hello/1.0.0/hello.tar.gz", []byte("not gzip")},
		"corrupt zip":  {"modules/hello/1.0.0/hello.zip", []byte("PK\x03\x04garbage")},
		"bad checksum": {"modules/hello/1.0.0/hello.zip", corrupt},
		"bad method":   {"modules/hello/1.0.0/hello.zip", unknown.Bytes()},
		"escaping entry": {
			"modules/hello/1.0.0/hello.tar.gz",
			helloTarGz(t, [2]string{"../escape/BUILD.bazel", "x"}),
		},
	} {
		blobs := cutBlobs{body: tc.body, sound: -1} // counts down from -1, never to 0
		in, err := inspect(t, blobs, tc.path)
		checked, ok := in.(*inspection)
		if err != nil || !ok || checked.err == nil {
			t.Errorf("%s: %#v %v", name, in, err)
			continue
		}
		_, err = Handler{}.Admit(t.Context(), format.Env{Inspection: in}, c, format.Write{
			Path: tc.path, Checksums: kernel.Checksums{SHA256: "sha"}, Op: format.OpDeploy,
		})
		var refused *format.RejectionError
		if !errors.As(err, &refused) || refused.Status != http.StatusConflict ||
			refused.Message != "invalid Bazel Modules manifest or archive" {
			t.Errorf("%s: admitted with %v", name, err)
		}
	}
}
