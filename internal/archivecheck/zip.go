// Package archivecheck validates bounded archive structure before native-client extraction.
package archivecheck

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

// ZIPLimits bounds archive bytes and central-directory parsing allocations.
type ZIPLimits struct {
	ArchiveBytes   int64
	DirectoryBytes int64
	Entries        uint64
}

// ZIP validates both directory and physical entry layout before exposing files.
// Decompression keeps the standard library checksum checks and rejects hidden
// trailing data inside a deflate stream.
func ZIP(ctx context.Context, r io.ReaderAt, size int64, limits ZIPLimits) (*zip.Reader, error) {
	if limits.ArchiveBytes <= 0 || limits.DirectoryBytes <= 0 || limits.Entries == 0 {
		return nil, invalid("invalid ZIP limits")
	}
	offset, err := preflightZIP(ctx, r, size, limits)
	if err != nil {
		return nil, err
	}
	archive, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("ZIP directory: %w", err)
	}
	if err := validateZIPLayout(ctx, r, archive, offset); err != nil {
		return nil, err
	}
	registerStrictDeflate(archive)
	return archive, nil
}

// archive/zip allocates central-directory entries before exposing File. Bound
// and count that directory first, including ZIP64 boxes with large VM disks.
func preflightZIP(ctx context.Context, r io.ReaderAt, size int64, limits ZIPLimits) (int64, error) {
	if size <= 0 || size > limits.ArchiveBytes {
		return 0, invalid("ZIP size exceeds limit")
	}
	tailSize := min(size, int64(65535+22))
	tail := make([]byte, int(tailSize))
	if _, err := r.ReadAt(tail, size-tailSize); err != nil {
		return 0, fmt.Errorf("ZIP footer: %w", err)
	}
	end := -1
	// Match archive/zip's backwards EOCD search. Trailing bytes are refused so
	// neither reader can pick a different footer hidden in a comment.
	for i := len(tail) - 22; i >= 0; i-- {
		if binary.LittleEndian.Uint32(tail[i:]) == 0x06054b50 &&
			int(binary.LittleEndian.Uint16(tail[i+20:])) <= len(tail)-i-22 {
			end = i
			break
		}
	}
	if end < 0 || end+22+int(binary.LittleEndian.Uint16(tail[end+20:])) != len(tail) {
		return 0, invalid("missing or ambiguous ZIP footer")
	}
	e := tail[end:]
	if binary.LittleEndian.Uint16(e[4:]) != 0 || binary.LittleEndian.Uint16(e[6:]) != 0 ||
		binary.LittleEndian.Uint16(e[8:]) != binary.LittleEndian.Uint16(e[10:]) {
		return 0, invalid("multi-disk ZIP is unsupported")
	}
	count := uint64(binary.LittleEndian.Uint16(e[10:]))
	length := uint64(binary.LittleEndian.Uint32(e[12:]))
	offset := uint64(binary.LittleEndian.Uint32(e[16:]))
	endAt := size - tailSize + int64(end)
	boundary := uint64(endAt) //nolint:gosec // size >= tailSize and end >= 0 above.

	if count == 0xffff || length == 0xffffffff || offset == 0xffffffff {
		var err error
		count, length, offset, boundary, err = zip64Directory(r, boundary, limits.ArchiveBytes)
		if err != nil {
			return 0, err
		}
	}
	if offset > math.MaxInt64 || length > math.MaxInt64 {
		return 0, invalid("ZIP central directory exceeds limit")
	}
	directoryOffset, directoryBytes := int64(offset), int64(length)
	if count > limits.Entries || directoryBytes > limits.DirectoryBytes || offset > boundary ||
		length != boundary-offset || directoryOffset > limits.ArchiveBytes {
		return 0, invalid("ZIP central directory exceeds limit")
	}
	if err := scanZIPDirectory(
		ctx,
		io.NewSectionReader(r, directoryOffset, directoryBytes),
		count,
	); err != nil {
		return 0, err
	}
	return directoryOffset, nil
}

func scanZIPDirectory(ctx context.Context, r io.Reader, count uint64) error {
	br := bufio.NewReader(r)
	var entries uint64
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("ZIP directory canceled: %w", err)
		}
		var header [46]byte
		n, err := io.ReadFull(br, header[:])
		if n == 0 && err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("ZIP directory: %w", err)
		}
		if binary.LittleEndian.Uint32(header[:]) != 0x02014b50 {
			return invalid("invalid ZIP central directory")
		}
		variable := uint32(
			binary.LittleEndian.Uint16(header[28:]),
		) + uint32(
			binary.LittleEndian.Uint16(header[30:]),
		) + uint32(
			binary.LittleEndian.Uint16(header[32:]),
		)
		entries++
		if entries > count {
			return invalid("ZIP directory count mismatch")
		}
		if _, err := io.CopyN(io.Discard, br, int64(variable)); err != nil {
			return fmt.Errorf("ZIP directory fields: %w", err)
		}
	}
	if entries != count {
		return invalid("ZIP directory count mismatch")
	}
	return nil
}

func zip64Directory(
	r io.ReaderAt,
	end uint64,
	maxArchiveBytes int64,
) (count, length, offset, boundary uint64, err error) {
	if end > math.MaxInt64 {
		return 0, 0, 0, 0, invalid("missing ZIP64 locator")
	}
	endAt := int64(end)
	if endAt < 20 || endAt > maxArchiveBytes {
		return 0, 0, 0, 0, invalid("missing ZIP64 locator")
	}
	var locator [20]byte
	if _, err := r.ReadAt(locator[:], endAt-20); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("ZIP64 locator: %w", err)
	}
	if binary.LittleEndian.Uint32(locator[:]) != 0x07064b50 ||
		binary.LittleEndian.Uint32(locator[4:]) != 0 ||
		binary.LittleEndian.Uint32(locator[16:]) != 1 {
		return 0, 0, 0, 0, invalid("invalid ZIP64 locator")
	}
	boundary = binary.LittleEndian.Uint64(locator[8:])
	if boundary > math.MaxInt64 {
		return 0, 0, 0, 0, invalid("invalid ZIP64 footer offset")
	}
	boundaryAt := int64(boundary)
	if boundaryAt > maxArchiveBytes || boundaryAt > endAt-20 || endAt-20-boundaryAt < 56 {
		return 0, 0, 0, 0, invalid("invalid ZIP64 footer offset")
	}
	var record [56]byte
	if _, err := r.ReadAt(record[:], boundaryAt); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("ZIP64 footer: %w", err)
	}
	recordSize := binary.LittleEndian.Uint64(record[4:])
	if binary.LittleEndian.Uint32(record[:]) != 0x06064b50 || recordSize < 44 ||
		recordSize != end-20-boundary-12 ||
		binary.LittleEndian.Uint32(record[16:]) != 0 ||
		binary.LittleEndian.Uint32(record[20:]) != 0 ||
		binary.LittleEndian.Uint64(record[24:]) != binary.LittleEndian.Uint64(record[32:]) {
		return 0, 0, 0, 0, invalid("invalid ZIP64 footer")
	}
	count = binary.LittleEndian.Uint64(record[32:])
	length = binary.LittleEndian.Uint64(record[40:])
	offset = binary.LittleEndian.Uint64(record[48:])
	return count, length, offset, boundary, nil
}
