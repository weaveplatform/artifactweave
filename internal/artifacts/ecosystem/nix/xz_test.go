package nix

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"hash/crc64"
	"io"
	"testing"

	"github.com/ulikunitz/xz"
	"github.com/ulikunitz/xz/lzma"
)

// xzBlockParts are a block's header, LZMA2 data, padding and check, and
// xzParts a whole stream, so a test can break any one part and keep the
// others' checksums right.
type (
	xzBlockParts struct{ header, data, pad, check []byte }
	xzParts      struct {
		header []byte
		blocks []xzBlockParts
		index  []byte
		footer []byte
	}
)

func (p xzParts) bytes() []byte {
	out := append([]byte{}, p.header...)
	for _, b := range p.blocks {
		out = append(append(append(append(out, b.header...), b.data...), b.pad...), b.check...)
	}
	return append(append(out, p.index...), p.footer...)
}

func sealed(b []byte) []byte {
	return binary.LittleEndian.AppendUint32(b, crc32.ChecksumIEEE(b))
}

func xzStreamHeader(check byte) []byte {
	header := append(append([]byte{}, xzMagic...), 0, check)
	return binary.LittleEndian.AppendUint32(header, crc32.ChecksumIEEE(header[6:]))
}

// xzBlockHeader encodes a block header declaring the sizes that are not
// negative, with the filter bytes given and null padding.
func xzBlockHeader(compressed, uncompressed int64, filter ...byte) []byte {
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

func xzCheck(check byte, data []byte) []byte {
	switch check {
	case xzCheckCRC32:
		return binary.LittleEndian.AppendUint32(nil, crc32.ChecksumIEEE(data))
	case xzCheckCRC64:
		return binary.LittleEndian.AppendUint64(nil, crc64.Checksum(data, xzCRC64))
	case xzCheckSHA256:
		sum := sha256.Sum256(data)
		return sum[:]
	}
	return nil
}

func xzIndexBytes(records []xzRecord) []byte {
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

func xzFooterBytes(indexSize int, check byte) []byte {
	f := binary.LittleEndian.AppendUint32(nil, uint32(indexSize/4-1))
	f = append(f, 0, check, 'Y', 'Z')
	return append(binary.LittleEndian.AppendUint32(nil, crc32.ChecksumIEEE(f[:6])), f...)
}

// buildXZ lays out one stream holding a block per entry of blocks, with
// check as its check type, declaring each block's sizes when sizes is set.
func buildXZ(t *testing.T, check byte, sizes bool, blocks ...[]byte) xzParts {
	t.Helper()
	p := xzParts{header: xzStreamHeader(check)}
	var records []xzRecord
	for _, data := range blocks {
		var b bytes.Buffer
		w, err := lzma.Writer2Config{DictCap: 1 << 20}.NewWriter2(&b)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write(data); err != nil {
			t.Fatal(err)
		}
		if err = w.Close(); err != nil {
			t.Fatal(err)
		}
		compressed, uncompressed := int64(-1), int64(-1)
		if sizes {
			compressed, uncompressed = int64(b.Len()), int64(len(data))
		}
		part := xzBlockParts{
			header: xzBlockHeader(compressed, uncompressed, xzLZMA2, 1, lzma.EncodeDictCap(1<<20)),
			data:   b.Bytes(),
			pad:    make([]byte, -b.Len()&3),
			check:  xzCheck(check, data),
		}
		p.blocks = append(p.blocks, part)
		records = append(records, xzRecord{
			unpadded:     int64(len(part.header) + len(part.data) + len(part.check)),
			uncompressed: int64(len(data)),
		})
	}
	p.index = xzIndexBytes(records)
	p.footer = xzFooterBytes(len(p.index), check)
	return p
}

func readXZ(data []byte) ([]byte, error) {
	r, err := newXZReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

// Every xz file the container allows decodes whole: each check type,
// declared sizes, several blocks, concatenated streams with stream padding
// between and after them, and what ulikunitz/xz's own writer produces.
func TestUnitXZDecodesWholeFiles(t *testing.T) {
	first, second := []byte("native nix fixture\n"), bytes.Repeat([]byte("block "), 400)
	want := append(append([]byte{}, first...), second...)
	for _, check := range []byte{xzCheckNone, xzCheckCRC32, xzCheckCRC64, xzCheckSHA256} {
		for _, sizes := range []bool{false, true} {
			got, err := readXZ(buildXZ(t, check, sizes, first, second).bytes())
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("check %#x sizes %v: %q %v", check, sizes, got, err)
			}
		}
	}
	empty, err := readXZ(buildXZ(t, xzCheckCRC64, false).bytes())
	if err != nil || len(empty) != 0 {
		t.Fatalf("a stream without blocks: %q %v", empty, err)
	}
	concatenated := append(buildXZ(t, xzCheckCRC32, false, first).bytes(), make([]byte, 8)...)
	concatenated = append(concatenated, buildXZ(t, xzCheckSHA256, true, second).bytes()...)
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
		if got, err := readXZ(b.Bytes()); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("ulikunitz/xz writer %+v: %v", config, err)
		}
	}
}

