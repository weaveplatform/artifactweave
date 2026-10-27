package pypi

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"path"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/archive"
)

const (
	remoteArchiveEntries    = 1 << 20
	remoteArchiveInflated   = int64(16 << 30)
	remoteZipDirectoryLimit = int64(64 << 20)
)

type archiveReader struct {
	ctx       context.Context
	r         io.Reader
	remaining *int64
}

func (r archiveReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err //nolint:wrapcheck // preserve cancellation
	}
	if r.remaining != nil {
		if *r.remaining < 0 {
			return 0, remoteError()
		}
		if int64(len(p)) > *r.remaining+1 {
			p = p[:*r.remaining+1]
		}
	}
	n, err := r.r.Read(p)
	if r.remaining != nil {
		*r.remaining -= int64(n)
		if *r.remaining < 0 {
			return n, remoteError()
		}
	}
	return n, err //nolint:wrapcheck // Reader contract
}

type archiveReaderAt struct {
	ctx       context.Context
	r         io.ReaderAt
	remaining int64
	isBounded bool
}

func (r *archiveReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err //nolint:wrapcheck // preserve cancellation
	}
	if r.isBounded {
		r.remaining -= int64(len(p))
		if r.remaining < 0 {
			return 0, remoteError()
		}
	}
	return r.r.ReadAt(p, off) //nolint:wrapcheck // ReaderAt contract
}

// Indexing intentionally stops after package metadata. Remote admission instead
// reads every entry and compression trailer: a matching advertised digest does
// not make a corrupt archive installable.
func validateCompleteArchive(
	ctx context.Context,
	ra io.ReaderAt,
	size int64,
	name string,
	entries int,
	inflated int64,
) (err error) {
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	checked := &archiveReaderAt{ctx: ctx, r: ra}
	if err := ctx.Err(); err != nil {
		return err //nolint:wrapcheck // preserve cancellation
	}
	if strings.HasSuffix(name, ".whl") || strings.HasSuffix(name, ".egg") ||
		strings.HasSuffix(name, ".zip") {
		return validateRemoteZip(ctx, checked, size, entries, inflated)
	}
	var stream io.Reader = io.NewSectionReader(checked, 0, size)
	switch {
	case strings.HasSuffix(name, ".tar.gz"), strings.HasSuffix(name, ".tgz"):
		gz, err := gzip.NewReader(stream)
		if err != nil {
			return remoteError()
		}
		defer func() { _ = gz.Close() }()
		stream = gz
	case strings.HasSuffix(name, ".tar.bz2"):
		stream = bzip2.NewReader(stream)
	case strings.HasSuffix(name, ".tar"):
	default:
		return remoteError()
	}
	return validateRemoteTar(ctx, stream, entries, inflated)
}

func validateRemoteZip(
	ctx context.Context,
	ra *archiveReaderAt,
	size int64,
	entries int,
	inflated int64,
) error {
	if err := zipDirectoryBound(ra, size, entries); err != nil {
		return err
	}
	// Bound directory parsing too: ZIP headers can lie about their entry count.
	ra.remaining, ra.isBounded = remoteZipDirectoryLimit, true
	zr, err := zip.NewReader(ra, size)
	ra.isBounded = false
	if err != nil || len(zr.File) > entries {
		return remoteError()
	}
	seen := map[string]bool{}
	for _, f := range zr.File {
		n, e := archive.Clean(f.Name)
		if e != nil || seen[n] || (n == "" && !f.FileInfo().IsDir()) {
			return remoteError()
		}
		seen[n] = true
		if f.UncompressedSize64 > uint64(max(inflated, 0)) {
			return remoteError()
		}
		body, e := f.Open()
		if e != nil {
			return remoteError()
		}
		_, e = io.Copy(io.Discard, archiveReader{ctx: ctx, r: body, remaining: &inflated})
		closeErr := body.Close()
		if e != nil || closeErr != nil {
			return remoteError()
		}
	}
	return ctx.Err() //nolint:wrapcheck // preserve cancellation
}

