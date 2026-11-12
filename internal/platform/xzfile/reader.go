package xzfile

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"hash/crc64"
	"io"
	"math"

	"github.com/ulikunitz/xz/lzma"
)

// MaxDict is the largest dictionary a block header may name: 1.5 GiB,
// dictionary property 37. Artifactory decodes xz with XZ for Java, whose
// LZMA2Decoder refuses a property above 37 before it allocates anything:
// https://github.com/tukaani-project/xz-java/blob/f14a6039/src/org/tukaani/xz/LZMA2Decoder.java#L15
const MaxDict = 3 << 29

// maxDictProperty is the dictionary property that names MaxDict.
const maxDictProperty = 37

// The xz container's constants, from the .xz file format 1.2.1: the check
// types and the one filter github.com/ulikunitz/xz decodes, LZMA2.
const (
	checkNone   = 0x00
	checkCRC32  = 0x01
	checkCRC64  = 0x04
	checkSHA256 = 0x0a
	filterLZMA2 = 0x21
)

var (
	headerMagic = []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}
	footerMagic = []byte{'Y', 'Z'}
	crc64Table  = crc64.MakeTable(crc64.ECMA)
)

// Every refusal but an early end carries the text github.com/ulikunitz/xz's
// Reader gives the same fault, and an early end is io.ErrUnexpectedEOF, as
// it is there wherever that reader notices one. Debian names the fault in
// its 400 for a package ("Invalid Debian package: ..."), so a package that
// was refused before is refused in the same words.
var (
	errHeaderMagic    = errors.New("xz: invalid header magic bytes")
	errHeaderCRC      = errors.New("xz: invalid checksum for file header")
	errFlags          = errors.New("xz: invalid flags")
	errPadding        = errors.New("xz: padding (4 zero bytes) encountered")
	errBlockHeaderCRC = errors.New("xz: checksum error for block header")
	errBlockFlags     = errors.New("xz: reserved block header flags set")
	errUvarint        = errors.New("xz: uvarint overflows 64-bit unsigned integer")
	errSizeOverflow   = errors.New("xz: size overflow in block header")
	errFilterCount    = errors.New("xz: unsupported filter count")
	errFilterReserved = errors.New("xz: reserved filter id in block stream header")
	errFilterID       = errors.New("xz: invalid filter id")
	errFilterSize     = errors.New("xz: wrong LZMA2 filter size")
	errDictProperty   = errors.New("xz: wrong LZMA2 dictionary size property")
	errUncompressed   = errors.New("xz: wrong uncompressed size for block")
	errCompressed     = errors.New("xz: wrong compressed size for block")
	errBlockPadding   = errors.New("xz: non-zero block padding")
	errBlockCheck     = errors.New("xz: checksum error for block")
	errRecordCount    = errors.New("xz: record number overflow")
	// errIndexLength and errRecord begin texts that go on with the counts
	// and records ulikunitz/xz prints after them.
	errIndexLength     = errors.New("xz: index length is")
	errRecord          = errors.New("xz: record")
	errUnpadded        = errors.New("xz: unpadded size negative")
	errUncompressedNeg = errors.New("xz: uncompressed size negative")
	errIndexPadding    = errors.New("xz: non-zero byte in index padding")
	errIndexCRC        = errors.New("xz: wrong checksum for index")
	errFooterMagic     = errors.New("xz: footer magic invalid")
	errFooterCRC       = errors.New("xz: footer checksum error")
	errFooterFlags     = errors.New("xz: footer flags incorrect")
	errFooterSize      = errors.New("xz: index size in footer wrong")
)

// reader decodes an xz file as liblzma's stream decoder does with
// LZMA_CONCATENATED, which xz -t and libarchive use:
// one or more streams, each followed by optional stream padding, where
// every stream ends in an index that lists exactly the blocks read and a
// footer that matches the index and the stream's header.
//
// github.com/ulikunitz/xz's own Reader takes an input that stops where a
// block header or the index must start for the end of the stream, so a
// file whose index and footer were cut off was admitted although no
// client can read it (#1297, #1331). SingleStream does not change that:
// the reader still sees the end of the input first. This reader keeps
// that package's LZMA2 decoder and the checks its Reader makes, and parses
// the container itself, so the end of the input is an error anywhere but
// between streams. Parsing the container is also what lets it choose the
// dictionary, which that Reader takes from the block header (#1332).
type reader struct {
	in *bufio.Reader
	// limit is the most bytes the caller reads, or 0 for no limit.
	limit int64
	// flags is the current stream's check type, and records the index
	// entries of the blocks read in it.
	flags   byte
	records []record
	inside  bool
	block   *block
	err     error
}

