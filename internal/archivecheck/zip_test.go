package archivecheck

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"testing"
)

var testZIPLimits = ZIPLimits{ArchiveBytes: 16 << 30, DirectoryBytes: 32 << 20, Entries: 100000}

type zipFixtureEntry struct{ name, body string }

func zipFixture(t *testing.T, entries []zipFixtureEntry) []byte {
	t.Helper()
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	for _, entry := range entries {
		f, err := writer.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(f, entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func checkZIPFixture(ctx context.Context, r io.ReaderAt, size int64) (*zip.Reader, error) {
	return ZIP(ctx, r, size, testZIPLimits)
}

func preflightFixture(ctx context.Context, r io.ReaderAt, size int64) (int64, error) {
	return preflightZIP(ctx, r, size, testZIPLimits)
}

func zip64Fixture(t *testing.T) []byte {
	t.Helper()
	data := zipFixture(t, []zipFixtureEntry{{"metadata.json", `{"provider":"virtualbox"}`}})
	end := len(data) - 22
	e := bytes.Clone(data[end:])
	record := make([]byte, 56)
	locator := make([]byte, 20)
	binary.LittleEndian.PutUint32(record, 0x06064b50)
	binary.LittleEndian.PutUint64(record[4:], 44)
	binary.LittleEndian.PutUint64(record[24:], 1)
	binary.LittleEndian.PutUint64(record[32:], 1)
	binary.LittleEndian.PutUint64(record[40:], uint64(binary.LittleEndian.Uint32(e[12:])))
	binary.LittleEndian.PutUint64(record[48:], uint64(binary.LittleEndian.Uint32(e[16:])))
	binary.LittleEndian.PutUint32(locator, 0x07064b50)
	binary.LittleEndian.PutUint64(locator[8:], uint64(end))
	binary.LittleEndian.PutUint32(locator[16:], 1)
	binary.LittleEndian.PutUint16(e[8:], 0xffff)
	binary.LittleEndian.PutUint16(e[10:], 0xffff)
	binary.LittleEndian.PutUint32(e[12:], 0xffffffff)
	binary.LittleEndian.PutUint32(e[16:], 0xffffffff)
	return append(append(append(data[:end:end], record...), locator...), e...)
}

func TestUnitZIPDirectoryLimitsBeforeParserAllocation(t *testing.T) {
	standard := zipFixture(t, []zipFixtureEntry{{"metadata.json", `{"provider":"virtualbox"}`}})
	wide := zip64Fixture(t)
	if b, err := checkZIPFixture(
		t.Context(),
		bytes.NewReader(wide),
		int64(len(wide)),
	); err != nil ||
		len(b.File) != 1 {
		t.Fatalf("ZIP64: %+v %v", b, err)
	}
	for _, tc := range []struct {
		name   string
		wide   bool
		mutate func([]byte)
	}{
		{"many declared records", true, func(b []byte) {
			p := len(b) - 98
			binary.LittleEndian.PutUint64(b[p+24:], testZIPLimits.Entries+1)
			binary.LittleEndian.PutUint64(b[p+32:], testZIPLimits.Entries+1)
		}},
		{"large directory", true, func(b []byte) { binary.LittleEndian.PutUint64(b[len(b)-58:], uint64(testZIPLimits.DirectoryBytes+1)) }},
		{"directory offset", true, func(b []byte) { binary.LittleEndian.PutUint64(b[len(b)-50:], ^uint64(0)) }},
		{"count mismatch", false, func(b []byte) {
			binary.LittleEndian.PutUint16(b[len(b)-14:], 2)
			binary.LittleEndian.PutUint16(b[len(b)-12:], 2)
		}},
		{"multi disk", false, func(b []byte) { b[len(b)-18] = 1 }},
		{"bad directory signature", false, func(b []byte) { p := binary.LittleEndian.Uint32(b[len(b)-6:]); b[p] = 0 }},
		{"directory fields overflow", false, func(b []byte) {
			p := binary.LittleEndian.Uint32(b[len(b)-6:])
			binary.LittleEndian.PutUint16(b[int(p)+28:], 0xffff)
		}},
		{"missing locator", true, func(b []byte) { b[len(b)-42] = 0 }},
		{"locator disk", true, func(b []byte) { b[len(b)-38] = 1 }},
		{"locator offset", true, func(b []byte) { binary.LittleEndian.PutUint64(b[len(b)-34:], ^uint64(0)) }},
		{"invalid wide signature", true, func(b []byte) { b[len(b)-98] = 0 }},
		{"invalid wide length", true, func(b []byte) { b[len(b)-94] = 0 }},
		{"wide disk", true, func(b []byte) { b[len(b)-82] = 1 }},
		{"wide disk count mismatch", true, func(b []byte) { b[len(b)-74] = 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := bytes.Clone(standard)
			if tc.wide {
				data = bytes.Clone(wide)
			}
			tc.mutate(data)
			if _, err := checkZIPFixture(
				t.Context(),
				bytes.NewReader(data),
				int64(len(data)),
			); err == nil {
				t.Fatal("malformed directory accepted")
			}
		})
	}
	// Record counts are checked against the actual bounded directory, not trusted
	// from a footer that can deliberately understate the allocation required.
	for _, data := range [][]byte{append(bytes.Clone(standard), 0), []byte("PKbroken")} {
		if _, err := checkZIPFixture(
			t.Context(),
			bytes.NewReader(data),
			int64(len(data)),
		); err == nil {
			t.Fatal("ambiguous footer accepted")
		}
	}
}

func TestUnitZIP64LargeOffsets(t *testing.T) {
	data := zip64Fixture(t)
	record := data[len(data)-98:]
	offset := binary.LittleEndian.Uint64(record[48:])
	length := binary.LittleEndian.Uint64(record[40:])
	// Sparse padding avoids allocating or writing a VM disk while proving that
	// the preflight keeps ZIP64 offsets above 4 GiB intact.
	const padding = uint64(1) << 32
	f, err := os.CreateTemp(t.TempDir(), "wide-*.box")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	binary.LittleEndian.PutUint64(record[48:], offset+padding)
	binary.LittleEndian.PutUint64(data[len(data)-34:], offset+padding+length)
	if _, err := f.WriteAt(data[int(offset):], int64(offset+padding)); err != nil {
		t.Fatal(err)
	}
	if _, err := preflightFixture(t.Context(), f, int64(len(data))+int64(padding)); err != nil {
		t.Fatalf("wide offset: %v", err)
	}
}

type preflightFailAtReader struct {
	io.ReaderAt
	offset int64
}

func (r preflightFailAtReader) ReadAt(b []byte, off int64) (int, error) {
	if off == r.offset {
		return 0, io.ErrUnexpectedEOF
	}
	return r.ReaderAt.ReadAt(b, off)
}

func TestUnitZIPDirectoryReadFailures(t *testing.T) {
	data := zip64Fixture(t)
	for _, off := range []int64{0, int64(len(data) - 42), int64(len(data) - 98), int64(binary.LittleEndian.Uint64(data[len(data)-50:]))} {
		if _, err := preflightFixture(
			t.Context(),
			preflightFailAtReader{bytes.NewReader(data), off},
			int64(len(data)),
		); err == nil {
			t.Fatalf("read error at %d ignored", off)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := preflightFixture(
		ctx,
		bytes.NewReader(data),
		int64(len(data)),
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("directory cancellation: %v", err)
	}
	if _, _, _, _, err := zip64Directory(
		bytes.NewReader(nil),
		1,
		testZIPLimits.ArchiveBytes,
	); err == nil {
		t.Fatal("missing ZIP64 locator accepted")
	}
}

func TestUnitZIPRejectsUncountedDirectoryGap(t *testing.T) {
	data := zipFixture(t, []zipFixtureEntry{{"metadata.json", `{"provider":"virtualbox"}`}})
	end := len(data) - 22
	offset := int(binary.LittleEndian.Uint32(data[end+16:]))
	// The footer claims only the first record. Go's ZIP reader otherwise scans
	// both records and accepts certain larger multiples of 65536 extra records.
	extra := bytes.Clone(data[offset:end])
	attack := append(append(bytes.Clone(data[:end]), extra...), data[end:]...)
	if _, err := checkZIPFixture(
		t.Context(),
		bytes.NewReader(attack),
		int64(len(attack)),
	); err == nil {
		t.Fatal("uncounted directory gap accepted")
	}
}
