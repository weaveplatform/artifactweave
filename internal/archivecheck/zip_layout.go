package archivecheck

import (
	"archive/zip"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"slices"
)

type zipEntryLayout struct {
	file *zip.File
	data int64
}

// Native Vagrant extracts with tar, which may read ZIP local records in physical
// order. Validate the same complete entry stream that a streaming extractor sees,
// rather than trusting only the names in the central directory.
func validateZIPLayout(
	ctx context.Context,
	r io.ReaderAt,
	archive *zip.Reader,
	directoryOffset int64,
) error {
	entries := make([]zipEntryLayout, 0, len(archive.File))
	for _, f := range archive.File {
		offset, err := f.DataOffset()
		if err != nil {
			return fmt.Errorf("vagrant ZIP local offset: %w", err)
		}
		if offset < 30 || offset > directoryOffset {
			return invalid("invalid ZIP local offset")
		}
		entries = append(entries, zipEntryLayout{file: f, data: offset})
	}
	slices.SortFunc(entries, func(a, b zipEntryLayout) int {
		switch {
		case a.data < b.data:
			return -1
		case a.data > b.data:
			return 1
		default:
			return 0
		}
	})
	var cursor int64
	for i, entry := range entries {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("vagrant ZIP layout canceled: %w", err)
		}
		if i > 0 && entry.data == entries[i-1].data {
			return invalid("duplicate ZIP local record")
		}
		if err := validateZIPLocal(r, cursor, entry); err != nil {
			return err
		}
		if entry.file.CompressedSize64 > math.MaxInt64 {
			return invalid("ZIP payload size exceeds limit")
		}
		compressedSize := int64(entry.file.CompressedSize64)
		if compressedSize > directoryOffset-entry.data {
			return invalid("ZIP payload overlaps central directory")
		}
		cursor = entry.data + compressedSize
		if entry.file.Flags&8 != 0 {
			var nextData int64
			if i+1 < len(entries) {
				nextData = entries[i+1].data
			}
			var err error
			cursor, err = zipDescriptorEnd(r, cursor, directoryOffset, nextData, entry.file)
			if err != nil {
				return err
			}
		}
	}
	if cursor != directoryOffset {
		return invalid("unindexed ZIP local records or padding")
	}
	return nil
}

func validateZIPLocal(r io.ReaderAt, cursor int64, entry zipEntryLayout) error {
	var header [30]byte
	if cursor < 0 || cursor > entry.data-30 {
		return invalid("overlapping ZIP local records")
	}
	if _, err := r.ReadAt(header[:], cursor); err != nil {
		return fmt.Errorf("vagrant ZIP local header: %w", err)
	}
	nameLength := int(binary.LittleEndian.Uint16(header[26:]))
	extraLength := int(binary.LittleEndian.Uint16(header[28:]))
	f := entry.file
	if f.Flags & ^uint16(0x080e) != 0 || f.Method != zip.Deflate && f.Flags&6 != 0 {
		return invalid("unsupported ZIP encryption or flags")
	}
	if binary.LittleEndian.Uint32(header[:]) != 0x04034b50 ||
		binary.LittleEndian.Uint16(header[6:]) != f.Flags ||
		binary.LittleEndian.Uint16(header[8:]) != f.Method ||
		cursor+30+int64(nameLength+extraLength) != entry.data {
		return invalid("ZIP local header differs from central directory")
	}
	fields := make([]byte, nameLength+extraLength)
	if _, err := r.ReadAt(fields, cursor+30); err != nil {
		return fmt.Errorf("vagrant ZIP local fields: %w", err)
	}
	if string(fields[:nameLength]) != f.Name {
		return invalid("ZIP local filename differs from central directory")
	}
	compressed := uint64(binary.LittleEndian.Uint32(header[18:]))
	uncompressed := uint64(binary.LittleEndian.Uint32(header[22:]))
	var err error
	compressed, uncompressed, err = zipLocalSizes(
		fields[nameLength:],
		f.Name,
		compressed,
		uncompressed,
	)
	if err != nil {
		return err
	}
	if _, _, err := zipLocalSizes(f.Extra, f.Name, 0, 0); err != nil {
		return err
	}
	checksum := binary.LittleEndian.Uint32(header[14:])
	if f.Flags&8 != 0 {
		if f.Method == zip.Store &&
			(compressed == 0 && f.CompressedSize64 != 0 || uncompressed == 0 && f.UncompressedSize64 != 0) {
			return invalid("stored ZIP entry requires local sizes")
		}
		if checksum != 0 && checksum != f.CRC32 ||
			compressed != 0 && compressed != f.CompressedSize64 ||
			uncompressed != 0 && uncompressed != f.UncompressedSize64 {
			return invalid("ZIP local descriptor claims disagree")
		}
	} else if checksum != f.CRC32 || compressed != f.CompressedSize64 || uncompressed != f.UncompressedSize64 {
		return invalid("ZIP local sizes or checksum disagree")
	}
	return nil
}

