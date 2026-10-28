package xet

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/pierrec/lz4/v4"
)

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

const nativeRoot = "18f7794cb6160bd4aa1d12851866b19cc8cc2473b2b35c8e0b80fb7bb5bec5fb"

func TestUnitNativeWireReconstruction(t *testing.T) {
	for _, name := range []string{"native", "large"} {
		t.Run(name, func(t *testing.T) {
			shard, err := ParseShard(fixture(t, name+".shard"))
			if err != nil {
				t.Fatal(err)
			}
			if len(shard.Xorbs) != 1 {
				t.Fatal("fixture must contain a single native xorb")
			}
			xorb, err := ParseXorb(fixture(t, name+".xorb"), shard.Xorbs[0].Hash.String())
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range shard.Files {
				var parts []HashPart
				for _, segment := range file.Segments {
					selected := Parts(xorb.Chunks[segment.Start:segment.End])
					if RangeHash(selected) != segment.Verification {
						t.Fatal("native segment verification differs")
					}
					parts = append(parts, selected...)
				}
				if FileHash(parts) != file.Hash {
					t.Fatal("native file identity differs")
				}
			}
		})
	}
}

func TestUnitXetHashGrammarAndKnownNativeRoots(t *testing.T) {
	for _, value := range []string{"", strings.Repeat("A", 64), strings.Repeat("z", 64), strings.Repeat("a", 63)} {
		if _, ok := ParseHash(value); ok {
			t.Fatalf("accepted %q", value)
		}
	}
	hash, ok := ParseHash(nativeRoot)
	if !ok || hash.String() != nativeRoot {
		t.Fatal("wire hash round trip")
	}
	if Aggregate(nil) != (Hash{}) || FileHash(nil) != (Hash{}) {
		t.Fatal("empty root")
	}
	parts := make([]HashPart, 40)
	for i := range parts {
		parts[i] = HashPart{Hash: DataHash([]byte{byte(i)}), Size: 1}
	}
	before := append([]HashPart(nil), parts...)
	root := Aggregate(parts)
	if root == (Hash{}) || !bytes.Equal(before[0].Hash[:], parts[0].Hash[:]) {
		t.Fatal("aggregation mutated its inputs")
	}
}

func indexedXorb(t *testing.T, version byte) ([]byte, string, int) {
	t.Helper()
	raw, err := EncodeChunks([][]byte{[]byte("hello"), []byte("world!")})
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := decodeChunks(raw)
	if err != nil {
		t.Fatal(err)
	}
	root := Aggregate(Parts(chunks))
	n := len(chunks)
	size := 60 + 36*n
	if version == 1 {
		size = 92 + 40*n
	}
	footer := make([]byte, size)
	copy(footer, "XETBLOB")
	footer[7] = version
	copy(footer[8:], root[:])
	if version == 0 {
		binary.LittleEndian.PutUint32(footer[40:], uint32(n))
		for i, c := range chunks {
			binary.LittleEndian.PutUint32(footer[44+4*i:], uint32(c.End))
			copy(footer[44+4*n+32*i:], c.Hash[:])
		}
	} else {
		copy(footer[40:], "XBLBHSH\x00")
		binary.LittleEndian.PutUint32(footer[48:], uint32(n))
		for i, c := range chunks {
			copy(footer[52+32*i:], c.Hash[:])
		}
		pos := 52 + 32*n
		copy(footer[pos:], "XBLBBND\x01")
		binary.LittleEndian.PutUint32(footer[pos+8:], uint32(n))
		total := 0
		for i, c := range chunks {
			total += len(c.Data)
			binary.LittleEndian.PutUint32(footer[pos+12+4*i:], uint32(c.End))
			binary.LittleEndian.PutUint32(footer[pos+12+4*n+4*i:], uint32(total))
		}
		tail := footer[pos+12+8*n:]
		binary.LittleEndian.PutUint32(tail, uint32(n))
		binary.LittleEndian.PutUint32(tail[4:], uint32(size-40))
		binary.LittleEndian.PutUint32(tail[8:], uint32(size-pos))
	}
	result := append(bytes.Clone(raw), footer...)
	result = binary.LittleEndian.AppendUint32(result, uint32(size))
	return result, root.String(), len(raw)
}

