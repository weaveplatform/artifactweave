package xzfile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"hash/crc64"
	"io"
	"math/rand/v2"
	"runtime"
	"testing"

	"github.com/ulikunitz/xz"
	"github.com/ulikunitz/xz/lzma"
)

// blockParts are a block's header, LZMA2 data, padding and check, and
// parts a whole stream, so a test can break any one part and keep the
// others' checksums right.
type (
	blockParts struct{ header, data, pad, check []byte }
	parts      struct {
		header []byte
		blocks []blockParts
		index  []byte
		footer []byte
	}
)

func (p parts) bytes() []byte {
	out := append([]byte{}, p.header...)
	for _, b := range p.blocks {
		out = append(append(append(append(out, b.header...), b.data...), b.pad...), b.check...)
	}
	return append(append(out, p.index...), p.footer...)
}

func sealed(b []byte) []byte {
	return binary.LittleEndian.AppendUint32(b, crc32.ChecksumIEEE(b))
}

func streamHeader(check byte) []byte {
	header := append(append([]byte{}, headerMagic...), 0, check)
	return binary.LittleEndian.AppendUint32(header, crc32.ChecksumIEEE(header[6:]))
}

// blockHeader encodes a block header declaring the sizes that are not
// negative, with the filter bytes given and null padding.
func blockHeader(compressed, uncompressed int64, filter ...byte) []byte {
	b := []byte{0, 0}
	if compressed >= 0 {
		b[1] |= 0x40
		b = binary.AppendUvarint(b, uint64(compressed))
	}
	if uncompressed >= 0 {
		b[1] |= 0x80
		b = binary.AppendUvarint(b, uint64(uncompressed))
	}
	b = append(b, filter...)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	b[0] = byte(len(b) / 4)
	return sealed(b)
}

func checkOf(check byte, data []byte) []byte {
	switch check {
	case checkCRC32:
		return binary.LittleEndian.AppendUint32(nil, crc32.ChecksumIEEE(data))
	case checkCRC64:
		return binary.LittleEndian.AppendUint64(nil, crc64.Checksum(data, crc64Table))
	case checkSHA256:
		sum := sha256.Sum256(data)
		return sum[:]
	}
	return nil
}

func indexBytes(records []record) []byte {
	b := binary.AppendUvarint([]byte{0}, uint64(len(records)))
	for _, r := range records {
		b = binary.AppendUvarint(
			binary.AppendUvarint(b, uint64(r.unpadded)),
			uint64(r.uncompressed),
		)
	}
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return sealed(b)
}

func footerBytes(indexSize int, check byte) []byte {
	f := binary.LittleEndian.AppendUint32(nil, uint32(indexSize/4-1))
	f = append(f, 0, check, 'Y', 'Z')
	return append(binary.LittleEndian.AppendUint32(nil, crc32.ChecksumIEEE(f[:6])), f...)
}