// Check ZIP/ZIP64's allocation hints before archive/zip allocates its directory.
// The following bounded ReaderAt also limits a directory that understates them.
func zipDirectoryBound(ra io.ReaderAt, size int64, entries int) error {
	if size < 22 {
		return remoteError()
	}
	tail := make([]byte, min(size, 65557))
	if _, err := ra.ReadAt(tail, size-int64(len(tail))); err != nil {
		return remoteError()
	}
	for i := len(tail) - 22; i >= 0; i-- {
		if binary.LittleEndian.Uint32(tail[i:]) != 0x06054b50 ||
			i+22+int(binary.LittleEndian.Uint16(tail[i+20:])) != len(tail) {
			continue
		}
		count := uint64(binary.LittleEndian.Uint16(tail[i+10:]))
		directory := uint64(binary.LittleEndian.Uint32(tail[i+12:]))
		if count == 0xffff || directory == 0xffffffff ||
			binary.LittleEndian.Uint32(tail[i+16:]) == 0xffffffff {
			offset := size - int64(len(tail)) + int64(i) - 20
			if offset < 0 {
				return remoteError()
			}
			locator := make([]byte, 20)
			if _, err := ra.ReadAt(
				locator,
				offset,
			); err != nil ||
				binary.LittleEndian.Uint32(locator) != 0x07064b50 {
				return remoteError()
			}
			recordOffset := binary.LittleEndian.Uint64(locator[8:])
			if size < 56 || recordOffset > uint64(size-56) {
				return remoteError()
			}
			record := make([]byte, 56)
			recordPosition := int64(recordOffset) //nolint:gosec // size checked above
			if _, err := ra.ReadAt(record, recordPosition); err != nil ||
				binary.LittleEndian.Uint32(record) != 0x06064b50 {
				return remoteError()
			}
			count = binary.LittleEndian.Uint64(record[32:])
			directory = binary.LittleEndian.Uint64(record[40:])
		}
		if count > uint64(max(entries, 0)) || directory > uint64(remoteZipDirectoryLimit) {
			return remoteError()
		}
		return nil
	}
	return remoteError()
}

func validateRemoteTar(ctx context.Context, stream io.Reader, entries int, inflated int64) error {
	bounded := &tarTrailerReader{r: archiveReader{ctx: ctx, r: stream, remaining: &inflated}}
	tr := tar.NewReader(bounded)
	seen := map[string]bool{}
	for count := 0; ; count++ {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || count >= entries {
			return remoteError()
		}
		n, err := archive.Clean(h.Name)
		if err != nil || seen[n] || (n == "" && h.Typeflag != tar.TypeDir) {
			return remoteError()
		}
		seen[n] = true
		if h.Typeflag == tar.TypeSymlink || h.Typeflag == tar.TypeLink {
			target := h.Linkname
			if strings.HasPrefix(target, "/") || strings.ContainsAny(target, "\\\x00\r\n") ||
				(len(target) > 1 && target[1] == ':') {
				return remoteError()
			}
			if h.Typeflag == tar.TypeSymlink {
				// Nothing is extracted; archive.Clean checks the resolved target below.
				//nolint:gosec
				target = path.Join(path.Dir(n), target)
			}
			if _, err = archive.Clean(target); err != nil {
				return remoteError()
			}
		}
		// tar reads through the shared inflation budget.
		//nolint:gosec
		if _, err = io.Copy(io.Discard, tr); err != nil {
			return remoteError()
		}
	}
	// tar stops at its zero end blocks, before a gzip/bzip2 stream necessarily
	// checks its trailer. Only zero tar padding is permitted after those blocks.
	buf := make([]byte, 32<<10)
	for {
		n, err := bounded.Read(buf)
		if bytes.IndexFunc(buf[:n], func(r rune) bool { return r != 0 }) >= 0 {
			return remoteError()
		}
		if errors.Is(err, io.EOF) {
			if bounded.size < 1024 || bounded.size%512 != 0 ||
				bytes.Count(bounded.tail[:], []byte{0}) != len(bounded.tail) {
				return remoteError()
			}
			return nil
		}
		if err != nil {
			return remoteError()
		}
	}
}

// archive/tar tolerates an EOF inside end padding; remote admission also requires
// complete 512-byte records and the two zero termination records.
type tarTrailerReader struct {
	r    io.Reader
	size int64
	tail [1024]byte
}

func (r *tarTrailerReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.size += int64(n)
	if n >= len(r.tail) {
		copy(r.tail[:], p[n-len(r.tail):n])
	} else {
		copy(r.tail[:], r.tail[n:])
		copy(r.tail[len(r.tail)-n:], p[:n])
	}
	return n, err //nolint:wrapcheck // Reader contract
}
