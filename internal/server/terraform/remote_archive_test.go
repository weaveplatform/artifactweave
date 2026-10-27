package terraform

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/ulikunitz/xz"
)

func archiveTar(t *testing.T, names ...string) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, name := range names {
		data := []byte("Metadata-Version: 2.1\nName: demo\nVersion: 1.0\n\n")
		if err := tw.WriteHeader(
			&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data))},
		); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func archiveGzip(t *testing.T, b []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	if _, err := gz.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func archiveZip(t *testing.T, names ...string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for _, name := range names {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte("payload")); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func completeArchive(t *testing.T, b []byte, name string, wantValid bool) {
	t.Helper()
	err := validateCompleteArchive(
		t.Context(),
		bytes.NewReader(b),
		int64(len(b)),
		name,
		remoteArchiveEntries,
		remoteArchiveInflated,
	)
	if (err == nil) != wantValid {
		t.Fatalf("%s validation=%v want valid=%v", name, err, wantValid)
	}
}

func TestUnitRemoteArchiveCompleteValidation(t *testing.T) {
	tarBytes := archiveTar(t, "PKG-INFO", "demo/data.txt")
	gz := archiveGzip(t, tarBytes)
	bz, err := os.ReadFile("../../format/pypi/testdata/demo_pkg-1.0.tar.bz2")
	if err != nil {
		t.Fatal(err)
	}
	zipBytes := archiveZip(t, "demo-1.0.dist-info/METADATA", "demo/data.txt")
	for name, b := range map[string][]byte{"demo.tar": tarBytes, "demo.tar.gz": gz, "demo.tar.bz2": bz, "demo.zip": zipBytes} {
		t.Run(name, func(t *testing.T) { completeArchive(t, b, name, true) })
	}
	corruptGzip := bytes.Clone(gz)
	corruptGzip[len(corruptGzip)-8] ^= 0xff
	corruptBzip := bytes.Clone(bz)
	corruptBzip[len(corruptBzip)-2] ^= 0xff
	corruptZip := bytes.Clone(zipBytes)
	zr, _ := zip.NewReader(bytes.NewReader(corruptZip), int64(len(corruptZip)))
	offset, _ := zr.File[1].DataOffset()
	corruptZip[offset] ^= 0xff
	for name, b := range map[string][]byte{"gzip trailer": corruptGzip, "gzip truncated": gz[:len(gz)-4]} {
		t.Run(name, func(t *testing.T) { completeArchive(t, b, "demo.tar.gz", false) })
	}
	completeArchive(t, corruptBzip, "demo.tar.bz2", false)
	completeArchive(t, corruptZip, "demo.zip", false)
	completeArchive(t, tarBytes[:600], "demo.tar", false)
	completeArchive(t, append(bytes.Clone(tarBytes), 1), "demo.tar", false)
	completeArchive(t, append(bytes.Clone(gz), gz...), "demo.tar.gz", false)
	completeArchive(t, []byte("not gzip"), "demo.tar.gz", false)
	completeArchive(t, []byte("not zip"), "demo.zip", false)
	completeArchive(t, []byte("not an archive"), "demo.unknown", false)
	completeArchive(t, archiveTar(t, "../escape"), "demo.tar", false)
	completeArchive(t, archiveTar(t, "duplicate", "duplicate"), "demo.tar", false)
	completeArchive(t, archiveZip(t, "../escape"), "demo.zip", false)
	completeArchive(t, archiveZip(t, "duplicate", "duplicate"), "demo.zip", false)
}

func TestUnitRemoteArchiveBoundsAndCancellation(t *testing.T) {
	for name, b := range map[string][]byte{"demo.tar": archiveTar(t, "a", "b"), "demo.zip": archiveZip(t, "a", "b")} {
		for _, limit := range []struct {
			entries  int
			inflated int64
		}{{1, remoteArchiveInflated}, {10, 2}} {
			if validateCompleteArchive(
				t.Context(),
				bytes.NewReader(b),
				int64(len(b)),
				name,
				limit.entries,
				limit.inflated,
			) == nil {
				t.Fatal("limit ignored", name, limit)
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := validateCompleteArchive(
			ctx,
			bytes.NewReader(b),
			int64(len(b)),
			name,
			10,
			remoteArchiveInflated,
		); !errors.Is(
			err,
			context.Canceled,
		) {
			t.Fatal(err)
		}
		ctx, cancel = context.WithCancel(t.Context())
		ra := cancelReaderAt{r: bytes.NewReader(b), cancel: cancel}
		if err := validateCompleteArchive(
			ctx,
			ra,
			int64(len(b)),
			name,
			10,
			remoteArchiveInflated,
		); !errors.Is(
			err,
			context.Canceled,
		) {
			t.Fatal(err)
		}
	}
	remaining := int64(-1)
	if _, err := (archiveReader{ctx: t.Context(), r: bytes.NewReader(nil), remaining: &remaining}).Read(
		make([]byte, 1),
	); err == nil {
		t.Fatal("negative limit")
	}
	ra := &archiveReaderAt{ctx: t.Context(), r: bytes.NewReader(nil), isBounded: true, remaining: 0}
	if _, err := ra.ReadAt(make([]byte, 1), 0); err == nil {
		t.Fatal("directory limit")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	f := moduleSource{Archive: "tar.gz"}
	if err := f.validateArchive(ctx, bytes.NewReader(nil), 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type cancelReaderAt struct {
	r      io.ReaderAt
	cancel context.CancelFunc
}

func (r cancelReaderAt) ReadAt(p []byte, off int64) (int, error) {
	n, err := r.r.ReadAt(p, off)
	r.cancel()
	return n, err
} //nolint:wrapcheck // ReaderAt test

func TestUnitRemoteZIPDirectoryBounds(t *testing.T) {
	b := archiveZip(t, "one")
	for _, change := range []func([]byte){
		func(b []byte) { binary.LittleEndian.PutUint32(b[len(b)-10:], uint32(remoteZipDirectoryLimit+1)) },
		func(b []byte) { binary.LittleEndian.PutUint16(b[len(b)-12:], 0xffff) },
		func(b []byte) { b[len(b)-22] = 0 },
		func(b []byte) { binary.LittleEndian.PutUint16(b[len(b)-2:], 1) },
		func(b []byte) { b[0] = 0 },
	} {
		mutated := bytes.Clone(b)
		change(mutated)
		completeArchive(t, mutated, "demo.zip", false)
	}
	// A syntactically valid ZIP64 directory hint and its out-of-bounds forms.
	zip64 := make([]byte, 98)
	binary.LittleEndian.PutUint32(zip64, 0x06064b50)
	binary.LittleEndian.PutUint32(zip64[56:], 0x07064b50)
	binary.LittleEndian.PutUint32(zip64[76:], 0x06054b50)
	binary.LittleEndian.PutUint16(zip64[86:], 0xffff)
	if err := zipDirectoryBound(bytes.NewReader(zip64), int64(len(zip64)), 10); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func([]byte){
		func(b []byte) { b[56] = 0 },
		func(b []byte) { binary.LittleEndian.PutUint64(b[64:], 1000) },
		func(b []byte) { b[0] = 0 },
		func(b []byte) { binary.LittleEndian.PutUint64(b[32:], 1000) },
	} {
		mutated := bytes.Clone(zip64)
		change(mutated)
		if zipDirectoryBound(bytes.NewReader(mutated), int64(len(mutated)), 10) == nil {
			t.Fatal("bad ZIP64 accepted")
		}
	}
}

func TestUnitRemoteTarLinks(t *testing.T) {
	for _, target := range []string{"data.txt", "../data.txt", "/escape", "../../escape", "C:/escape", "bad\\path"} {
		for _, kind := range []byte{tar.TypeSymlink, tar.TypeLink} {
			var b bytes.Buffer
			tw := tar.NewWriter(&b)
			if err := tw.WriteHeader(
				&tar.Header{Name: "demo/link", Linkname: target, Typeflag: kind, Mode: 0o600},
			); err != nil {
				t.Fatal(err)
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			valid := target == "data.txt" || (target == "../data.txt" && kind == tar.TypeSymlink)
			completeArchive(t, b.Bytes(), "demo.tar", valid)
		}
	}
}

func TestUnitRemoteModuleSubdirectoryValidation(t *testing.T) {
	for _, kind := range []string{"zip", "tar.gz", "tar.xz"} {
		var b []byte
		switch kind {
		case "zip":
			b = archiveZip(t, "root/modules/demo/main.tf", "root/modules/other/main.tf")
		case "tar.gz":
			b = archiveGzip(
				t,
				archiveTar(t, "root/modules/demo/main.tf", "root/modules/other/main.tf"),
			)
		case "tar.xz":
			b = archiveXZ(
				t,
				archiveTar(t, "root/modules/demo/main.tf", "root/modules/other/main.tf"),
			)
		}
		for _, selector := range []string{"root/modules/demo", "*/modules/demo", "root/modules/[d]emo", "root/modules/*", "missing"} {
			s := moduleSource{Archive: kind, Subdir: selector}
			err := s.validateArchive(t.Context(), bytes.NewReader(b), int64(len(b)))
			valid := selector != "root/modules/*" && selector != "missing"
			if (err == nil) != valid {
				t.Fatalf("%s %s: %v", kind, selector, err)
			}
		}
	}
}

func archiveXZ(t *testing.T, b []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	x, err := xz.NewWriter(&out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = x.Write(b); err != nil {
		t.Fatal(err)
	}
	if err = x.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestUnitRemoteModuleCompressionTrailers(t *testing.T) {
	for _, kind := range []string{"tar.xz", "xz", "gz", "bz2"} {
		var b []byte
		switch kind {
		case "tar.xz":
			b = archiveXZ(t, archiveTar(t, "main.tf"))
		case "xz":
			b = archiveXZ(t, []byte("module bytes"))
		case "gz":
			b = archiveGzip(t, []byte("module bytes"))
		case "bz2":
			var err error
			b, err = os.ReadFile("../../format/pypi/testdata/demo_pkg-1.0.tar.bz2")
			if err != nil {
				t.Fatal(err)
			}
		}
		completeArchive(t, b, "module."+kind, true)
		broken := bytes.Clone(b)
		broken[len(broken)-2] ^= 0xff
		completeArchive(t, broken, "module."+kind, false)
		completeArchive(t, b[:len(b)-4], "module."+kind, false)
	}
	completeArchive(t, []byte("bad xz"), "module.tar.xz", false)
}

func TestUnitRemoteModuleSafeNestedSymlinks(t *testing.T) {
	for _, unsafe := range []bool{false, true} {
		var b bytes.Buffer
		tw := tar.NewWriter(&b)
		for _, h := range []*tar.Header{{Name: "root/modules/demo/main.tf", Size: 1, Mode: 0o600}, {Name: "root/modules/demo/alias", Linkname: "main.tf", Typeflag: tar.TypeSymlink, Mode: 0o600}} {
			if err := tw.WriteHeader(h); err != nil {
				t.Fatal(err)
			}
			if h.Size > 0 {
				_, _ = tw.Write([]byte("x"))
			}
		}
		if unsafe {
			if err := tw.WriteHeader(
				&tar.Header{Name: "root/modules/demo/alias/escape.tf", Size: 1, Mode: 0o600},
			); err != nil {
				t.Fatal(err)
			}
			_, _ = tw.Write([]byte("x"))
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		s := moduleSource{Archive: "tar", Subdir: "*/modules/demo"}
		err := s.validateArchive(t.Context(), bytes.NewReader(b.Bytes()), int64(b.Len()))
		if (err != nil) != unsafe {
			t.Fatalf("unsafe=%v err=%v", unsafe, err)
		}
	}
	for _, target := range []string{"main.tf", "../../escape", "/escape", ""} {
		var b bytes.Buffer
		z := zip.NewWriter(&b)
		h := &zip.FileHeader{Name: "root/link"}
		h.SetMode(0o600 | os.ModeSymlink)
		w, err := z.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(target))
		if err = z.Close(); err != nil {
			t.Fatal(err)
		}
		completeArchive(t, b.Bytes(), "module.zip", target == "main.tf")
	}
}