// record is an index entry. ulikunitz/xz prints its records with %v in
// the error for an index that does not match the blocks, so the field
// order is its order.
type record struct{ unpadded, uncompressed int64 }

// NewReader reads the first stream header of in, as xz.NewReader does,
// and returns the reader of the whole file's content. limit is the most
// bytes the caller reads from it, or 0 for no limit: a match cannot reach
// further back than the content decoded, so a block is decoded with a
// dictionary no larger than limit and reads everything the caller does.
func NewReader(in io.Reader, limit int64) (io.Reader, error) {
	x := &reader{in: bufio.NewReaderSize(in, 32<<10), limit: limit}
	if err := x.stream(true); err != nil {
		return nil, err
	}
	return x, nil
}

// dictSize returns the dictionary for a block whose header names dict: dict,
// as XZ for Java and liblzma allocate it, but no more than the caller
// reads, and no less than the 4 KiB the LZMA2 decoder takes.
func dictSize(dict, limit int64) int {
	if limit > 0 {
		dict = min(dict, limit)
	}
	return int(max(dict, lzma.MinDictCap))
}

func (x *reader) Read(p []byte) (int, error) {
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
func (x *reader) next() error {
	if !x.inside {
		return x.stream(false)
	}
	size, err := x.in.ReadByte()
	if err != nil {
		return io.ErrUnexpectedEOF
	}
	if size == 0 {
		return x.index()
	}
	return x.startBlock(size)
}

// stream reads a stream header. Between streams, groups of four null
// bytes are stream padding, and the end of the input ends the file. The
// first stream must start at once: xz.NewReader refuses padding before it,
// and so does liblzma.
func (x *reader) stream(first bool) error {
	var header [12]byte
	for {
		n, err := io.ReadFull(x.in, header[:4])
		if !first && n == 0 && errors.Is(err, io.EOF) {
			return io.EOF
		}
		if err != nil {
			return io.ErrUnexpectedEOF
		}
		if !bytes.Equal(header[:4], []byte{0, 0, 0, 0}) {
			break
		}
		if first {
			return errPadding
		}
	}
	if _, err := io.ReadFull(x.in, header[4:]); err != nil {
		return io.ErrUnexpectedEOF
	}
	switch {
	case !bytes.Equal(header[:6], headerMagic):
		return errHeaderMagic
	case crc32.ChecksumIEEE(header[6:8]) != binary.LittleEndian.Uint32(header[8:]):
		return errHeaderCRC
	case header[6] != 0 || checkSize(header[7]) < 0:
		return errFlags
	}
	x.flags, x.records, x.inside = header[7], x.records[:0], true
	return nil
}

func checkSize(flags byte) int {
	switch flags {
	case checkNone:
		return 0
	case checkCRC32:
		return 4
	case checkCRC64:
		return 8
	case checkSHA256:
		return sha256.Size
	}
	return -1
}

// readUvarint reads a multibyte integer as ulikunitz/xz reads one: at most
// ten bytes, the tenth no more than 1. It returns the error of r as it is,
// io.EOF included.
func readUvarint(r io.ByteReader) (uint64, error) {
	var x uint64
	for i, s := 1, uint(0); ; i, s = i+1, s+7 {
		b, err := r.ReadByte()
		if err != nil {
			return x, err //nolint:wrapcheck // the caller tells io.EOF apart
		}
		if i > 10 || i == 10 && b > 1 {
			return x, errUvarint
		}
		if b < 0x80 {
			return x | uint64(b)<<s, nil
		}
		x |= uint64(b&0x7f) << s
	}
}

// block is a block being decoded: its LZMA2 data, the compressed bytes
// read and the uncompressed bytes produced, against the sizes its header
// declares (-1 when it declares none), and its check.
type block struct {
	data                     io.Reader
	read                     *count
	header, n                int64
	compressed, uncompressed int64
	check                    hash.Hash
}

type count struct {
	r io.Reader
	n int64
}

func (c *count) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err //nolint:wrapcheck // the LZMA2 decoder tells io.EOF apart
}