// packLZMA2 compresses data as one LZMA2 chunk sequence, with dict as the
// encoder's dictionary.
func packLZMA2(t *testing.T, dict int, data []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w, err := lzma.Writer2Config{DictCap: dict}.NewWriter2(&b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// stream lays out one stream with check as its check type and a block for
// each element of data, whose header names dictionary property prop and,
// when sizes is set, declares its sizes; compressed holds each block's
// LZMA2 data.
func stream(check byte, sizes bool, prop byte, data, compressed [][]byte) parts {
	p := parts{header: streamHeader(check)}
	var records []record
	for i, c := range compressed {
		packed, unpacked := int64(-1), int64(-1)
		if sizes {
			packed, unpacked = int64(len(c)), int64(len(data[i]))
		}
		part := blockParts{
			header: blockHeader(packed, unpacked, filterLZMA2, 1, prop),
			data:   c,
			pad:    make([]byte, -len(c)&3),
			check:  checkOf(check, data[i]),
		}
		p.blocks = append(p.blocks, part)
		records = append(records, record{
			unpadded:     int64(len(part.header) + len(part.data) + len(part.check)),
			uncompressed: int64(len(data[i])),
		})
	}
	p.index = indexBytes(records)
	p.footer = footerBytes(len(p.index), check)
	return p
}

// build is stream with each block compressed under a 1 MiB dictionary.
func build(t *testing.T, check byte, sizes bool, data ...[]byte) parts {
	t.Helper()
	compressed := make([][]byte, len(data))
	for i, d := range data {
		compressed[i] = packLZMA2(t, 1<<20, d)
	}
	return stream(check, sizes, lzma.EncodeDictCap(1<<20), data, compressed)
}

func read(limit int64, data []byte) ([]byte, error) {
	r, err := NewReader(context.Background(), bytes.NewReader(data), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	return io.ReadAll(r)
}

func readXZ(data []byte) ([]byte, error) { return read(0, data) }

// readUlikunitz reads data with the reader this package replaces, each
// block with the dictionary its header names, as this one reads it: that
// reader's default raised it to 8 MiB.
func readUlikunitz(data []byte) ([]byte, error) {
	r, err := xz.ReaderConfig{DictCap: lzma.MinDictCap}.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

// fixture is a stream of two blocks, the second of several LZMA2 chunks,
// with a CRC64 check: what xz writes, small enough to cut and break at
// every byte.
func fixture(t *testing.T) ([]byte, []byte) {
	t.Helper()
	first, second := []byte("shared xz fixture\n"), bytes.Repeat([]byte("block "), 400)
	return build(
			t,
			checkCRC64,
			false,
			first,
			second,
		).bytes(),
		append(
			append([]byte{}, first...),
			second...)
}

// Every xz file the container allows decodes whole: each check type,
// declared sizes, several blocks, concatenated streams with stream padding
// between and after them, and what ulikunitz/xz's own writer produces.
func TestUnitDecodesWholeFiles(t *testing.T) {
	first, second := []byte("native fixture\n"), bytes.Repeat([]byte("block "), 400)
	want := append(append([]byte{}, first...), second...)
	for _, check := range []byte{checkNone, checkCRC32, checkCRC64, checkSHA256} {
		for _, sizes := range []bool{false, true} {
			got, err := readXZ(build(t, check, sizes, first, second).bytes())
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("check %#x sizes %v: %q %v", check, sizes, got, err)
			}
		}
	}
	empty, err := readXZ(build(t, checkCRC64, false).bytes())
	if err != nil || len(empty) != 0 {
		t.Fatalf("a stream without blocks: %q %v", empty, err)
	}
	concatenated := append(build(t, checkCRC32, false, first).bytes(), make([]byte, 8)...)
	concatenated = append(concatenated, build(t, checkSHA256, true, second).bytes()...)
	concatenated = append(concatenated, make([]byte, 4)...)
	if got, err := readXZ(concatenated); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("concatenated streams: %v", err)
	}
	for _, config := range []xz.WriterConfig{{}, {CheckSum: xz.CRC32}, {NoCheckSum: true}, {BlockSize: 1024}} {
		var b bytes.Buffer
		w, err := config.NewWriter(&b)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write(want); err != nil {
			t.Fatal(err)
		}
		if err = w.Close(); err != nil {
			t.Fatal(err)
		}
		if got, err := NewReader(
			t.Context(),
			bytes.NewReader(b.Bytes()),
			int64(len(want)),
		); err != nil {
			t.Fatalf("ulikunitz/xz writer %+v: %v", config, err)
		} else if all, err := io.ReadAll(
			got,
		); err != nil ||
			!bytes.Equal(all, want) {
			t.Fatalf("ulikunitz/xz writer %+v: %v", config, err)
		}
	}
}

// tail is the length of the index and footer that end data, a stream that
// holds no stream padding: the footer's backward size plus the footer.
func tail(t *testing.T, data []byte) int {
	t.Helper()
	footer := data[len(data)-12:]
	if !bytes.Equal(footer[10:], footerMagic) {
		t.Fatal("no footer at the end")
	}
	return int(binary.LittleEndian.Uint32(footer[4:8])+1)*4 + 12
}

// An xz file that stops anywhere but between streams is refused, as
// liblzma refuses it. Where ulikunitz/xz's reader refused the same cut, the
// refusal is in its words; where it decoded the cut file whole, after a
// block or inside a block header, the refusal is io.ErrUnexpectedEOF, its
// word for an early end. Cutting the index and footer is one of those.
func TestUnitRefusesAnEarlyEnd(t *testing.T) {
	data, want := fixture(t)
	accepted := 0
	for cut := 1; cut < len(data); cut++ {
		short := data[:len(data)-cut]
		got, err := readXZ(short)
		if err == nil {
			t.Fatalf("cut by %d bytes decoded %d bytes", cut, len(got))
		}
		if _, before := readUlikunitz(short); before == nil {
			accepted++
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Errorf("cut by %d bytes: %v, want io.ErrUnexpectedEOF", cut, err)
			}
		} else if err.Error() != before.Error() {
			t.Errorf("cut by %d bytes: %q, ulikunitz/xz said %q", cut, err, before)
		}
	}
	if got, err := readXZ(data); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("whole fixture: %v", err)
	}
	cut := data[:len(data)-tail(t, data)]
	if got, err := readUlikunitz(cut); err != nil || !bytes.Equal(got, want) || accepted == 0 {
		t.Fatalf(
			"ulikunitz/xz no longer reads the fixture without its index and footer (%v); this test's premise changed",
			err,
		)
	}
	whole := build(t, checkCRC64, false, want)
	for name, data := range map[string][]byte{
		"a second stream's header alone": append(whole.bytes(), streamHeader(checkCRC64)...),
		"a block header cut short": append(
			whole.bytes()[:len(whole.bytes())-len(whole.index)-len(whole.footer)], 0x02, 0x00,
		),
		"stream padding of two bytes":          append(whole.bytes(), 0, 0),
		"a stream header cut short":            append(whole.bytes(), headerMagic...),
		"no input":                             nil,
		"three bytes of a first stream header": headerMagic[:3],
	} {
		if _, err := readXZ(data); err == nil {
			t.Errorf("%s decoded", name)
		}
	}
}