// An xz file that stops anywhere but between streams is refused, as
// liblzma refuses it. Cutting the native fixture's last 24 bytes, its
// index and footer, leaves a stream ulikunitz/xz's reader decoded whole.
func TestUnitXZRefusesAnEarlyEnd(t *testing.T) {
	_, archive, nar := fixture(t)
	for cut := 1; cut < len(archive); cut++ {
		got, err := readXZ(archive[:len(archive)-cut])
		if err == nil {
			t.Fatalf("cut by %d bytes decoded %d bytes", cut, len(got))
		}
	}
	if got, err := readXZ(archive); err != nil || !bytes.Equal(got, nar) {
		t.Fatalf("whole fixture: %v", err)
	}
	r, err := xz.NewReader(bytes.NewReader(archive[:len(archive)-24]))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := io.ReadAll(r); err != nil || !bytes.Equal(got, nar) {
		t.Fatalf(
			"ulikunitz/xz no longer reads the cut fixture whole (%v); this test's premise changed",
			err,
		)
	}
	whole := buildXZ(t, xzCheckCRC64, false, nar)
	header := append(whole.bytes(), xzStreamHeader(xzCheckCRC64)...)
	for name, data := range map[string][]byte{
		"a second stream's header alone":       header,
		"a block header cut short":             append(whole.bytes()[:len(whole.bytes())-len(whole.index)-len(whole.footer)], 0x02, 0x00),
		"stream padding of two bytes":          append(whole.bytes(), 0, 0),
		"a stream header cut short":            append(whole.bytes(), xzMagic...),
		"no input":                             nil,
		"three bytes of a first stream header": xzMagic[:3],
	} {
		if _, err := readXZ(data); err == nil {
			t.Errorf("%s decoded", name)
		}
	}
}

// Each part of the container that liblzma checks is checked: a broken
// header, block header, block, index or footer is refused.
func TestUnitXZRefusesBrokenParts(t *testing.T) {
	// Its LZMA2 data is 18 bytes long, so the block has two bytes of padding.
	data := bytes.Repeat([]byte("nix "), 64)
	// lzma2 swaps in another block header and rebuilds the index and footer
	// to match, so only the header can be what is refused.
	lzma2 := func(header []byte) func(*xzParts) {
		return func(p *xzParts) {
			p.blocks[0].header = header
			b := p.blocks[0]
			p.index = xzIndexBytes([]xzRecord{{
				unpadded:     int64(len(b.header) + len(b.data) + len(b.check)),
				uncompressed: int64(len(data)),
			}})
			p.footer = xzFooterBytes(len(p.index), xzCheckCRC64)
		}
	}
	for name, breakIt := range map[string]func(*xzParts){
		"stream magic":         func(p *xzParts) { p.header[0] = 0 },
		"stream header CRC":    func(p *xzParts) { p.header[8]++ },
		"stream flags":         func(p *xzParts) { p.header = sealedFlags(0x01, xzCheckCRC64) },
		"unknown check type":   func(p *xzParts) { p.header = xzStreamHeader(0x02) },
		"block header CRC":     func(p *xzParts) { p.blocks[0].header[len(p.blocks[0].header)-1]++ },
		"reserved block flags": lzma2(sealedBlock(0x04, xzLZMA2, 1, 16)),
		"two filters":          lzma2(sealedBlock(0x01, xzLZMA2, 1, 16)),
		"another filter":       lzma2(xzBlockHeader(-1, -1, 0x03, 1, 16)),
		"filter property size": lzma2(xzBlockHeader(-1, -1, xzLZMA2, 2, 16)),
		"dictionary size":      lzma2(xzBlockHeader(-1, -1, xzLZMA2, 1, 41)),
		"missing filter":       lzma2(sealed([]byte{1, 0, 0, 0})),
		"filter cut short":     lzma2(sealed([]byte{1, 0, xzLZMA2, 1})),
		"header padding":       lzma2(sealed([]byte{2, 0, xzLZMA2, 1, 16, 0, 1, 0})),
		"oversized compressed size": lzma2(
			sealedBlock(0x40, append(binary.AppendUvarint(nil, 1<<63), xzLZMA2, 1, 16)...),
		),
		"declared compressed size":   func(p *xzParts) { lzma2(blockSizes(p, 1, 0))(p) },
		"declared uncompressed size": func(p *xzParts) { lzma2(blockSizes(p, 0, 1))(p) },
		"short compressed size":      func(p *xzParts) { lzma2(blockSizes(p, -1, 0))(p) },
		"short uncompressed size":    func(p *xzParts) { lzma2(blockSizes(p, 0, -1))(p) },
		"LZMA2 data":                 func(p *xzParts) { p.blocks[0].data[0] = 0x03 },
		"block padding":              func(p *xzParts) { p.blocks[0].pad[len(p.blocks[0].pad)-1] = 1 },
		"block check":                func(p *xzParts) { p.blocks[0].check[0]++ },
		"index count": func(p *xzParts) {
			index := append([]byte{}, p.index[:len(p.index)-4]...)
			index[1] = 2
			p.index = sealed(index)
		},
		"index unpadded size": func(p *xzParts) {
			p.index = xzIndexBytes([]xzRecord{{unpadded: 1, uncompressed: int64(len(data))}})
		},
		"index padding": func(p *xzParts) {
			p.index[len(p.index)-5] = 1
			p.index = sealed(p.index[:len(p.index)-4])
		},
		"index CRC":            func(p *xzParts) { p.index[len(p.index)-1]++ },
		"footer magic":         func(p *xzParts) { p.footer[11] = 'X' },
		"footer CRC":           func(p *xzParts) { p.footer[0]++ },
		"footer backward size": func(p *xzParts) { p.footer = xzFooterBytes(len(p.index)+4, xzCheckCRC64) },
		"footer check type":    func(p *xzParts) { p.footer = xzFooterBytes(len(p.index), xzCheckCRC32) },
		"footer flags": func(p *xzParts) {
			p.footer[8] = 1
			binary.LittleEndian.PutUint32(p.footer, crc32.ChecksumIEEE(p.footer[4:10]))
		},
		"index cut short": func(p *xzParts) { p.index, p.footer = []byte{0}, nil },
		"index record cut short": func(p *xzParts) {
			p.index, p.footer = binary.AppendUvarint([]byte{0, 1}, 1<<20), nil
		},
		"index padding cut short": func(p *xzParts) { p.index, p.footer = p.index[:len(p.index)-5], nil },
		"footer cut short":        func(p *xzParts) { p.footer = p.footer[:11] },
		"block check cut short":   func(p *xzParts) { p.blocks[0].check, p.index, p.footer = p.blocks[0].check[:3], nil, nil },
		"garbage after the stream": func(p *xzParts) {
			p.footer = append(p.footer, 'x', 'x', 'x', 'x', 0, 0, 0, 0, 0, 0, 0, 0)
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := buildXZ(t, xzCheckCRC64, false, data)
			breakIt(&p)
			if got, err := readXZ(p.bytes()); err == nil {
				t.Fatalf("decoded %d bytes", len(got))
			}
		})
	}
}

