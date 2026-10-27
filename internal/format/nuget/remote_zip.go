package nuget

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net/url"
	"path"
	"strings"
)

const (
	remoteDirectoryLimit = 32 << 20
	remoteEntryLimit     = 100000
)

// Bound the directory before either archive/zip reader can allocate its File
// slice. Counts in EOCD are untrusted: walk the actual records as well. NuGet
// packages start at byte zero; multi-disk and self-extracting ZIPs are not nupkg.
func remoteZIPDirectory(r io.ReaderAt, size int64) error {
	if size < 22 {
		return ErrNotPackage
	}
	tail := make([]byte, min(size, 22+65535))
	if _, err := r.ReadAt(tail, size-int64(len(tail))); err != nil {
		return fmt.Errorf("remote nuget directory: %w", err)
	}
	end := -1
	for i := len(tail) - 22; i >= 0; i-- {
		if binary.LittleEndian.Uint32(tail[i:]) == 0x06054b50 &&
			i+22+int(binary.LittleEndian.Uint16(tail[i+20:])) == len(tail) {
			end = i
			break
		}
	}
	if end < 0 {
		return ErrNotPackage
	}
	e := tail[end:]
	if binary.LittleEndian.Uint32(e[4:]) != 0 ||
		binary.LittleEndian.Uint16(e[8:]) != binary.LittleEndian.Uint16(e[10:]) {
		return ErrNotPackage
	}
	count := uint64(binary.LittleEndian.Uint16(e[10:]))
	length, offset := uint64(
		binary.LittleEndian.Uint32(e[12:]),
	), uint64(
		binary.LittleEndian.Uint32(e[16:]),
	)
	boundary := size - int64(len(tail)) + int64(end)
	if count == 0xffff || length == 0xffffffff || offset == 0xffffffff {
		var err error
		count, length, offset, boundary, err = remoteZIP64(r, boundary)
		if err != nil {
			return err
		}
	}
	if boundary < 0 || offset > math.MaxInt64 || count == 0 || count > remoteEntryLimit ||
		length > remoteDirectoryLimit ||
		offset > uint64(boundary) ||
		length != uint64(boundary)-offset {
		return ErrNotPackage
	}
	return remoteZIPRecords(r, int64(offset), int64(length), count)
}

func remoteZIP64(
	r io.ReaderAt,
	end int64,
) (count, length, offset uint64, boundary int64, err error) {
	fail := func() (uint64, uint64, uint64, int64, error) { return 0, 0, 0, 0, ErrNotPackage }
	if end < 20 {
		return fail()
	}
	var locator [20]byte
	if _, err = r.ReadAt(locator[:], end-20); err != nil {
		return fail()
	}
	if binary.LittleEndian.Uint32(locator[:]) != 0x07064b50 ||
		binary.LittleEndian.Uint32(locator[4:]) != 0 ||
		binary.LittleEndian.Uint32(locator[16:]) != 1 {
		return fail()
	}
	start := binary.LittleEndian.Uint64(locator[8:])
	if start > math.MaxInt64 || start > uint64(end-20) || uint64(end-20)-start < 56 {
		return fail()
	}
	var record [56]byte
	if _, err = r.ReadAt(record[:], int64(start)); err != nil {
		return fail()
	}
	if binary.LittleEndian.Uint32(record[:]) != 0x06064b50 ||
		binary.LittleEndian.Uint64(record[4:]) != uint64(end-20)-start-12 ||
		binary.LittleEndian.Uint64(record[16:]) != 0 {
		return fail()
	}
	count = binary.LittleEndian.Uint64(record[32:])
	if count != binary.LittleEndian.Uint64(record[24:]) {
		return fail()
	}
	return count, binary.LittleEndian.Uint64(
			record[40:],
		), binary.LittleEndian.Uint64(
			record[48:],
		), int64(
			start,
		), nil
}

func remoteZIPRecords(r io.ReaderAt, offset, length int64, count uint64) error {
	var records uint64
	var header [46]byte
	end := offset + length
	for offset < end {
		if end-offset < int64(len(header)) {
			return ErrNotPackage
		}
		if _, err := r.ReadAt(header[:], offset); err != nil {
			return fmt.Errorf("remote nuget entry directory: %w", err)
		}
		if binary.LittleEndian.Uint32(header[:]) != 0x02014b50 {
			return ErrNotPackage
		}
		n := int64(
			len(header),
		) + int64(
			binary.LittleEndian.Uint16(header[28:]),
		) + int64(
			binary.LittleEndian.Uint16(header[30:]),
		) + int64(
			binary.LittleEndian.Uint16(header[32:]),
		)
		if n > end-offset {
			return ErrNotPackage
		}
		offset += n
		records++
		if records > remoteEntryLimit {
			return ErrNotPackage
		}
	}
	if records != count {
		return ErrNotPackage
	}
	return nil
}

// NuGet unescapes entry names before checking the extraction destination. Check
// both slash conventions so a package admitted on Linux stays safe on Windows.
func remoteEntryName(name string) bool {
	decoded, err := url.PathUnescape(name)
	if err != nil || strings.ContainsAny(decoded, "\x00:") {
		return false
	}
	decoded = strings.ReplaceAll(decoded, "\\", "/")
	if strings.HasPrefix(decoded, "/") {
		return false
	}
	clean := path.Clean(decoded)
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}