// Changing any one byte of the fixture is refused in ulikunitz/xz's words
// wherever that reader refused it, and decodes the same where it decoded
// it. Where that reader decoded it but this one sees an early end, the
// changed byte made a block header or the index indicator a longer block
// header that the end of the file cuts short, which that reader took for
// the end of the stream.
func TestUnitRefusesAsUlikunitzDoes(t *testing.T) {
	data, _ := fixture(t)
	early := 0
	for i := range data {
		broken := append([]byte{}, data...)
		broken[i] ^= 0x41
		got, err := readXZ(broken)
		before, beforeErr := readUlikunitz(broken)
		switch {
		case beforeErr == nil && err == nil:
			if !bytes.Equal(got, before) {
				t.Errorf("byte %d: decoded %q, ulikunitz/xz decoded %q", i, got, before)
			}
		case beforeErr == nil:
			early++
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Errorf("byte %d: ulikunitz/xz decoded it, this reader said %v", i, err)
			}
		case err == nil || err.Error() != beforeErr.Error():
			t.Errorf("byte %d: %v, ulikunitz/xz said %q", i, err, beforeErr)
		}
	}
	if early == 0 {
		t.Error("no changed byte cut a header short; the fixture no longer shows the early end")
	}
}

// Each part of the container that liblzma checks is checked: a broken
// header, block header, block, index or footer is refused, with the text
// ulikunitz/xz gives it.
func TestUnitRefusesBrokenParts(t *testing.T) {
	// Its LZMA2 data is 18 bytes long, so the block has two bytes of padding.
	data := bytes.Repeat([]byte("xz "), 85)
	// header swaps in another block header and rebuilds the index and
	// footer to match, so only the header can be what is refused.
	header := func(h []byte) func(*parts) {
		return func(p *parts) {
			p.blocks[0].header = h
			b := p.blocks[0]
			p.index = indexBytes([]record{{
				unpadded:     int64(len(b.header) + len(b.data) + len(b.check)),
				uncompressed: int64(len(data)),
			}})
			p.footer = footerBytes(len(p.index), checkCRC64)
		}
	}
	for _, c := range []struct {
		name    string
		breakIt func(*parts)
		// early marks a file this reader sees an early end in, where
		// ulikunitz/xz's reader decoded it, or, for a field that runs past
		// its block header, ended the stream there and read on as if a new
		// stream started, so its words depend on what follows.
		early bool
	}{
		{"stream magic", func(p *parts) { p.header[0] = 0 }, false},
		{"stream header CRC", func(p *parts) { p.header[8]++ }, false},
		{"stream flags", func(p *parts) { p.header = sealedFlags(0x01, checkCRC64) }, false},
		{"unknown check type", func(p *parts) { p.header = streamHeader(0x02) }, false},
		{"stream padding first", func(p *parts) { p.header = append(make([]byte, 4), p.header...) }, false},
		{"block header CRC", func(p *parts) { p.blocks[0].header[len(p.blocks[0].header)-1]++ }, false},
		{"reserved block flags", header(sealedBlock(0x04, filterLZMA2, 1, 16)), false},
		{"two filters", header(sealedBlock(0x01, filterLZMA2, 1, 16)), false},
		{"another filter", header(blockHeader(-1, -1, 0x03, 1, 16)), false},
		{"reserved filter", header(sealedBlock(0, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x40)), false},
		{"filter id overflow", header(sealedBlock(0, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x02)), false},
		{"filter property size", header(blockHeader(-1, -1, filterLZMA2, 2, 16)), false},
		{"dictionary size", header(blockHeader(-1, -1, filterLZMA2, 1, 41)), false},
		{"missing filter", header(sealed([]byte{1, 0, 0, 0})), false},
		{"filter cut short", header(sealed([]byte{1, 0, filterLZMA2, 1})), false},
		{"header padding", header(sealed([]byte{2, 0, filterLZMA2, 1, 16, 0, 1, 0})), false},
		{"oversized compressed size", header(
			sealedBlock(0x40, append(binary.AppendUvarint(nil, 1<<63), filterLZMA2, 1, 16)...),
		), false},
		{"size runs past the header", header(sealedBlock(0xc0, 0x80, 0x80)), true},
		{"declared compressed size", func(p *parts) { header(blockSizes(p, 1, 0))(p) }, false},
		{"declared uncompressed size", func(p *parts) { header(blockSizes(p, 0, 1))(p) }, false},
		{"short compressed size", func(p *parts) { header(blockSizes(p, -1, 0))(p) }, false},
		{"short uncompressed size", func(p *parts) { header(blockSizes(p, 0, -1))(p) }, false},
		{"LZMA2 data", func(p *parts) { p.blocks[0].data[0] = 0x03 }, false},
		{"block padding", func(p *parts) { p.blocks[0].pad[len(p.blocks[0].pad)-1] = 1 }, false},
		{"block check", func(p *parts) { p.blocks[0].check[0]++ }, false},
		{"index count", func(p *parts) {
			index := append([]byte{}, p.index[:len(p.index)-4]...)
			index[1] = 2
			p.index = sealed(index)
		}, false},
		{"index count overflow", func(p *parts) {
			p.index = sealed(binary.AppendUvarint([]byte{0}, 1<<63))
		}, false},
		{"index unpadded size", func(p *parts) {
			p.index = indexBytes([]record{{unpadded: 1, uncompressed: int64(len(data))}})
		}, false},
		{"index uncompressed size", func(p *parts) {
			p.index = indexBytes([]record{{unpadded: p.records()[0].unpadded, uncompressed: 1}})
		}, false},
		{"negative unpadded size", func(p *parts) {
			p.index = sealed(binary.AppendUvarint([]byte{0, 1}, 1<<63))
		}, false},
		{"negative uncompressed size", func(p *parts) {
			p.index = sealed(binary.AppendUvarint(binary.AppendUvarint([]byte{0, 1}, 1), 1<<63))
		}, false},
		{"index padding", func(p *parts) {
			p.index[len(p.index)-5] = 1
			p.index = sealed(p.index[:len(p.index)-4])
		}, false},
		{"index CRC", func(p *parts) { p.index[len(p.index)-1]++ }, false},
		{"footer magic", func(p *parts) { p.footer[11] = 'X' }, false},
		{"footer CRC", func(p *parts) { p.footer[0]++ }, false},
		{"footer backward size", func(p *parts) { p.footer = footerBytes(len(p.index)+4, checkCRC64) }, false},
		{"footer check type", func(p *parts) { p.footer = footerBytes(len(p.index), checkCRC32) }, false},
		{"footer unknown check type", func(p *parts) { p.footer = footerBytes(len(p.index), 0x02) }, false},
		{"footer flags", func(p *parts) {
			p.footer[8] = 1
			binary.LittleEndian.PutUint32(p.footer, crc32.ChecksumIEEE(p.footer[4:10]))
		}, false},
		{"index cut short", func(p *parts) { p.index, p.footer = []byte{0}, nil }, false},
		{"index record cut short", func(p *parts) {
			p.index, p.footer = binary.AppendUvarint([]byte{0, 1}, 1<<20), nil
		}, false},
		{"index padding cut short", func(p *parts) { p.index, p.footer = p.index[:len(p.index)-5], nil }, false},
		{"index CRC cut short", func(p *parts) { p.index, p.footer = p.index[:len(p.index)-2], nil }, false},
		{"footer cut short", func(p *parts) { p.footer = p.footer[:11] }, false},
		{"block check cut short", func(p *parts) {
			p.blocks[0].check, p.index, p.footer = p.blocks[0].check[:3], nil, nil
		}, false},
		{"garbage after the stream", func(p *parts) {
			p.footer = append(p.footer, 'x', 'x', 'x', 'x', 0, 0, 0, 0, 0, 0, 0, 0)
		}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := build(t, checkCRC64, false, data)
			c.breakIt(&p)
			got, err := readXZ(p.bytes())
			if err == nil {
				t.Fatalf("decoded %d bytes", len(got))
			}
			_, before := readUlikunitz(p.bytes())
			switch {
			case c.early && !errors.Is(err, io.ErrUnexpectedEOF):
				t.Fatalf("%v, ulikunitz/xz said %v; want io.ErrUnexpectedEOF", err, before)
			case !c.early && (before == nil || err.Error() != before.Error()):
				t.Fatalf("%q, ulikunitz/xz said %v", err, before)
			}
		})
	}
}