func zipLocalSizes(
	extra []byte,
	name string,
	compressed, uncompressed uint64,
) (uint64, uint64, error) {
	var wide []byte
	for len(extra) > 0 {
		if len(extra) < 4 || int(binary.LittleEndian.Uint16(extra[2:])) > len(extra)-4 {
			return 0, 0, invalid("malformed ZIP extra field")
		}
		kind, size := binary.LittleEndian.Uint16(extra), int(binary.LittleEndian.Uint16(extra[2:]))
		data := extra[4 : 4+size]
		extra = extra[4+size:]
		switch kind {
		case 0x0001:
			if wide != nil {
				return 0, 0, invalid("duplicate ZIP64 extra field")
			}
			wide = data
		case 0x7075:
			if len(data) < 5 || data[0] != 1 ||
				binary.LittleEndian.Uint32(data[1:]) != crc32.ChecksumIEEE([]byte(name)) ||
				string(data[5:]) != name {
				return 0, 0, invalid("ZIP alternate Unicode filename")
			}
		case 0x5455, 0x5855, 0x7855, 0x7875, 0x000a, 0x6375:
			// Timestamps, numeric user/group IDs, NTFS timestamps and comments
			// do not change the entry path or turn a regular file into a link.
		default:
			return 0, 0, invalid("unsupported ZIP extra field")
		}
	}
	for _, value := range []*uint64{&uncompressed, &compressed} {
		if *value == 0xffffffff {
			if len(wide) < 8 {
				return 0, 0, invalid("missing ZIP64 local size")
			}
			*value = binary.LittleEndian.Uint64(wide)
			wide = wide[8:]
		}
	}
	return compressed, uncompressed, nil
}

func zipDescriptorEnd(r io.ReaderAt, cursor, boundary, nextData int64, f *zip.File) (int64, error) {
	data := make([]byte, int(min(int64(24), boundary-cursor)))
	if _, err := r.ReadAt(data, cursor); err != nil {
		return 0, fmt.Errorf("vagrant ZIP descriptor: %w", err)
	}
	var match int64
	for _, size := range []int{12, 16, 20, 24} {
		if size > len(data) || !zipDescriptorMatches(data[:size], f) {
			continue
		}
		end := cursor + int64(size)
		if !zipNextRecord(r, end, boundary, nextData) {
			continue
		}
		if match != 0 {
			return 0, invalid("ambiguous ZIP descriptor")
		}
		match = end
	}
	if match == 0 {
		return 0, invalid("ZIP descriptor differs from central directory")
	}
	return match, nil
}

func zipDescriptorMatches(data []byte, f *zip.File) bool {
	if len(data) == 16 || len(data) == 24 {
		if binary.LittleEndian.Uint32(data) != 0x08074b50 {
			return false
		}
		data = data[4:]
	}
	if binary.LittleEndian.Uint32(data) != f.CRC32 {
		return false
	}
	if len(data) == 12 {
		return uint64(binary.LittleEndian.Uint32(data[4:])) == f.CompressedSize64 &&
			uint64(binary.LittleEndian.Uint32(data[8:])) == f.UncompressedSize64
	}
	return binary.LittleEndian.Uint64(data[4:]) == f.CompressedSize64 &&
		binary.LittleEndian.Uint64(data[12:]) == f.UncompressedSize64
}

func zipNextRecord(r io.ReaderAt, cursor, boundary, data int64) bool {
	if data == 0 {
		return cursor == boundary
	}
	if cursor < 0 || cursor > data-30 {
		return false
	}
	var header [30]byte
	if _, err := r.ReadAt(header[:], cursor); err != nil {
		return false
	}
	return binary.LittleEndian.Uint32(header[:]) == 0x04034b50 &&
		cursor+30+int64(
			binary.LittleEndian.Uint16(header[26:]),
		)+int64(
			binary.LittleEndian.Uint16(header[28:]),
		) == data
}