// startBlock reads a block header whose size byte is size and starts its
// LZMA2 data. It checks what ulikunitz/xz's block header checks, in its
// order: one filter, LZMA2, and header padding of any length if it is null.
// A field that runs past the header is an early end; ulikunitz/xz instead
// ends the stream there and reads on as if a new stream started.
func (x *reader) startBlock(size byte) error {
	header := make([]byte, (int(size)+1)*4)
	header[0] = size
	if _, err := io.ReadFull(x.in, header[1:]); err != nil {
		return io.ErrUnexpectedEOF
	}
	end := len(header) - 4
	flags := header[1]
	switch {
	case crc32.ChecksumIEEE(header[:end]) != binary.LittleEndian.Uint32(header[end:]):
		return errBlockHeaderCRC
	case flags&0x3c != 0:
		return errBlockFlags
	}
	r := bytes.NewReader(header[2:end])
	b := &block{header: int64(len(header)), compressed: -1, uncompressed: -1}
	for _, field := range []struct {
		present byte
		size    *int64
	}{{0x40, &b.compressed}, {0x80, &b.uncompressed}} {
		if flags&field.present == 0 {
			continue
		}
		v, err := readUvarint(r)
		switch {
		case errors.Is(err, io.EOF):
			return io.ErrUnexpectedEOF
		case err != nil:
			return err
		case v >= 1<<63:
			return errSizeOverflow
		}
		*field.size = int64(v) //nolint:gosec // G115: v is below 2^63, checked above
	}
	dict, err := lzma2Filter(r, flags)
	if err != nil {
		return err
	}
	for r.Len() > 0 {
		if c, _ := r.ReadByte(); c != 0 {
			return errPadding
		}
	}
	// Allocating what the header names, as ulikunitz/xz did, let a file of
	// a few bytes take 4 GiB (#1332). The caller never reads past limit,
	// so a dictionary of limit refuses nothing the header's would read.
	b.read = &count{r: x.in}
	b.data, err = lzma.Reader2Config{DictCap: dictSize(dict, x.limit)}.NewReader2(b.read)
	if err != nil {
		return err //nolint:wrapcheck // the decoder's own text, as ulikunitz/xz gives it
	}
	switch x.flags {
	case checkCRC32:
		b.check = crc32.NewIEEE()
	case checkCRC64:
		b.check = crc64.New(crc64Table)
	case checkSHA256:
		b.check = sha256.New()
	}
	x.block = b
	return nil
}

// lzma2Filter reads a block header's filter flags, which must name one
// filter, LZMA2, and returns the dictionary size its property names.
func lzma2Filter(r *bytes.Reader, flags byte) (int64, error) {
	if flags&0x03 != 0 {
		return 0, errFilterCount
	}
	id, err := readUvarint(r)
	switch {
	case errors.Is(err, io.EOF):
		return 0, io.ErrUnexpectedEOF
	case err != nil:
		return 0, err
	case id >= 1<<62:
		return 0, errFilterReserved
	case id != filterLZMA2:
		return 0, errFilterID
	}
	var props [2]byte
	if _, err = io.ReadFull(r, props[:]); err != nil {
		return 0, io.ErrUnexpectedEOF
	}
	if props[0] != 1 {
		return 0, errFilterSize
	}
	// ulikunitz/xz accepted properties 38 to 40, 2 GiB to 4 GiB, and
	// refused the bytes above them with this text; XZ for Java refuses
	// every byte above 37 (MaxDict).
	if props[1] > maxDictProperty {
		return 0, errDictProperty
	}
	return int64(2|props[1]&1) << (props[1]/2 + 11), nil
}

func (b *block) Read(p []byte) (int, error) {
	n, err := b.data.Read(p)
	if b.check != nil {
		b.check.Write(p[:n])
	}
	b.n += int64(n)
	switch {
	case b.uncompressed >= 0 && b.n > b.uncompressed:
		return n, errUncompressed
	case b.compressed >= 0 && b.read.n > b.compressed:
		return n, errCompressed
	}
	return n, err //nolint:wrapcheck // Read passes io.EOF on to endBlock
}