func (p parts) records() []record {
	var out []record
	for _, b := range p.blocks {
		out = append(out, record{unpadded: int64(len(b.header) + len(b.data) + len(b.check))})
	}
	return out
}

func sealedFlags(first, check byte) []byte {
	header := append(append([]byte{}, headerMagic...), first, check)
	return binary.LittleEndian.AppendUint32(header, crc32.ChecksumIEEE(header[6:]))
}

// sealedBlock is a block header with the given flags and filter bytes.
func sealedBlock(flags byte, filter ...byte) []byte {
	b := append([]byte{0, flags}, filter...)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	b[0] = byte(len(b) / 4)
	return sealed(b)
}

// blockSizes declares p's one block's sizes, each off by the delta given.
func blockSizes(p *parts, compressed, uncompressed int64) []byte {
	var plain bytes.Buffer
	r, _ := lzma.NewReader2(bytes.NewReader(p.blocks[0].data))
	n, _ := io.Copy(&plain, r)
	return blockHeader(
		int64(len(p.blocks[0].data))+compressed,
		n+uncompressed,
		filterLZMA2,
		1,
		lzma.EncodeDictCap(1<<20),
	)
}

// A block is decoded with the dictionary its header names, lowered to the
// caller's limit and raised to the 4 KiB the LZMA2 decoder takes.
func TestUnitDictionarySize(t *testing.T) {
	for _, c := range []struct {
		header, limit int64
		want          int
	}{
		{1 << 20, 0, 1 << 20},
		{MaxDict, 0, MaxDict},
		{MaxDict, 36 << 20, 36 << 20},
		{4 << 20, 1 << 20, 1 << 20},
		{1 << 20, 10, lzma.MinDictCap},
	} {
		if got := dictSize(c.header, c.limit); got != c.want {
			t.Errorf("header %d, limit %d: %d, want %d", c.header, c.limit, got, c.want)
		}
	}
}

