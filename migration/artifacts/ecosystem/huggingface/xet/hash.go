// Package xet validates the public Hugging Face Xet chunk and shard wire formats.
package xet

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/zeebo/blake3"
)

var (
	dataKey = [32]byte{
		102,
		151,
		245,
		119,
		91,
		149,
		80,
		222,
		49,
		53,
		203,
		172,
		165,
		151,
		24,
		28,
		157,
		228,
		33,
		16,
		155,
		235,
		43,
		88,
		180,
		208,
		176,
		75,
		147,
		173,
		242,
		41,
	}
	nodeKey = [32]byte{
		1,
		126,
		197,
		199,
		165,
		71,
		41,
		150,
		253,
		148,
		102,
		102,
		180,
		138,
		2,
		230,
		93,
		221,
		83,
		111,
		55,
		199,
		109,
		210,
		248,
		99,
		82,
		230,
		74,
		83,
		113,
		63,
	}
	verifyKey = [32]byte{
		127,
		24,
		87,
		214,
		206,
		86,
		237,
		102,
		18,
		127,
		249,
		19,
		231,
		165,
		195,
		243,
		164,
		205,
		38,
		213,
		181,
		219,
		73,
		230,
		65,
		36,
		152,
		127,
		40,
		251,
		148,
		195,
	}
)

// Hash is the binary Merkle hash. Xet's textual form reverses each uint64 word.
type Hash [32]byte

func (h Hash) String() string {
	return fmt.Sprintf(
		"%016x%016x%016x%016x",
		binary.LittleEndian.Uint64(h[:8]),
		binary.LittleEndian.Uint64(h[8:16]),
		binary.LittleEndian.Uint64(h[16:24]),
		binary.LittleEndian.Uint64(h[24:]),
	)
}

func ParseHash(s string) (Hash, bool) {
	var h Hash
	if len(s) != 64 || s != strings.ToLower(s) {
		return h, false
	}
	data, err := hex.DecodeString(s)
	if err != nil {
		return h, false
	}
	for i := range 4 {
		for j := range 8 {
			h[i*8+j] = data[i*8+7-j]
		}
	}
	return h, true
}

func keyed(key [32]byte, data []byte) Hash {
	// The key is an array, so NewKeyed's only error (a non-32-byte key) is impossible.
	h, _ := blake3.NewKeyed(key[:])
	_, _ = h.Write(data)
	var out Hash
	copy(out[:], h.Sum(nil))
	return out
}

func DataHash(data []byte) Hash { return keyed(dataKey, data) }

// HashPart carries the chunk's uncompressed length, which is part of its parent hash.
type HashPart struct {
	Hash Hash
	Size uint64
}

func Aggregate(parts []HashPart) Hash {
	if len(parts) == 0 {
		return Hash{}
	}
	parts = append([]HashPart(nil), parts...)
	for len(parts) > 1 {
		next := make([]HashPart, 0, (len(parts)+1)/2)
		for len(parts) > 0 {
			end := min(9, len(parts))
			for i := 2; i < end; i++ {
				if binary.LittleEndian.Uint64(parts[i].Hash[24:])%4 == 0 {
					end = i + 1
					break
				}
			}
			var input strings.Builder
			var size uint64
			for _, p := range parts[:end] {
				fmt.Fprintf(&input, "%s : %d\n", p.Hash.String(), p.Size)
				size += p.Size
			}
			next = append(next, HashPart{Hash: keyed(nodeKey, []byte(input.String())), Size: size})
			parts = parts[end:]
		}
		parts = next
	}
	return parts[0].Hash
}

func FileHash(parts []HashPart) Hash {
	if len(parts) == 0 {
		return Hash{}
	}
	h := Aggregate(parts)
	return keyed([32]byte{}, h[:])
}

func RangeHash(parts []HashPart) Hash {
	data := make([]byte, 0, len(parts)*32)
	for _, p := range parts {
		data = append(data, p.Hash[:]...)
	}
	return keyed(verifyKey, data)
}