// endBlock checks a block whose LZMA2 data ended: its declared sizes, its
// padding and its check, and records it for the index.
func (x *reader) endBlock() error {
	b := x.block
	x.block = nil
	if b.n < b.uncompressed || b.read.n < b.compressed {
		return io.ErrUnexpectedEOF
	}
	pad := int(-b.read.n & 3)
	tail := make([]byte, pad+checkSize(x.flags))
	if _, err := io.ReadFull(x.in, tail); err != nil {
		return io.ErrUnexpectedEOF
	}
	var sum []byte
	switch x.flags {
	case checkCRC32:
		sum = binary.LittleEndian.AppendUint32(nil, b.check.(hash.Hash32).Sum32())
	case checkCRC64:
		sum = binary.LittleEndian.AppendUint64(nil, b.check.(hash.Hash64).Sum64())
	case checkSHA256:
		sum = b.check.Sum(nil)
	}
	if !bytes.Equal(tail[:pad], make([]byte, pad)) {
		return errBlockPadding
	}
	if !bytes.Equal(tail[pad:], sum) {
		return errBlockCheck
	}
	x.records = append(x.records, record{
		unpadded:     b.header + b.read.n + int64(checkSize(x.flags)),
		uncompressed: b.n,
	})
	return nil
}

// indexReader reads index bytes, adding each to the index's CRC32 and
// count, and makes the end of the input an early end.
type indexReader struct {
	in  *bufio.Reader
	crc hash.Hash32
	n   int64
}

func (r *indexReader) ReadByte() (byte, error) {
	c, err := r.in.ReadByte()
	if err != nil {
		return 0, io.ErrUnexpectedEOF
	}
	r.crc.Write([]byte{c})
	r.n++
	return c, nil
}

// index reads the index, whose indicator was read, and the footer, in
// ulikunitz/xz's order: the record count, every record, the padding and
// the CRC32, then whether the records match the blocks read, then the
// footer, which must name the index's size and the stream's check type.
func (x *reader) index() error {
	r := &indexReader{in: x.in, crc: crc32.NewIEEE()}
	r.crc.Write([]byte{0})
	count, err := readUvarint(r)
	switch {
	case err != nil:
		return err
	case count > math.MaxInt:
		return errRecordCount
	case count != uint64(len(x.records)):
		return fmt.Errorf("%w %d; want %d", errIndexLength, count, len(x.records))
	}
	listed := make([]record, len(x.records))
	for i := range listed {
		for _, field := range []struct {
			v        *int64
			negative error
		}{{&listed[i].unpadded, errUnpadded}, {&listed[i].uncompressed, errUncompressedNeg}} {
			v, err := readUvarint(r)
			switch {
			case err != nil:
				return err
			case v >= 1<<63:
				return field.negative
			}
			*field.v = int64(v) //nolint:gosec // G115: v is below 2^63, checked above
		}
	}
	var padding byte
	for range -(r.n + 1) & 3 {
		c, err := r.ReadByte()
		if err != nil {
			return err
		}
		padding |= c
	}
	if padding != 0 {
		return errIndexPadding
	}
	size := 1 + r.n + 4
	var tail [16]byte
	if _, err = io.ReadFull(x.in, tail[:4]); err != nil {
		return io.ErrUnexpectedEOF
	}
	if binary.LittleEndian.Uint32(tail[:4]) != r.crc.Sum32() {
		return errIndexCRC
	}
	for i, want := range listed {
		if x.records[i] != want {
			return fmt.Errorf("%w %d is %v; want %v", errRecord, i, x.records[i], want)
		}
	}
	if _, err = io.ReadFull(x.in, tail[4:]); err != nil {
		return io.ErrUnexpectedEOF
	}
	footer := tail[4:]
	switch {
	case !bytes.Equal(footer[10:], footerMagic):
		return errFooterMagic
	case crc32.ChecksumIEEE(footer[4:10]) != binary.LittleEndian.Uint32(footer[:4]):
		return errFooterCRC
	case footer[8] != 0 || checkSize(footer[9]) < 0:
		return errFlags
	case footer[9] != x.flags:
		return errFooterFlags
	case (int64(binary.LittleEndian.Uint32(footer[4:8]))+1)*4 != size:
		return errFooterSize
	}
	x.inside = false
	return nil
}
