package xet

import (
	"bytes"
	"encoding/binary"
)

const MaxShard = 64 << 20

var shardMagic = []byte{
	'H',
	'F',
	'R',
	'e',
	'p',
	'o',
	'M',
	'e',
	't',
	'a',
	'D',
	'a',
	't',
	'a',
	0,
	85,
	105,
	103,
	69,
	106,
	123,
	129,
	87,
	131,
	165,
	189,
	217,
	92,
	205,
	209,
	74,
	169,
}

type Segment struct {
	Xorb         Hash
	Size         uint32
	Start        uint32
	End          uint32
	Verification Hash
}
type File struct {
	Hash     Hash
	SHA256   string
	Segments []Segment
	Verified bool
}
type ShardXorb struct {
	Hash        Hash
	Size        uint32
	EncodedSize uint32
	Chunks      []HashPart
}
type Shard struct {
	Files []File
	Xorbs []ShardXorb
}

// ParseShard accepts the version-2 MDB wire format. Every section is bounded
// before entry counts are used; upload-only lookup tables are never trusted.
func ParseShard(data []byte) (Shard, error) {
	var s Shard
	if len(data) < 48+48+48 || len(data) > MaxShard || !bytes.Equal(data[:32], shardMagic) ||
		u64(data[32:]) != 2 {
		return s, invalid("shard header")
	}
	if u64(data[40:]) == 0 {
		end, err := fileSectionEnd(data[48:])
		if err != nil {
			return s, err
		}
		s.Files, err = parseFiles(data[48 : 48+end])
		if err != nil {
			return s, err
		}
		s.Xorbs, err = parseShardXorbs(data[48+end:])
		if err != nil {
			return s, err
		}
		if len(s.Files) == 0 {
			return s, invalid("shard file count")
		}
		return s, nil
	}
	if u64(data[40:]) != 200 || len(data) < 344 {
		return s, invalid("shard footer size")
	}
	footerAt := uint64(len(data)) - 200
	f := data[footerAt:]
	if u64(f) != 1 || u64(f[192:]) != footerAt || u64(f[8:]) != 48 {
		return s, invalid("shard footer")
	}
	offsets := []uint64{
		u64(f[8:]),
		u64(f[16:]),
		u64(f[24:]),
		u64(f[40:]),
		u64(f[56:]),
		footerAt,
	}
	var previous uint64
	for _, o := range offsets {
		if o > footerAt || o < previous {
			return s, invalid("shard section offsets")
		}
		previous = o
	}
	// Lookup entries contain one u64 hash fragment and u32 location (file/xorb),
	// or two u32 locations for chunk entries. A minimal uploaded shard has none.
	if u64(f[32:]) > (offsets[3]-offsets[2])/12 || u64(f[48:]) > (offsets[4]-offsets[3])/12 ||
		u64(f[64:]) > (offsets[5]-offsets[4])/16 {
		return s, invalid("shard lookup bounds")
	}
	var err error
	s.Files, err = parseFiles(data[offsets[0]:offsets[1]])
	if err != nil {
		return s, err
	}
	s.Xorbs, err = parseShardXorbs(data[offsets[1]:offsets[2]])
	if err != nil {
		return s, err
	}
	if len(s.Files) == 0 || len(s.Files) > 256 {
		return s, invalid("shard file count")
	}
	return s, nil
}