func TestUnitIndexedXorbIntegrity(t *testing.T) {
	for _, v := range []byte{0, 1} {
		data, hash, start := indexedXorb(t, v)
		parsed, err := ParseXorb(data, hash)
		if err != nil || len(parsed.Chunks) != 2 {
			t.Fatalf("footer version %d: %v", v, err)
		}
		positions := []int{start + 7, start + 8, start + 40, start + 44}
		if v == 1 {
			positions = append(
				positions,
				start+48,
				start+52,
				start+116,
				start+124,
				start+128,
				start+136,
				start+144,
			)
		}
		for _, pos := range positions {
			bad := bytes.Clone(data)
			bad[pos] ^= 128
			if _, err := ParseXorb(bad, hash); !errors.Is(err, ErrInvalid) {
				t.Fatalf("version %d accepted changed footer byte %d: %v", v, pos-start, err)
			}
		}
		bad := bytes.Clone(data)
		bad[8] ^= 1
		if _, err := ParseXorb(bad, hash); err == nil {
			t.Fatal("accepted modified chunk bytes")
		}
	}
}

func TestUnitXorbCompressionAndLimits(t *testing.T) {
	data := bytes.Repeat([]byte("abcd"), 500)
	var packed bytes.Buffer
	writer := lz4.NewWriter(&packed)
	if _, e := writer.Write(data); e != nil {
		t.Fatal(e)
	}
	if e := writer.Close(); e != nil {
		t.Fatal(e)
	}
	n := packed.Len()
	raw := []byte{
		0,
		byte(n),
		byte(n >> 8),
		byte(n >> 16),
		1,
		byte(len(data)),
		byte(len(data) >> 8),
		byte(len(data) >> 16),
	}
	raw = append(raw, packed.Bytes()...)
	if _, e := ParseXorb(raw, DataHash(data).String()); e != nil {
		t.Fatal(e)
	}
	malformed := [][]byte{
		nil,
		{},
		make([]byte, MaxEncodedXorb+1),
		[]byte("1234567"),
		append(bytes.Clone(raw), 1),
	}
	for _, pos := range []int{0, 1, 4, 5, 6, 7, 8} {
		bad := bytes.Clone(raw)
		bad[pos] ^= 255
		malformed = append(malformed, bad)
	}
	for _, bad := range malformed {
		if _, e := ParseXorb(bad, DataHash(data).String()); !errors.Is(e, ErrInvalid) {
			t.Fatalf("invalid xorb accepted len=%d: %v", len(bad), e)
		}
	}
	if _, e := ParseXorb(raw, "not-a-hash"); e == nil {
		t.Fatal("invalid root accepted")
	}
	if _, e := ParseXorb(raw, Hash{}.String()); e == nil {
		t.Fatal("wrong root accepted")
	}
	for _, c := range [][][]byte{{nil}, {make([]byte, MaxChunk+1)}} {
		if _, e := EncodeChunks(c); e == nil {
			t.Fatal("unbounded encode")
		}
	}
	if _, e := decodeChunks(nil); e == nil {
		t.Fatal("empty xorb")
	}
	tiny := []byte{0, 1, 0, 0, 0, 1, 0, 0, 'x'}
	if _, e := decodeChunks(bytes.Repeat(tiny, MaxChunks+1)); e == nil {
		t.Fatal("too many chunks")
	}
}

func fullShard(t *testing.T) []byte {
	t.Helper()
	data := fixture(t, "native.shard")
	end, err := fileSectionEnd(data[48:])
	if err != nil {
		t.Fatal(err)
	}
	footer := make([]byte, 200)
	binary.LittleEndian.PutUint64(data[40:], 200)
	binary.LittleEndian.PutUint64(footer, 1)
	binary.LittleEndian.PutUint64(footer[8:], 48)
	binary.LittleEndian.PutUint64(footer[16:], uint64(48+end))
	for _, offset := range []int{24, 40, 56, 192} {
		binary.LittleEndian.PutUint64(footer[offset:], uint64(len(data)))
	}
	return append(data, footer...)
}

