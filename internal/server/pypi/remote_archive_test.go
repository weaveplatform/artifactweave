package pypi

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
	for name, b := range map[string][]byte{"demo.tar": tarBytes, "demo.tar.gz": gz, "demo.tgz": gz, "demo.tar.bz2": bz, "demo.zip": zipBytes, "demo.whl": zipBytes, "demo.egg": zipBytes} {
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
	completeArchive(t, corruptZip, "demo.whl", false)
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
	// Both advertised hash and package identity match; the trailer is still invalid.
	f := remoteFile{name: "demo-1.0.tar.gz", path: "demo/1.0/demo-1.0.tar.gz", version: "1.0"}
	if err = f.validateArtifact(t.Context(), bytes.NewReader(gz), int64(len(gz))); err != nil {
		t.Fatal(err)
	}
	if err = f.validateArtifact(
		t.Context(),
		bytes.NewReader(corruptGzip),
		int64(len(corruptGzip)),
	); err == nil {
		t.Fatal("corrupt distribution admitted")
	}
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
	ra := &archiveReaderAt{ctx: t.Context(), r: bytes.NewReader(nil), bounded: true, remaining: 0}
	if _, err := ra.ReadAt(make([]byte, 1), 0); err == nil {
		t.Fatal("directory limit")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	f := remoteFile{name: "demo-1.0.tar.gz"}
	if err := f.validateArtifact(ctx, bytes.NewReader(nil), 0); !errors.Is(err, context.Canceled) {
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