func parseFiles(data []byte) ([]File, error) {
	var files []File
	seen := map[Hash]bool{}
	for len(data) >= 48 {
		header := data[:48]
		data = data[48:]
		if bookend(header) {
			if len(data) != 0 {
				return nil, invalid("file section trailing bytes")
			}
			return files, nil
		}
		n := uint64(u32(header[36:]))
		flags := u32(header[32:])
		if n == 0 || n > 1<<20 || flags & ^uint32(3<<30) != 0 {
			return nil, invalid("file flags or segment count")
		}
		count := n
		if flags&(1<<31) != 0 {
			count += n
		}
		if flags&(1<<30) != 0 {
			count++
		}
		if count > uint64(len(data))/48 {
			return nil, invalid("file section truncated")
		}
		var file File
		copy(file.Hash[:], header[:32])
		file.Verified = flags&(1<<31) != 0
		if seen[file.Hash] {
			return nil, invalid("duplicate file")
		}
		seen[file.Hash] = true
		for range n {
			entry := data[:48]
			data = data[48:]
			var segment Segment
			copy(segment.Xorb[:], entry[:32])
			segment.Size = u32(entry[36:])
			segment.Start = u32(entry[40:])
			segment.End = u32(entry[44:])
			if u32(entry[32:]) != 0 || segment.Size == 0 || segment.Start >= segment.End ||
				segment.End > MaxChunks {
				return nil, invalid("file segment bounds")
			}
			file.Segments = append(file.Segments, segment)
		}
		if file.Verified {
			for i := range file.Segments {
				copy(file.Segments[i].Verification[:], data[:32])
				data = data[48:]
			}
		}
		if flags&(1<<30) != 0 {
			var sha Hash
			copy(sha[:], data[:32])
			file.SHA256 = sha.String()
			data = data[48:]
		}
		files = append(files, file)
		if len(files) > 256 {
			return nil, invalid("too many shard files")
		}
	}
	return nil, invalid("missing file section marker")
}

func parseShardXorbs(data []byte) ([]ShardXorb, error) {
	var xorbs []ShardXorb
	seen := map[Hash]bool{}
	for len(data) >= 48 {
		header := data[:48]
		data = data[48:]
		if bookend(header) {
			if len(data) != 0 {
				return nil, invalid("xorb section trailing bytes")
			}
			return xorbs, nil
		}
		n := uint64(u32(header[36:]))
		if n == 0 || n > MaxChunks || n > uint64(len(data))/48 || u32(header[32:]) != 0 {
			return nil, invalid("shard xorb entry count")
		}
		var x ShardXorb
		copy(x.Hash[:], header[:32])
		x.Size = u32(header[40:])
		x.EncodedSize = u32(header[44:])
		if seen[x.Hash] {
			return nil, invalid("duplicate xorb")
		}
		seen[x.Hash] = true
		var total uint64
		for range n {
			e := data[:48]
			data = data[48:]
			var part HashPart
			copy(part.Hash[:], e[:32])
			part.Size = uint64(u32(e[36:]))
			if part.Size == 0 || part.Size > MaxChunk || uint64(u32(e[32:])) != total ||
				u32(e[40:]) & ^uint32(1<<31) != 0 {
				return nil, invalid("shard xorb chunk")
			}
			total += part.Size
			x.Chunks = append(x.Chunks, part)
		}
		if total > MaxXorb || total != uint64(x.Size) || Aggregate(x.Chunks) != x.Hash {
			return nil, invalid("shard xorb digest")
		}
		xorbs = append(xorbs, x)
	}
	return nil, invalid("missing xorb section marker")
}

func bookend(b []byte) bool {
	for _, v := range b[:32] {
		if v != 255 {
			return false
		}
	}
	return true
}
func u32(b []byte) uint32 { return binary.LittleEndian.Uint32(b[:4]) }
func u64(b []byte) uint64 { return binary.LittleEndian.Uint64(b[:8]) }

func fileSectionEnd(data []byte) (int, error) {
	for pos := 0; pos+48 <= len(data); {
		header := data[pos : pos+48]
		if bookend(header) {
			return pos + 48, nil
		}
		n := uint64(u32(header[36:]))
		flags := u32(header[32:])
		if flags&(1<<31) != 0 {
			n *= 2
		}
		if flags&(1<<30) != 0 {
			n++
		}
		if n > MaxShard/48 || int(n) > (len(data)-pos-48)/48 {
			return 0, invalid("shard segment length")
		}
		pos += 48 + int(n)*48
	}
	return 0, invalid("missing file section marker")
}
