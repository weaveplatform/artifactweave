package xet

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/pierrec/lz4/v4"
)

const (
	MaxChunk       = 128 << 10
	MaxXorb        = 64 << 20
	MaxEncodedXorb = 80 << 20
	MaxChunks      = 8192
)

var ErrInvalid = errors.New("invalid Xet content")

type Chunk struct {
	Hash  Hash
	Data  []byte
	Start int
	End   int
}

type Xorb struct {
	Hash   Hash
	Chunks []Chunk
}

func invalid(reason string) error { return fmt.Errorf("%w: %s", ErrInvalid, reason) }

// ParseXorb authenticates every chunk, both boundary tables, and the root hash
// before the object can be referenced by a file reconstruction.
func ParseXorb(data []byte, want string) (Xorb, error) {
	var result Xorb
	expected, ok := ParseHash(want)
	if !ok || len(data) < 8 || len(data) > MaxEncodedXorb {
		return result, invalid("xorb length or identity")
	}
	// Uploads use only chunk records; stored objects may append an indexed footer.
	footerLen := int(binary.LittleEndian.Uint32(data[len(data)-4:]))
	if footerLen < 60 || footerLen > len(data)-4 ||
		!bytes.Equal(data[len(data)-4-footerLen:len(data)-4-footerLen+7], []byte("XETBLOB")) {
		chunks, err := decodeChunks(data)
		if err != nil {
			return result, err
		}
		if Aggregate(Parts(chunks)) != expected {
			return result, invalid("xorb root digest differs")
		}
		return Xorb{Hash: expected, Chunks: chunks}, nil
	}
	start := len(data) - 4 - footerLen
	footer := data[start : len(data)-4]
	if !bytes.Equal(footer[:7], []byte("XETBLOB")) || footer[7] > 1 {
		return result, invalid("xorb footer version")
	}
	copy(result.Hash[:], footer[8:40])
	if result.Hash != expected {
		return result, invalid("xorb identity differs")
	}
	chunks, err := decodeChunks(data[:start])
	if err != nil {
		return result, err
	}
	if err = checkFooter(footer, chunks); err != nil {
		return result, err
	}
	parts := Parts(chunks)
	if Aggregate(parts) != expected {
		return result, invalid("xorb root digest differs")
	}
	result.Chunks = chunks
	return result, nil
}

func checkFooter(f []byte, chunks []Chunk) error {
	n := len(chunks)
	var hashes, boundaries, unpacked []byte
	if f[7] == 0 {
		if len(f) != 60+36*n || int(binary.LittleEndian.Uint32(f[40:44])) != n {
			return invalid("legacy xorb chunk count")
		}
		boundaries = f[44 : 44+4*n]
		hashes = f[44+4*n : 44+36*n]
	} else {
		if len(f) != 92+40*n || !bytes.Equal(f[40:48], []byte("XBLBHSH\x00")) ||
			int(binary.LittleEndian.Uint32(f[48:52])) != n {
			return invalid("xorb hash table")
		}
		hashes = f[52 : 52+32*n]
		pos := 52 + 32*n
		if !bytes.Equal(f[pos:pos+8], []byte("XBLBBND\x01")) ||
			int(binary.LittleEndian.Uint32(f[pos+8:pos+12])) != n {
			return invalid("xorb boundary table")
		}
		boundaries = f[pos+12 : pos+12+4*n]
		unpacked = f[pos+12+4*n : pos+12+8*n]
		tail := f[pos+12+8*n:]
		if int(binary.LittleEndian.Uint32(tail[:4])) != n ||
			int(binary.LittleEndian.Uint32(tail[4:8])) != len(f)-40 ||
			int(binary.LittleEndian.Uint32(tail[8:12])) != len(f)-pos {
			return invalid("xorb table offsets")
		}
	}
	total := 0
	for i, c := range chunks {
		total += len(c.Data)
		if !bytes.Equal(hashes[i*32:(i+1)*32], c.Hash[:]) ||
			int(binary.LittleEndian.Uint32(boundaries[i*4:])) != c.End {
			return invalid("xorb chunk verification")
		}
		if unpacked != nil && int(binary.LittleEndian.Uint32(unpacked[i*4:])) != total {
			return invalid("xorb unpacked boundary")
		}
	}
	return nil
}

func decodeChunks(data []byte) ([]Chunk, error) {
	var chunks []Chunk
	total := 0
	for pos := 0; pos < len(data); {
		if len(data)-pos < 8 || len(chunks) >= MaxChunks {
			return nil, invalid("chunk header")
		}
		hdr := data[pos : pos+8]
		packed := int(hdr[1]) | int(hdr[2])<<8 | int(hdr[3])<<16
		unpacked := int(hdr[5]) | int(hdr[6])<<8 | int(hdr[7])<<16
		if hdr[0] != 0 || hdr[4] > 2 || unpacked == 0 || unpacked > MaxChunk || packed == 0 ||
			packed > MaxChunk+1024 ||
			packed > len(data)-pos-8 ||
			total+unpacked > MaxXorb {
			return nil, invalid("chunk sizes or encoding")
		}
		payload := data[pos+8 : pos+8+packed]
		out := payload
		if hdr[4] != 0 {
			var err error
			out, err = io.ReadAll(
				io.LimitReader(lz4.NewReader(bytes.NewReader(payload)), int64(unpacked)+1),
			)
			if err != nil {
				return nil, invalid("chunk compression")
			}
		}
		if len(out) != unpacked {
			return nil, invalid("chunk expanded size")
		}
		if hdr[4] == 2 {
			out = regroup(out)
		}
		end := pos + 8 + packed
		chunks = append(chunks, Chunk{Hash: DataHash(out), Data: out, Start: pos, End: end})
		total += len(out)
		pos = end
	}
	if len(chunks) == 0 {
		return nil, invalid("empty xorb")
	}
	return chunks, nil
}

func regroup(data []byte) []byte {
	result := make([]byte, len(data))
	at := 0
	for group := range 4 {
		for i := group; i < len(data); i += 4 {
			result[i] = data[at]
			at++
		}
	}
	return result
}

func Parts(chunks []Chunk) []HashPart {
	parts := make([]HashPart, len(chunks))
	for i, c := range chunks {
		parts[i] = HashPart{Hash: c.Hash, Size: uint64(len(c.Data))}
	}
	return parts
}

// EncodeChunks writes the uncompressed transfer representation, suitable for
// a signed byte-range response. A reconstruction never needs the xorb footer.
func EncodeChunks(chunks [][]byte) ([]byte, error) {
	var out []byte
	for _, c := range chunks {
		if len(c) == 0 || len(c) > MaxChunk {
			return nil, invalid("chunk length")
		}
		n := len(c)
		out = append(
			out,
			0,
			byte(n&255),
			byte((n>>8)&255),
			byte((n>>16)&255),
			0,
			byte(n&255),
			byte((n>>8)&255),
			byte((n>>16)&255),
		)
		out = append(out, c...)
	}
	return out, nil
}
