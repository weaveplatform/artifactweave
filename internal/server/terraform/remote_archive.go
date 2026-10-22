package terraform

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
	"io/fs"
	"path"
	"strings"

	"github.com/ulikunitz/xz"

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
			return 0, remoteModuleError()
		}
		if int64(len(p)) > *r.remaining+1 {
			p = p[:*r.remaining+1]
		}
	}
	n, err := r.r.Read(p)
	if r.remaining != nil {
		*r.remaining -= int64(n)
		if *r.remaining < 0 {
			return n, remoteModuleError()
		}
	}
	return n, err //nolint:wrapcheck // Reader contract
}

type archiveReaderAt struct {
	ctx       context.Context
	r         io.ReaderAt
	remaining int64
	bounded   bool
}

func (r *archiveReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err //nolint:wrapcheck // preserve cancellation
	}
	if r.bounded {
		r.remaining -= int64(len(p))
		if r.remaining < 0 {
			return 0, remoteModuleError()
		}
	}
	return r.r.ReadAt(p, off) //nolint:wrapcheck // ReaderAt contract
}

// Remote modules retain the exact source bytes. Complete entry/CRC/trailer
// validation protects both persistent and transient admission before a signed
// download binds those bytes to their computed digest.
func validateCompleteArchive(
	ctx context.Context,
	ra io.ReaderAt,
	size int64,
	name string,
	entries int,
	inflated int64,
) error {
	return validateSelectedArchive(ctx, ra, size, name, entries, inflated, "", nil)
}

func validateSelectedArchive(
	ctx context.Context,
	ra io.ReaderAt,
	size int64,
	name string,
	entries int,
	inflated int64,
	subdir string,
	selected *string,
) (err error) {
	selection := moduleSelection{pattern: subdir, matches: map[string]bool{}}
	defer func() {
		if err == nil && subdir != "" && len(selection.matches) != 1 {
			err = remoteModuleError()
		}
		if err == nil && selected != nil {
			for directory := range selection.matches {
				*selected = directory
			}
		}
	}()
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	checked := &archiveReaderAt{ctx: ctx, r: ra}
	if err := ctx.Err(); err != nil {
		return err //nolint:wrapcheck // preserve cancellation
	}
	if strings.HasSuffix(name, ".zip") {
		return validateRemoteZip(ctx, checked, size, entries, inflated, &selection)
	}
	var stream io.Reader = io.NewSectionReader(checked, 0, size)
	switch {
	case strings.HasSuffix(name, ".gz"):
		gz, err := gzip.NewReader(stream)
		if err != nil {
			return remoteModuleError()
		}
		defer func() { _ = gz.Close() }()
		stream = gz
	case strings.HasSuffix(name, ".bz2"):
		stream = bzip2.NewReader(stream)
	case strings.HasSuffix(name, ".xz"):
		xr, err := (xz.ReaderConfig{DictCap: 64 << 20}).NewReader(stream)
		if err != nil {
			return remoteModuleError()
		}
		stream = xr
	case strings.HasSuffix(name, ".tar"):
	default:
		return remoteModuleError()
	}
	if !strings.Contains(name, ".tar") {
		_, err := io.Copy(io.Discard, archiveReader{ctx: ctx, r: stream, remaining: &inflated})
		if err != nil {
			return remoteModuleError()
		}
		return nil
	}
	return validateRemoteTar(ctx, stream, entries, inflated, &selection)
}

func validateRemoteZip(
	ctx context.Context,
	ra *archiveReaderAt,
	size int64,
	entries int,
	inflated int64,
	selection *moduleSelection,
) error {
	if err := zipDirectoryBound(ra, size, entries); err != nil {
		return err
	}
	// Bound directory parsing too: ZIP headers can lie about their entry count.
	ra.remaining, ra.bounded = remoteZipDirectoryLimit, true
	zr, err := zip.NewReader(ra, size)
	ra.bounded = false
	if err != nil || len(zr.File) > entries {
		return remoteModuleError()
	}
	seen := map[string]bool{}
	links := moduleLinkGraph{}
	for _, f := range zr.File {
		n, e := archive.Clean(f.Name)
		if e != nil || seen[n] || (n == "" && !f.FileInfo().IsDir()) {
			return remoteModuleError()
		}
		seen[n] = true
		selection.add(n, f.FileInfo().IsDir())
		if f.Mode().Type() & ^(fs.ModeDir|fs.ModeSymlink) != 0 {
			return remoteModuleError()
		}
		if f.UncompressedSize64 > uint64(max(inflated, 0)) {
			return remoteModuleError()
		}
		body, e := f.Open()
		if e != nil {
			return remoteModuleError()
		}
		if f.Mode()&fs.ModeSymlink != 0 {
			link, readErr := io.ReadAll(
				io.LimitReader(archiveReader{ctx: ctx, r: body, remaining: &inflated}, 8193),
			)
			e = readErr
			if e == nil {
				e = links.add(n, string(link), true)
			}
		} else {
			_, e = io.Copy(io.Discard, archiveReader{ctx: ctx, r: body, remaining: &inflated})
		}
		closeErr := body.Close()
		if e != nil || closeErr != nil {
			return remoteModuleError()
		}
	}
	if err := links.validate(ctx, seen); err != nil {
		return err
	}
	return ctx.Err() //nolint:wrapcheck // preserve cancellation
}

