// Package archive reads zip, tar and tar.gz archives: the walker the item
// service explodes and serves archive entries with, and a bounded walker
// and reader the format handlers (decision 0032) read packages with, such
// as an npm tarball's package.json and file count, a Go module zip's go.mod
// or a jar's POM. Every count an archive's headers give is the uploader's
// claim, so the bounded walker counts what it actually decompresses.
package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// ErrUnusable is content that is not a usable archive: an unknown or
// corrupt format, an entry escaping its folder, or a limit exceeded.
var ErrUnusable = errors.New("archive: unusable archive")

// ErrNotFound is an archive with no entry the caller asked for.
var ErrNotFound = errors.New("archive: no such entry")

// errFound ends a walk once ReadFile has its file.
var errFound = errors.New("archive: found")

// Kind is an archive container format.
type Kind int

// Archive kinds; None is a file none of them.
const (
	None Kind = iota
	Zip
	Tar
	TarGz
)

// ParseKind reads an archiveType parameter (zip, tar, tar.gz, tgz).
func ParseKind(s string) (Kind, bool) {
	switch strings.ToLower(s) {
	case "zip":
		return Zip, true
	case "tar":
		return Tar, true
	case "tar.gz", "tgz":
		return TarGz, true
	}
	return None, false
}

// ContentType is the media type of an archive of the kind.
func (k Kind) ContentType() string {
	switch k {
	case Zip:
		return "application/zip"
	case Tar:
		return "application/x-tar"
	default:
		return "application/x-gzip"
	}
}

// Ext is the file extension of an archive of the kind.
func (k Kind) Ext() string {
	switch k {
	case Zip:
		return ".zip"
	case Tar:
		return ".tar"
	default:
		return ".tar.gz"
	}
}

// SniffLen is how much of the content Sniff reads: a tar header's magic
// ends at byte 262.
const SniffLen = 512

// Sniff tells the kind from the content's first bytes, falling back to the
// name for what has no signature to read.
func Sniff(head []byte, name string) Kind {
	switch {
	case bytes.HasPrefix(head, []byte("PK\x03\x04")), bytes.HasPrefix(head, []byte("PK\x05\x06")):
		return Zip
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}):
		return TarGz
	case len(head) >= 262 && string(head[257:262]) == "ustar":
		return Tar
	}
	lower := strings.ToLower(name)
	for _, ext := range []string{".zip", ".jar", ".war", ".ear", ".aar", ".nupkg", ".whl"} {
		if strings.HasSuffix(lower, ext) {
			return Zip
		}
	}
	switch {
	case strings.HasSuffix(lower, ".tar"):
		return Tar
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return TarGz
	}
	return None
}

// KindOf sniffs the kind of the content in ra, named name.
func KindOf(ra io.ReaderAt, name string) Kind {
	head := make([]byte, SniffLen)
	n, _ := ra.ReadAt(head, 0)
	return Sniff(head[:n], name)
}

// Entry is one entry as an archive lists it.
type Entry struct {
	// Name is the entry's name as the archive spells it; Clean makes it a
	// path.
	Name string
	Dir  bool
	// Regular is a plain file; links and devices are not.
	Regular bool
	// Size is what the archive's header claims.
	Size int64
	// Open reads the entry. A tar entry's reader is valid only until the
	// walk moves on.
	Open func() (io.Reader, error)
}

// Walk calls fn for every entry of the archive in ra, in archive order,
// until fn returns an error, which Walk returns.
func Walk(ra io.ReaderAt, size int64, kind Kind, fn func(Entry) error) error {
	return walk(ra, size, kind, nil, fn)
}

// walk is Walk with wrap, when not nil, wrapping the tar stream as it is
// decompressed, so a caller can count the bytes a walk inflates.
func walk(
	ra io.ReaderAt,
	size int64,
	kind Kind,
	wrap func(io.Reader) io.Reader,
	fn func(Entry) error,
) error {
	switch kind {
	case Zip:
		zr, err := zip.NewReader(ra, size)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrUnusable, err)
		}
		for _, f := range zr.File {
			e := Entry{
				Name: f.Name, Dir: f.FileInfo().IsDir(), Regular: f.Mode().IsRegular(),
				Size: int64(min(f.UncompressedSize64, 1<<62)), //nolint:gosec // capped
				Open: func() (io.Reader, error) { return f.Open() },
			}
			if err := fn(e); err != nil {
				return err
			}
		}
		return nil
	case Tar, TarGz:
		var r io.Reader = io.NewSectionReader(ra, 0, size)
		if kind == TarGz {
			gz, err := gzip.NewReader(r)
			if err != nil {
				return fmt.Errorf("%w: %w", ErrUnusable, err)
			}
			defer func() { _ = gz.Close() }()
			r = gz
		}
		if wrap != nil {
			r = wrap(r)
		}
		return walkTar(r, fn)
	}
	return fmt.Errorf("%w: not a zip, tar or tar.gz archive", ErrUnusable)
}

// walkTar calls fn for every entry of the tar stream r.
func walkTar(r io.Reader, fn func(Entry) error) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: %w", ErrUnusable, err)
		}
		e := Entry{
			Name: h.Name, Dir: h.Typeflag == tar.TypeDir,
			Regular: h.Typeflag == tar.TypeReg, Size: h.Size,
			Open: func() (io.Reader, error) { return tr, nil },
		}
		if err := fn(e); err != nil {
			return err
		}
	}
}