func sealedFlags(first, check byte) []byte {
	header := append(append([]byte{}, xzMagic...), first, check)
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
func blockSizes(p *xzParts, compressed, uncompressed int64) []byte {
	var nar bytes.Buffer
	r, _ := lzma.NewReader2(bytes.NewReader(p.blocks[0].data))
	n, _ := io.Copy(&nar, r)
	return xzBlockHeader(
		int64(
			len(p.blocks[0].data),
		)+compressed,
		n+uncompressed,
		xzLZMA2,
		1,
		lzma.EncodeDictCap(1<<20),
	)
}

// A NAR whose xz stream lost its index and footer is no valid archive, and
// a channel tarball whose stream lost them is no valid channel.
func TestUnitNARsNeedTheirWholeXZStream(t *testing.T) {
	info, archive, _ := fixture(t)
	cut := archive[:len(archive)-24]
	if _, err := ReadArchive(
		bytes.NewReader(cut),
		int64(len(cut)),
		"xz",
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Fatalf("cut NAR: %v", err)
	}
	if a, err := ReadArchive(
		bytes.NewReader(archive),
		int64(len(archive)),
		"xz",
	); err != nil ||
		!a.Matches(info) {
		t.Fatalf("whole NAR: %v", err)
	}
}

func TestUnitChannelsNeedTheirWholeXZStream(t *testing.T) {
	tarball := compressXZ(t, channelTar(t, 0))
	if err := ReadChannel(bytes.NewReader(tarball), int64(len(tarball))); err != nil {
		t.Fatal(err)
	}
	cutTarball := tarball[:len(tarball)-xzTail(t, tarball)]
	if err := ReadChannel(
		bytes.NewReader(cutTarball),
		int64(len(cutTarball)),
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Fatalf("cut channel tarball: %v", err)
	}
}

// xzTail is the length of the index and footer that end data, a stream
// that holds no stream padding: the footer's backward size plus the
// footer.
func xzTail(t *testing.T, data []byte) int {
	t.Helper()
	footer := data[len(data)-12:]
	if !bytes.Equal(footer[10:], xzFooter) {
		t.Fatal("no footer at the end")
	}
	return int(binary.LittleEndian.Uint32(footer[4:8])+1)*4 + 12
}
