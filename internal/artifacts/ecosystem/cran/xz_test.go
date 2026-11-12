package cran

import (
	"archive/tar"
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/ulikunitz/xz"
)

// A .tar.xz source package whose xz stream lost its index and footer is
// refused as any unreadable package is, as R's untar refuses it through
// liblzma, and the whole package is still read (#1331). The tar reader
// stops at the archive's end, so only the drain after it reads on to the
// end of the stream.
func TestUnitXZSourcePackagesNeedTheirWholeStream(t *testing.T) {
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	const description = "Package: demo\nVersion: 1.0\nDescription: xz source\n"
	if err := tw.WriteHeader(&tar.Header{
		Name: "demo/DESCRIPTION", Mode: 0o644, Size: int64(len(description)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(description)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	w, err := xz.NewWriter(&compressed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(archive.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	whole := compressed.Bytes()
	footer := whole[len(whole)-12:]
	cut := whole[:len(whole)-int(binary.LittleEndian.Uint32(footer[4:8])+1)*4-12]
	for _, p := range []string{"src/contrib/demo_1.0.tar.xz", "src/contrib/Archive/demo/demo_1.0.tar.xz"} {
		if _, err := ReadPackage(
			t.Context(),
			bytes.NewReader(whole),
			int64(len(whole)),
			p,
		); err != nil {
			t.Fatalf("whole %s: %v", p, err)
		}
		if _, err := ReadPackage(
			t.Context(), bytes.NewReader(cut), int64(len(cut)), p,
		); !errors.Is(err, ErrPackage) {
			t.Errorf("%s without its index: %v, want ErrPackage", p, err)
		}
	}
}
