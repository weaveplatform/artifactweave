package nix

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash"
	"hash/crc32"
	"hash/crc64"
	"io"

	"github.com/ulikunitz/xz/lzma"
)

// The xz container's constants, from the .xz file format 1.2.1: the
// stream header's magic, the footer's, the check types and the one filter
// github.com/ulikunitz/xz decodes, LZMA2.
const (
	xzCheckNone   = 0x00
	xzCheckCRC32  = 0x01
	xzCheckCRC64  = 0x04
	xzCheckSHA256 = 0x0a
	xzLZMA2       = 0x21
	// xzDictCap is the smallest dictionary a block is decoded with, as
	// ReaderConfig{DictCap: 64 << 20} gave it before: ulikunitz/xz raises
	// it to a block's own dictionary size, never lowers it.
	xzDictCap = 64 << 20
)

var (
	xzMagic    = []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}
	xzFooter   = []byte{'Y', 'Z'}
	xzCRC64    = crc64.MakeTable(crc64.ECMA)
	errXZ      = errors.New("nix: invalid xz file")
	errXZShort = errors.New("nix: xz file ends early")
)

// xzReader decodes an xz file as liblzma's stream decoder does with
// LZMA_CONCATENATED, which xz -t and libarchive, Nix's decompressor, use:
// one or more streams, each followed by optional stream padding, where
// every stream ends in an index that lists exactly the blocks read and a
// footer that matches the index and the stream's header.
//
// github.com/ulikunitz/xz's own Reader takes an input that stops right
// after a block, where a block header or the index must start, for the end
// of the stream, so a NAR or channel tarball whose index and footer were
// cut off was admitted although Nix cannot read it (#1297). SingleStream
// does not change that: the reader still sees the end of the input first.
// This reader keeps that package's LZMA2 decoder and the checks its Reader
// makes, and parses the container itself, so the end of the input is an
// error anywhere but between streams.
type xzReader struct {
	in *bufio.Reader
	// flags is the current stream's check type, and records the index
	// entries of the blocks read in it.
	flags   byte
	records []xzRecord
	inside  bool
	block   *xzBlock
	err     error
}

type xzRecord struct{ unpadded, uncompressed int64 }

// newXZReader reads the first stream header of in, as xz.NewReader does,
// and returns the reader of its content.
func newXZReader(in io.Reader) (io.Reader, error) {
	x := &xzReader{in: bufio.NewReaderSize(in, 32<<10)}
	if err := x.stream(true); err != nil {
		return nil, err
	}
	return x, nil
}

func (x *xzReader) Read(p []byte) (int, error) {
	for x.err == nil {
		if x.block == nil {
			x.err = x.next()
			continue
		}
		n, err := x.block.Read(p)
		if errors.Is(err, io.EOF) {
			err = x.endBlock()
		}
		if err != nil {
			x.err = err
			return n, err
		}
		if n > 0 || len(p) == 0 {
			return n, nil
		}
	}
	return 0, x.err
}

// next reads what follows a stream or a block: the next stream, or io.EOF
// at the end of the input; or the next block, or the index and footer.
func (x *xzReader) next() error {
	if !x.inside {
		return x.stream(false)
	}
	size, err := x.in.ReadByte()
	if err != nil {
		return errXZShort
	}
	if size == 0 {
		return x.index()
	}
	return x.startBlock(size)
}

// stream reads a stream header. Between streams, groups of four null
// bytes are stream padding, and the end of the input ends the file; the
// first stream starts at once.
func (x *xzReader) stream(first bool) error {
	var header [12]byte
	for {
		n, err := io.ReadFull(x.in, header[:4])
		if !first && n == 0 && errors.Is(err, io.EOF) {
			return io.EOF
		}
		if err != nil {
			return errXZShort
		}
		if first || !bytes.Equal(header[:4], []byte{0, 0, 0, 0}) {
			break
		}
	}
	if _, err := io.ReadFull(x.in, header[4:]); err != nil {
		return errXZShort
	}
	if !bytes.Equal(header[:6], xzMagic) ||
		crc32.ChecksumIEEE(header[6:8]) != binary.LittleEndian.Uint32(header[8:]) ||
		header[6] != 0 || checkSize(header[7]) < 0 {
		return errXZ
	}
	x.flags, x.records, x.inside = header[7], x.records[:0], true
	return nil
}

func checkSize(flags byte) int {
	switch flags {
	case xzCheckNone:
		return 0
	case xzCheckCRC32:
		return 4
	case xzCheckCRC64:
		return 8
	case xzCheckSHA256:
		return sha256.Size
	}
	return -1
}

// xzBlock is a block being decoded: its LZMA2 data, the compressed bytes
// read and the uncompressed bytes produced, against the sizes its header
// declares (-1 when it declares none), and its check.
type xzBlock struct {
	data                     io.Reader
	read                     *xzCount
	header, n                int64
	compressed, uncompressed int64
	check                    hash.Hash
}

type xzCount struct {
	r io.Reader
	n int64
}

func (c *xzCount) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err //nolint:wrapcheck // the LZMA2 decoder tells io.EOF apart
}