// A dictionary property names what liblzma and ulikunitz/xz read it as, up
// to 37, 1.5 GiB; every byte above it is refused, as XZ for Java refuses
// it, and as ulikunitz/xz refused one above 40.
func TestUnitDictionaryProperty(t *testing.T) {
	for prop := range 256 {
		got, err := lzma2Filter(bytes.NewReader([]byte{filterLZMA2, 1, byte(prop)}), 0)
		switch want, wantErr := lzma.DecodeDictCap(byte(prop)); {
		case prop > maxDictProperty:
			if !errors.Is(err, errDictProperty) {
				t.Errorf("property %d: %d %v, want %v", prop, got, err, errDictProperty)
			}
		case err != nil || wantErr != nil || got != want:
			t.Errorf("property %d: %d %v, want %d", prop, got, err, want)
		}
	}
	if got, _ := lzma2Filter(
		bytes.NewReader([]byte{filterLZMA2, 1, maxDictProperty}),
		0,
	); got != MaxDict {
		t.Errorf("property %d names %d, want MaxDict", maxDictProperty, got)
	}
}

// A block header that names a dictionary above 1.5 GiB is refused before
// anything is allocated, as XZ for Java refuses it: ulikunitz/xz allocated
// all of a 4 GiB one before it read a byte (#1332). One that names 1.5 GiB
// is read with a dictionary no larger than the caller's limit.
func TestUnitAHugeDictionaryIsNotAllocated(t *testing.T) {
	data := []byte("abc")
	chunk := append(append([]byte{0x01, 0x00, byte(len(data) - 1)}, data...), 0x00)
	for _, c := range []struct {
		prop  byte
		limit int64
		ok    bool
		most  uint64
	}{
		{40, 0, false, 1 << 20},
		{39, 0, false, 1 << 20},
		{38, 0, false, 1 << 20},
		{maxDictProperty, 1 << 20, true, 2 << 20},
	} {
		file := stream(checkNone, false, c.prop, [][]byte{data}, [][]byte{chunk}).bytes()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		got, err := read(c.limit, file)
		runtime.ReadMemStats(&after)
		if c.ok && (err != nil || !bytes.Equal(got, data)) ||
			!c.ok && !errors.Is(err, errDictProperty) {
			t.Fatalf("property %d: %q %v", c.prop, got, err)
		}
		if n := after.TotalAlloc - before.TotalAlloc; n > c.most {
			t.Errorf("property %d allocated %d MiB, want at most %d MiB", c.prop, n>>20, c.most>>20)
		}
	}
}

