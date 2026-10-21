package nuget

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

const msfMagic = "Microsoft C/C++ MSF 7.00\r\n\x1aDS\x00\x00\x00"

// PDBKey reads only the identity of a Microsoft PDB v7 or Portable PDB
// v1.0. The GUID uses Windows GUID byte order; the age is unpadded hex.
// Portable PDBs use the GUID part of their 20-byte ID and FFFFFFFF, not
// the trailing four-byte stamp (Microsoft's SSQP key conventions).
func PDBKey(b []byte) (string, bool) {
	if bytes.HasPrefix(b, []byte(msfMagic)) {
		return msfKey(b)
	}
	if bytes.HasPrefix(b, []byte("BSJB")) {
		return portableKey(b)
	}
	return "", false
}

func pdbGUID(b []byte) string {
	return fmt.Sprintf("%08X%04X%04X%X", binary.LittleEndian.Uint32(b),
		binary.LittleEndian.Uint16(b[4:]), binary.LittleEndian.Uint16(b[6:]), b[8:16])
}

// span validates offsets before converting them to slice indexes. Every
// count and offset in these formats is supplied by the uploader.
func span(b []byte, off, size uint64) ([]byte, bool) {
	if off > uint64(len(b)) || size > uint64(len(b))-off {
		return nil, false
	}
	return b[off : off+size], true
}

func portableKey(b []byte) (string, bool) {
	if len(b) < 20 {
		return "", false
	}
	pos := uint64(16) + uint64(binary.LittleEndian.Uint32(b[12:]))
	header, ok := span(b, pos, 4)
	if !ok {
		return "", false
	}
	count := binary.LittleEndian.Uint16(header[2:])
	pos += 4
	for range count {
		h, ok := span(b, pos, 8)
		if !ok {
			return "", false
		}
		off, size := uint64(
			binary.LittleEndian.Uint32(h),
		), uint64(
			binary.LittleEndian.Uint32(h[4:]),
		)
		pos += 8
		name, _ := span(b, pos, min(32, uint64(len(b))-pos))
		end := bytes.IndexByte(name, 0)
		if end < 0 {
			return "", false
		}
		// A stream name is at most 32 bytes, including NUL, aligned to four.
		pos += uint64((end + 4) & ^3) //nolint:gosec // end is in [0,31]
		data, ok := span(b, off, size)
		if !ok {
			return "", false
		}
		if string(name[:end]) == "#Pdb" {
			if len(data) < 20 {
				return "", false
			}
			return pdbGUID(data) + "FFFFFFFF", true
		}
	}
	return "", false
}

func msfKey(b []byte) (string, bool) {
	if len(b) < 56 {
		return "", false
	}
	block := uint64(binary.LittleEndian.Uint32(b[32:]))
	blocks := uint64(binary.LittleEndian.Uint32(b[40:]))
	size := uint64(binary.LittleEndian.Uint32(b[44:]))
	blockMap := uint64(binary.LittleEndian.Uint32(b[52:]))
	if block < 512 || block > 65536 || block&(block-1) != 0 ||
		blocks*block != uint64(len(b)) || size < 12 || size > uint64(len(b)) {
		return "", false
	}
	count := (size + block - 1) / block
	// MSF7's directory block numbers fit in one block-map block.
	if count > block/4 {
		return "", false
	}
	indices, ok := span(b, blockMap*block, count*4)
	if !ok {
		return "", false
	}
	dir := make([]byte, 0, size)
	for i := uint64(0); i < count; i++ {
		off := uint64(binary.LittleEndian.Uint32(indices[i*4:])) * block
		part, ok := span(b, off, min(block, size-uint64(len(dir))))
		if !ok {
			return "", false
		}
		dir = append(dir, part...)
	}
	return msfDirectoryKey(b, dir, block)
}

func msfDirectoryKey(b, dir []byte, block uint64) (string, bool) {
	count := uint64(binary.LittleEndian.Uint32(dir))
	if count < 2 || count > (uint64(len(dir))-4)/4 {
		return "", false
	}
	size0 := binary.LittleEndian.Uint32(dir[4:])
	size1 := binary.LittleEndian.Uint32(dir[8:])
	if size1 < 28 || size1 == ^uint32(0) {
		return "", false
	}
	var skip uint64
	if size0 != ^uint32(0) { // FFFFFFFF is an absent stream, consuming no blocks.
		skip = (uint64(size0) + block - 1) / block
	}
	index, ok := span(dir, 4+count*4+skip*4, 4)
	if !ok {
		return "", false
	}
	info, ok := span(b, uint64(binary.LittleEndian.Uint32(index))*block, 28)
	if !ok {
		return "", false
	}
	return pdbGUID(info[12:]) + fmt.Sprintf("%X", binary.LittleEndian.Uint32(info[8:])), true
}
