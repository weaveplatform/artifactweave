package cocoapods

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestUnitSourceArchivePreservesCancellation(t *testing.T) {
	for _, ext := range []string{".zip", ".tar.gz"} {
		t.Run(ext, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			body := cancelSourceRead{Reader: bytes.NewReader([]byte("archive")), cancel: cancel}
			if err := ValidateSourceArchive(ctx, body, 7, ext); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled source validation: %v", err)
			}
			if err := ValidateSourceArchive(ctx, body, 7, ext); !errors.Is(err, context.Canceled) {
				t.Fatalf("already-canceled source validation: %v", err)
			}
		})
	}
}

type cancelSourceRead struct {
	*bytes.Reader
	cancel context.CancelFunc
}

func (r cancelSourceRead) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.cancel()
	return n, err
}

func TestUnitSourceZIPRejectsDifferentLocalFilename(t *testing.T) {
	var output bytes.Buffer
	w := zip.NewWriter(&output)
	f, err := w.Create("HelloPod.m")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("void hello(void) {}\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data := output.Bytes()
	// archive/zip exposes the safe central name while native extractors can
	// observe this conflicting local-header path in the unchanged served ZIP.
	copy(data[30:40], "../bad.m!!")
	if err := ValidateSourceArchive(
		t.Context(),
		bytes.NewReader(data),
		int64(len(data)),
		".zip",
	); err == nil {
		t.Fatal("ZIP with traversal in its local filename was admitted")
	}
}

func TestUnitPodArchivesBoundHiddenPAXExpansion(t *testing.T) {
	var output bytes.Buffer
	gz := gzip.NewWriter(&output)
	tw := tar.NewWriter(gz)
	metadata := strings.Repeat("x", 512<<10)
	// PAX records are consumed inside tar.Reader.Next and do not contribute to
	// the returned file size. Compress a little over the real aggregate limit
	// without retaining hundreds of MiB in memory or writing a large fixture.
	for i := 0; i <= maxExpandedBytes/len(metadata); i++ {
		if err := tw.WriteHeader(&tar.Header{
			Name: fmt.Sprintf("empty-%d", i), Mode: 0o644, Format: tar.FormatPAX,
			PAXRecords: map[string]string{"comment": metadata},
		}); err != nil {
			t.Fatal(err)
		}
	}
	spec := `{"name":"HelloPod","version":"1.0.0"}`
	if err := tw.WriteHeader(&tar.Header{
		Name: "HelloPod.podspec.json", Mode: 0o644, Size: int64(len(spec)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(spec)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	data := output.Bytes()
	if _, err := ReadArchive(
		bytes.NewReader(data),
		int64(len(data)),
		"HelloPod",
		"1.0.0",
	); err == nil {
		t.Error("local pod archive admitted hidden PAX expansion above the limit")
	}
	if err := ValidateSourceArchive(t.Context(),
		bytes.NewReader(data),
		int64(len(data)),
		".tar.gz",
	); err == nil {
		t.Error("remote pod archive admitted hidden PAX expansion above the limit")
	}
}
