package nix

import (
	"compress/bzip2"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// Archive describes both the transport bytes and the uncompressed NAR.
type Archive struct {
	FileHash string
	FileSize int64
	NarHash  string
	NarSize  int64
}

func ReadArchive(input io.Reader, size int64, compression string) (Archive, error) {
	var out Archive
	if size <= 0 || size > MaxArchiveBytes {
		return out, ErrInvalid
	}
	fileHash := sha256.New()
	compressed := &io.LimitedReader{R: input, N: size + 1}
	raw := io.TeeReader(compressed, fileHash)
	var reader io.Reader
	switch compression {
	case "none":
		reader = raw
	case "xz":
		r, err := (xz.ReaderConfig{DictCap: 64 << 20}).NewReader(raw)
		if err != nil {
			return out, ErrInvalid
		}
		reader = r
	case "bzip2":
		reader = bzip2.NewReader(raw)
	case "gzip":
		r, err := gzip.NewReader(raw)
		if err != nil {
			return out, ErrInvalid
		}
		defer r.Close()
		reader = r
	case "zstd":
		r, err := zstd.NewReader(
			raw,
			zstd.WithDecoderMaxMemory(64<<20),
			zstd.WithDecoderConcurrency(1),
		)
		if err != nil {
			return out, ErrInvalid
		}
		defer r.Close()
		reader = r
	default:
		return out, ErrInvalid
	}
	narHash := sha256.New()
	expanded := &io.LimitedReader{R: reader, N: MaxNarBytes + 1}
	parser := narReader{r: io.TeeReader(expanded, narHash)}
	if !parser.expect("nix-archive-1") || !parser.node(0) {
		return out, ErrInvalid
	}
	var tail [1]byte
	n, err := parser.r.Read(tail[:])
	if n != 0 || !errors.Is(err, io.EOF) {
		return out, ErrInvalid
	}
	// Consume compressed trailers and reject bytes after an uncompressed NAR.
	if _, err = io.Copy(io.Discard, raw); err != nil || compressed.N != 1 {
		return out, ErrInvalid
	}
	out = Archive{
		FileHash: hex.EncodeToString(fileHash.Sum(nil)),
		FileSize: size,
		NarHash:  hex.EncodeToString(narHash.Sum(nil)),
		NarSize:  MaxNarBytes + 1 - expanded.N,
	}
	return out, nil
}

type narReader struct {
	r       io.Reader
	entries int
}

func (n *narReader) length() (uint64, bool) {
	var b [8]byte
	_, err := io.ReadFull(n.r, b[:])
	return binary.LittleEndian.Uint64(b[:]), err == nil
}

func (n *narReader) padding(length uint64) bool {
	var b [8]byte
	count := (8 - length%8) % 8
	_, err := io.ReadFull(n.r, b[:count])
	if err != nil {
		return false
	}
	for _, v := range b[:count] {
		if v != 0 {
			return false
		}
	}
	return true
}

func (n *narReader) token() (string, bool) {
	size, ok := n.length()
	if !ok || size > 4096 {
		return "", false
	}
	b := make([]byte, int(size))
	_, err := io.ReadFull(n.r, b)
	return string(b), err == nil && n.padding(size)
}
func (n *narReader) expect(want string) bool { s, ok := n.token(); return ok && s == want }
func (n *narReader) content() bool {
	size, ok := n.length()
	if !ok || size > uint64(MaxNarBytes) {
		return false
	}
	_, err := io.CopyN(io.Discard, n.r, int64(size))
	return err == nil && n.padding(size)
}

func (n *narReader) node(depth int) bool {
	n.entries++
	if depth > 128 || n.entries > 100000 || !n.expect("(") || !n.expect("type") {
		return false
	}
	kind, ok := n.token()
	if !ok {
		return false
	}
	switch kind {
	case "regular":
		return n.regular()
	case "symlink":
		if !n.expect("target") {
			return false
		}
		target, ok := n.token()
		return ok && target != "" && !strings.ContainsRune(target, 0) && n.expect(")")
	case "directory":
		return n.directory(depth)
	default:
		return false
	}
}

func (n *narReader) regular() bool {
	field, ok := n.token()
	if !ok {
		return false
	}
	if field == "executable" {
		if !n.expect("") {
			return false
		}
		field, ok = n.token()
	}
	return ok && field == "contents" && n.content() && n.expect(")")
}

func (n *narReader) directory(depth int) bool {
	previous := ""
	for {
		field, ok := n.token()
		if !ok {
			return false
		}
		if field == ")" {
			return true
		}
		if field != "entry" || !n.expect("(") || !n.expect("name") {
			return false
		}
		name, ok := n.token()
		if !ok || name == "" || name == "." || name == ".." ||
			strings.ContainsAny(name, "/\x00") ||
			name <= previous {
			return false
		}
		previous = name
		if !n.expect("node") || !n.node(depth+1) || !n.expect(")") {
			return false
		}
	}
}

func (a Archive) Matches(info Info) bool {
	return a.FileHash == info.FileHash && a.FileSize == info.FileSize &&
		a.NarHash == info.NarHash &&
		a.NarSize == info.NarSize
}