// Check ZIP/ZIP64's allocation hints before archive/zip allocates its directory.
// The following bounded ReaderAt also limits a directory that understates them.
func zipDirectoryBound(ra io.ReaderAt, size int64, entries int) error {
	if size < 22 {
		return remoteModuleError()
	}
	tail := make([]byte, min(size, 65557))
	if _, err := ra.ReadAt(tail, size-int64(len(tail))); err != nil {
		return remoteModuleError()
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
				return remoteModuleError()
			}
			locator := make([]byte, 20)
			if _, err := ra.ReadAt(
				locator,
				offset,
			); err != nil ||
				binary.LittleEndian.Uint32(locator) != 0x07064b50 {
				return remoteModuleError()
			}
			recordOffset := binary.LittleEndian.Uint64(locator[8:])
			if size < 56 || recordOffset > uint64(size-56) {
				return remoteModuleError()
			}
			record := make([]byte, 56)
			recordPosition := int64(recordOffset) //nolint:gosec // size checked above
			if _, err := ra.ReadAt(record, recordPosition); err != nil ||
				binary.LittleEndian.Uint32(record) != 0x06064b50 {
				return remoteModuleError()
			}
			count = binary.LittleEndian.Uint64(record[32:])
			directory = binary.LittleEndian.Uint64(record[40:])
		}
		if count > uint64(max(entries, 0)) || directory > uint64(remoteZipDirectoryLimit) {
			return remoteModuleError()
		}
		return nil
	}
	return remoteModuleError()
}

func validateRemoteTar(
	ctx context.Context,
	stream io.Reader,
	entries int,
	inflated int64,
	selection *moduleSelection,
) error {
	bounded := &tarTrailerReader{r: archiveReader{ctx: ctx, r: stream, remaining: &inflated}}
	tr := tar.NewReader(bounded)
	seen := map[string]bool{}
	links := moduleLinkGraph{}
	for count := 0; ; count++ {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || count >= entries {
			return remoteModuleError()
		}
		n, err := archive.Clean(h.Name)
		if err != nil || seen[n] || (n == "" && h.Typeflag != tar.TypeDir) {
			return remoteModuleError()
		}
		seen[n] = true
		selection.add(n, h.Typeflag == tar.TypeDir)
		switch h.Typeflag {
		case tar.TypeReg, tar.TypeDir, tar.TypeXGlobalHeader:
		case tar.TypeSymlink, tar.TypeLink:
			if links.add(n, h.Linkname, h.Typeflag == tar.TypeSymlink) != nil {
				return remoteModuleError()
			}
		default:
			return remoteModuleError()
		}

		// tar reads through the shared inflation budget.
		//nolint:gosec
		if _, err = io.Copy(io.Discard, tr); err != nil {
			return remoteModuleError()
		}
	}
	// tar stops at its zero end blocks, before a gzip/bzip2 stream necessarily
	// checks its trailer. Only zero tar padding is permitted after those blocks.
	buf := make([]byte, 32<<10)
	for {
		n, err := bounded.Read(buf)
		if bytes.IndexFunc(buf[:n], func(r rune) bool { return r != 0 }) >= 0 {
			return remoteModuleError()
		}
		if errors.Is(err, io.EOF) {
			if bounded.size < 1024 || bounded.size%512 != 0 ||
				bytes.Count(bounded.tail[:], []byte{0}) != len(bounded.tail) {
				return remoteModuleError()
			}
			return links.validate(ctx, seen)
		}
		if err != nil {
			return remoteModuleError()
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

func (s moduleSource) validateArchive(ctx context.Context, r io.ReadSeeker, size int64) error {
	return validateSelectedArchive(
		ctx,
		archive.ReaderAt(r),
		size,
		"module."+s.Archive,
		remoteArchiveEntries,
		remoteArchiveInflated,
		s.Subdir,
		nil,
	)
}

// A selector must resolve to exactly one real directory, including parents that
// archives omit as explicit entries. The complete source archive stays intact.
type moduleSelection struct {
	pattern string
	matches map[string]bool
}

func (s *moduleSelection) add(name string, directory bool) {
	if s == nil || s.pattern == "" {
		return
	}
	if !directory {
		name = path.Dir(name)
	}
	for name != "." && name != "" {
		if match, _ := path.Match(s.pattern, name); match {
			s.matches[name] = true
		}
		name = path.Dir(name)
	}
}

func moduleLink(name, target string, relative bool) bool {
	if target == "" || strings.HasPrefix(target, "/") ||
		strings.ContainsAny(target, "\\\x00\r\n") ||
		(len(target) > 1 && target[1] == ':') {
		return false
	}
	if relative {
		target = path.Join(path.Dir(name), target)
	} //nolint:gosec // lexical target validated below; nothing is extracted
	_, err := archive.Clean(target)
	return err == nil
}