// startBlock reads a block header whose size byte is size and starts its
// LZMA2 data. It accepts what ulikunitz/xz's block header accepts: one
// filter, LZMA2, and header padding of any length if it is null.
func (x *xzReader) startBlock(size byte) error {
	header := make([]byte, (int(size)+1)*4)
	header[0] = size
	if _, err := io.ReadFull(x.in, header[1:]); err != nil {
		return errXZShort
	}
	end := len(header) - 4
	flags := header[1]
	if crc32.ChecksumIEEE(header[:end]) != binary.LittleEndian.Uint32(header[end:]) ||
		flags&0x3f != 0 {
		return errXZ
	}
	r := bytes.NewReader(header[2:end])
	b := &xzBlock{header: int64(len(header)), compressed: -1, uncompressed: -1}
	for _, field := range []struct {
		present byte
		size    *int64
	}{{0x40, &b.compressed}, {0x80, &b.uncompressed}} {
		if flags&field.present == 0 {
			continue
		}
		// The format's largest size, its multibyte integer's, is 2^63-1.
		v, err := binary.ReadUvarint(r)
		if err != nil || v >= 1<<63 {
			return errXZ
		}
		*field.size = int64(v) //nolint:gosec // G115: v is below 2^63, checked above
	}
	id, err := binary.ReadUvarint(r)
	var props [2]byte
	if err != nil || id != xzLZMA2 {
		return errXZ
	}
	if _, err = io.ReadFull(r, props[:]); err != nil || props[0] != 1 {
		return errXZ
	}
	dict, err := lzma.DecodeDictCap(props[1])
	if err != nil {
		return errXZ
	}
	for r.Len() > 0 {
		if c, _ := r.ReadByte(); c != 0 {
			return errXZ
		}
	}
	b.read = &xzCount{r: x.in}
	b.data, err = lzma.Reader2Config{DictCap: max(xzDictCap, int(dict))}.NewReader2(b.read)
	if err != nil {
		return errXZ
	}
	switch x.flags {
	case xzCheckCRC32:
		b.check = crc32.NewIEEE()
	case xzCheckCRC64:
		b.check = crc64.New(xzCRC64)
	case xzCheckSHA256:
		b.check = sha256.New()
	}
	x.block = b
	return nil
}

func (b *xzBlock) Read(p []byte) (int, error) {
	n, err := b.data.Read(p)
	if b.check != nil {
		b.check.Write(p[:n])
	}
	b.n += int64(n)
	if b.uncompressed >= 0 && b.n > b.uncompressed ||
		b.compressed >= 0 && b.read.n > b.compressed {
		return n, errXZ
	}
	return n, err //nolint:wrapcheck // Read passes io.EOF on to endBlock
}

// endBlock checks a block whose LZMA2 data ended: its declared sizes, its
// padding and its check, and records it for the index.
func (x *xzReader) endBlock() error {
	b := x.block
	x.block = nil
	if b.n < b.uncompressed || b.read.n < b.compressed {
		return errXZ
	}
	pad := int(-b.read.n & 3)
	tail := make([]byte, pad+checkSize(x.flags))
	if _, err := io.ReadFull(x.in, tail); err != nil {
		return errXZShort
	}
	var sum []byte
	switch x.flags {
	case xzCheckCRC32:
		sum = binary.LittleEndian.AppendUint32(nil, b.check.(hash.Hash32).Sum32())
	case xzCheckCRC64:
		sum = binary.LittleEndian.AppendUint64(nil, b.check.(hash.Hash64).Sum64())
	case xzCheckSHA256:
		sum = b.check.Sum(nil)
	}
	if !bytes.Equal(tail[:pad], make([]byte, pad)) || !bytes.Equal(tail[pad:], sum) {
		return errXZ
	}
	x.records = append(x.records, xzRecord{
		unpadded:     b.header + b.read.n + int64(checkSize(x.flags)),
		uncompressed: b.n,
	})
	return nil
}

// xzIndex reads index bytes, adding each to the index's CRC32 and count.
type xzIndex struct {
	in  *bufio.Reader
	crc hash.Hash32
	n   int64
}

func (r *xzIndex) ReadByte() (byte, error) {
	c, err := r.in.ReadByte()
	if err != nil {
		return 0, errXZShort
	}
	r.crc.Write([]byte{c})
	r.n++
	return c, nil
}

// index reads the index, whose indicator was read, and the footer: the
// index must list each block read with its unpadded and uncompressed
// sizes, and the footer must name the index's size and the stream's check
// type.
func (x *xzReader) index() error {
	r := &xzIndex{in: x.in, crc: crc32.NewIEEE()}
	r.crc.Write([]byte{0})
	count, err := binary.ReadUvarint(r)
	if err != nil {
		return errXZShort
	}
	if count != uint64(len(x.records)) {
		return errXZ
	}
	for _, record := range x.records {
		for _, want := range []int64{record.unpadded, record.uncompressed} {
			v, err := binary.ReadUvarint(r)
			if err != nil {
				return errXZShort
			}
			//nolint:gosec // G115: want counts bytes read, so it is never negative
			if v != uint64(want) {
				return errXZ
			}
		}
	}
	for range -(r.n + 1) & 3 {
		c, err := r.ReadByte()
		if err != nil {
			return err
		}
		if c != 0 {
			return errXZ
		}
	}
	size := 1 + r.n + 4
	var tail [16]byte
	if _, err = io.ReadFull(x.in, tail[:]); err != nil {
		return errXZShort
	}
	footer := tail[4:]
	if binary.LittleEndian.Uint32(tail[:4]) != r.crc.Sum32() ||
		!bytes.Equal(footer[10:], xzFooter) ||
		crc32.ChecksumIEEE(footer[4:10]) != binary.LittleEndian.Uint32(footer[:4]) ||
		(int64(binary.LittleEndian.Uint32(footer[4:8]))+1)*4 != size ||
		footer[8] != 0 || footer[9] != x.flags {
		return errXZ
	}
	x.inside = false
	return nil
}