// Clean makes an entry name a relative path, refusing names that would
// leave the folder the archive is read into: absolute paths, drive letters
// and ".." segments. Backslashes count as separators, as Windows tools
// write them. A name that is only the folder itself cleans to "".
func Clean(name string) (string, error) {
	n := strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(n, "/") || (len(n) > 1 && n[1] == ':') {
		return "", fmt.Errorf("%w: the entry %q is an absolute path", ErrUnusable, name)
	}
	for _, seg := range strings.Split(n, "/") {
		if seg == ".." {
			return "", fmt.Errorf(
				"%w: the entry %q leaves the archive's folder",
				ErrUnusable,
				name,
			)
		}
	}
	n = strings.Trim(path.Clean("/"+n), "/")
	if n == "" || n == "." {
		return "", nil
	}
	if _, _, _, err := types.SplitItemPath(n); err != nil {
		return "", fmt.Errorf("%w: the entry %q is not a valid path", ErrUnusable, name)
	}
	return n, nil
}

// ReaderAt gives random access over a reader that can only seek, such as
// stored content, for zip, which reads its central directory from the end.
// Reads are serialised because they share one seek position. A reader that
// already reads at offsets is returned as it is.
func ReaderAt(rs io.ReadSeeker) io.ReaderAt {
	if ra, ok := rs.(io.ReaderAt); ok {
		return ra
	}
	return &readerAt{rs: rs}
}

type readerAt struct {
	mu sync.Mutex
	rs io.ReadSeeker
}

func (r *readerAt) ReadAt(p []byte, off int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.rs.Seek(off, io.SeekStart); err != nil {
		return 0, err //nolint:wrapcheck // io.ReaderAt contract
	}
	return io.ReadFull(r.rs, p) //nolint:wrapcheck // io.ReaderAt contract
}

// Limits bound what reading one file from an archive may cost.
type Limits struct {
	// Entries is how many entries the walk may pass.
	Entries int
	// Inflated bounds the bytes a tar.gz walk decompresses, including the
	// entries it skips on the way, which a zip walk does not read.
	Inflated int64
	// File bounds the content of the file read.
	File int64
}

// DefaultLimits suit the descriptors formats read: a package.json, a
// go.mod or a POM is kilobytes, and a package whose descriptor lies past a
// thousand entries or 256 MiB of content is refused rather than read.
var DefaultLimits = Limits{Entries: 1000, Inflated: 256 << 20, File: 1 << 20}

// Scan is Walk within lim's Entries and Inflated: ErrUnusable once the walk
// passes either, so a format can read a whole package, such as an npm
// tarball it counts the files of, without trusting its headers. lim.File
// is ReadEntry's.
func Scan(ra io.ReaderAt, size int64, kind Kind, lim Limits, fn func(Entry) error) error {
	budget := &countingReader{left: lim.Inflated}
	return walk(ra, size, kind, budget.wrap, counted(lim, fn))
}

// ScanTar is Scan over a tar stream the caller decompressed, for a
// compression the sniffer does not tell, such as a PyPI sdist's tar.bz2:
// lim's Inflated counts the bytes r yields, so a bomb in that compression
// stops at the budget too.
func ScanTar(r io.Reader, lim Limits, fn func(Entry) error) error {
	budget := &countingReader{left: lim.Inflated}
	return walkTar(budget.wrap(r), counted(lim, fn))
}

// counted is fn failing with ErrUnusable once a walk passes lim's Entries.
func counted(lim Limits, fn func(Entry) error) func(Entry) error {
	entries := 0
	return func(e Entry) error {
		entries++
		if entries > lim.Entries {
			return fmt.Errorf("%w: more than %d entries", ErrUnusable, lim.Entries)
		}
		return fn(e)
	}
}

// ReadEntry reads e whole, refusing content past lim.File with ErrUnusable.
func ReadEntry(e Entry, lim Limits) ([]byte, error) {
	r, err := e.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrUnusable, e.Name, err)
	}
	if c, ok := r.(io.Closer); ok {
		defer func() { _ = c.Close() }()
	}
	b, err := io.ReadAll(io.LimitReader(r, lim.File+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrUnusable, e.Name, err)
	}
	if int64(len(b)) > lim.File {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes", ErrUnusable, e.Name, lim.File)
	}
	return b, nil
}

// ReadFile returns the first regular file of the archive in ra whose
// cleaned name want accepts, with that name. ErrNotFound when no file
// matches; ErrUnusable when the content is no archive of kind or a limit is
// passed first.
func ReadFile(
	ra io.ReaderAt,
	size int64,
	kind Kind,
	lim Limits,
	want func(name string) bool,
) (string, []byte, error) {
	var (
		name string
		body []byte
	)
	err := Scan(ra, size, kind, lim, func(e Entry) error {
		n, err := Clean(e.Name)
		if err != nil || n == "" || !e.Regular || !want(n) {
			return nil //nolint:nilerr // an unusable entry is just not the one
		}
		if body, err = ReadEntry(e, lim); err != nil {
			return err
		}
		name = n
		return errFound
	})
	switch {
	case errors.Is(err, errFound):
		return name, body, nil
	case err != nil:
		return "", nil, err
	}
	return "", nil, ErrNotFound
}

// countingReader fails a read once more than left bytes came through it,
// so a walk past a gzip bomb stops at the budget.
type countingReader struct {
	r    io.Reader
	left int64
}

func (c *countingReader) wrap(r io.Reader) io.Reader {
	c.r = r
	return c
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if c.left < 0 {
		return n, fmt.Errorf("%w: it inflates past the read budget", ErrUnusable)
	}
	return n, err //nolint:wrapcheck // a pass-through reader
}