func TestUnitShardBoundsAndIntegrity(t *testing.T) {
	full := fullShard(t)
	if _, err := ParseShard(full); err != nil {
		t.Fatal(err)
	}
	native := fixture(t, "native.shard")
	for n := range len(native) {
		if _, err := ParseShard(native[:n]); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted truncated shard at %d: %v", n, err)
		}
	}
	for _, pos := range []int{0, 32, 40, 48 + 32, 48 + 36, 96 + 32, 96 + 36, 96 + 40, 96 + 44} {
		bad := bytes.Clone(native)
		bad[pos] ^= 255
		if pos == 132 {
			clear(bad[pos : pos+4])
		}
		if pos == 136 || pos == 140 {
			binary.LittleEndian.PutUint32(bad[pos:], ^uint32(0))
		}
		if _, err := ParseShard(bad); err == nil {
			t.Fatalf("accepted changed shard field %d", pos)
		}
	}
	for _, offset := range []int{0, 8, 16, 24, 32, 40, 48, 56, 64, 192} {
		bad := bytes.Clone(full)
		at := len(bad) - 200 + offset
		for i := range 8 {
			bad[at+i] = 255
		}
		if _, err := ParseShard(bad); err == nil {
			t.Fatalf("accepted invalid footer field %d", offset)
		}
	}
	end, err := fileSectionEnd(native[48:])
	if err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int{32, 36, 40, 48 + 32, 48 + 36, 48 + 40, 48} {
		bad := bytes.Clone(native)
		bad[48+end+offset] ^= 255
		if _, err := ParseShard(bad); err == nil {
			t.Fatalf("accepted invalid shard xorb field %d", offset)
		}
	}
	for _, bad := range [][]byte{append(bytes.Clone(native), 0), make([]byte, MaxShard+1), append(bytes.Clone(native[:48]), bytes.Repeat([]byte{255}, 96)...)} {
		if _, err := ParseShard(bad); err == nil {
			t.Fatal("invalid shard accepted")
		}
	}
}

func FuzzXetWireParsing(f *testing.F) {
	f.Add(fixture(f, "native.xorb"))
	f.Add(fixture(f, "native.shard"))
	f.Add(fixture(f, "large.shard"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		_, _ = ParseXorb(data, nativeRoot)
		_, _ = ParseShard(data)
	})
}

func TestUnitByteGroupedLZ4AndDuplicateShardRecords(t *testing.T) {
	source := []byte("byte groups need not divide evenly")
	var grouped []byte
	for group := range 4 {
		for at := group; at < len(source); at += 4 {
			grouped = append(grouped, source[at])
		}
	}
	var packed bytes.Buffer
	writer := lz4.NewWriter(&packed)
	_, _ = writer.Write(grouped)
	_ = writer.Close()
	n := packed.Len()
	raw := []byte{0, byte(n), byte(n >> 8), 0, 2, byte(len(source)), 0, 0}
	raw = append(raw, packed.Bytes()...)
	parsed, err := ParseXorb(raw, DataHash(source).String())
	if err != nil || !bytes.Equal(parsed.Chunks[0].Data, source) {
		t.Fatalf("grouped LZ4: %v", err)
	}
	native := fixture(t, "native.shard")
	fileEnd, err := fileSectionEnd(native[48:])
	if err != nil {
		t.Fatal(err)
	}
	first := native[48 : 48+192]
	duplicated := append(bytes.Clone(first), first...)
	duplicated = append(duplicated, bytes.Repeat([]byte{255}, 48)...)
	if _, err := parseFiles(duplicated); err == nil {
		t.Fatal("duplicate file records accepted")
	}
	xorb := native[48+fileEnd : len(native)-48]
	duplicated = append(bytes.Clone(xorb), xorb...)
	duplicated = append(duplicated, bytes.Repeat([]byte{255}, 48)...)
	if _, err := parseShardXorbs(duplicated); err == nil {
		t.Fatal("duplicate xorb records accepted")
	}
	if _, err := parseFiles(append(bytes.Repeat([]byte{255}, 48), 0)); err == nil {
		t.Fatal("trailing file bytes accepted")
	}
	if _, err := parseFiles(make([]byte, 1)); err == nil {
		t.Fatal("missing file marker accepted")
	}
	full := fullShard(t)
	full[48+32] = 1
	if _, err := ParseShard(full); err == nil {
		t.Fatal("invalid indexed file section accepted")
	}
	full = fullShard(t)
	full[48+fileEnd+32] = 1
	if _, err := ParseShard(full); err == nil {
		t.Fatal("invalid indexed xorb section accepted")
	}
}