// A block whose data refers further back than the dictionary it is decoded
// with is refused, and is read whole with a dictionary that reaches. A
// header that names less than its data refers back is refused, as XZ for
// Java refuses a match past its dictionary (LZDecoder.repeat:
// https://github.com/tukaani-project/xz-java/blob/f14a6039/src/org/tukaani/xz/lz/LZDecoder.java#L91)
// and liblzma refuses it: ulikunitz/xz read it within its 8 MiB default.
func TestUnitDataBeyondTheDictionary(t *testing.T) {
	random := make([]byte, 96<<10)
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range random {
		random[i] = byte(rng.Uint32())
	}
	// The repeat is all matches 96 KiB back, past a 64 KiB dictionary.
	data := append(append([]byte{}, random...), random...)
	compressed := packLZMA2(t, 1<<20, data)
	names := func(prop byte) []byte {
		return stream(checkCRC32, false, prop, [][]byte{data}, [][]byte{compressed}).bytes()
	}
	honest, understated := names(lzma.EncodeDictCap(1<<20)), names(lzma.EncodeDictCap(64<<10))
	for _, c := range []struct {
		name  string
		limit int64
		file  []byte
		ok    bool
	}{
		{"its own dictionary", 0, honest, true},
		{"a limit that reaches", int64(len(data)), honest, true},
		{"a limit short of the match", 64 << 10, honest, false},
		{"a header that understates it", 0, understated, false},
	} {
		got, err := read(c.limit, c.file)
		if ok := err == nil && bytes.Equal(got, data); ok != c.ok {
			t.Errorf("%s: read whole %v (%v), want %v", c.name, ok, err, c.ok)
		}
	}
}
