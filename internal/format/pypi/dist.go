package pypi

import (
	"bytes"
	"compress/bzip2"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/internal/archive"
	"github.com/weaveplatform/artifactweave/internal/format"
)

// ErrNotDist is content the format cannot index as a distribution: a
// file name of no distribution type, an archive it cannot read, no core
// metadata where its type keeps it, a header block past MaxMetadata, or a
// Name or Version the index cannot hold. Such a file is stored and, unless
// its properties name it, not indexed (decision 0037).
var ErrNotDist = errors.New("pypi: not a distribution the format can index")

// MaxMetadata is the most of a core metadata file the format reads: a
// larger one leaves a wheel indexed without its copy, and a header block
// that does not end within it leaves the file unindexed (decision 0037).
const MaxMetadata = 1 << 20

// limits bound reading a distribution, as npm's bound a tarball: the
// largest sdists hold tens of thousands of files and unpack to gigabytes,
// and a distribution past them is stored but not indexed rather than
// decompressed without end.
var limits = archive.Limits{Entries: 1 << 20, Inflated: 16 << 30, File: MaxMetadata}

// Type is a distribution's type, as its file name tells it.
type Type int

// Distribution types.
const (
	// Wheel is a built distribution, .whl; its core metadata is METADATA
	// in its .dist-info folder.
	Wheel Type = iota + 1
	// Sdist is a source distribution, .tar.gz, .tgz, .tar.bz2, .tar or
	// .zip; its core metadata is PKG-INFO.
	Sdist
	// Egg is a legacy built distribution, .egg; its core metadata is
	// EGG-INFO/PKG-INFO.
	Egg
)

// extensions are the file name endings of the types 7.x extracts metadata
// from (pypi-repositories, "Automatic extraction of properties").
var extensions = []struct {
	ext string
	t   Type
}{
	{".whl", Wheel},
	{".egg", Egg},
	{".tar.gz", Sdist},
	{".tgz", Sdist},
	{".tar.bz2", Sdist},
	{".tar", Sdist},
	{".zip", Sdist},
}

// typeOf is the type of the file name, with its extension; ok is false
// for a name of no distribution type, or one that is only an extension.
// The extensions are matched as the documentation writes them. [I]
// Whether 7.x reads one in upper case, such as .WHL, is unknown (#216).
func typeOf(name string) (t Type, ext string, ok bool) {
	for _, e := range extensions {
		if strings.HasSuffix(name, e.ext) && len(name) > len(e.ext) {
			return e.t, e.ext, true
		}
	}
	return 0, "", false
}

// IsDistribution reports whether the file at p has the name of a
// distribution type, the files the format reads and the enforcement
// settings check.
func IsDistribution(p string) bool {
	_, _, ok := typeOf(path.Base(p))
	return ok
}

// Dist is what the format reads from a distribution: its core metadata.
type Dist struct {
	// Name and Version are the metadata's as written: a valid project name
	// and a version the index can hold.
	Name, Version string
	// Summary and RequiresPython are the metadata's, or empty.
	Summary, RequiresPython string
	// Metadata is the core metadata file as the index keeps it: the whole
	// file when it is at most MaxMetadata bytes, else its header block,
	// with any byte that is not UTF-8 replaced so it can be stored as text.
	Metadata string
	// Core reports that Metadata is a wheel's METADATA byte for byte,
	// which PEP 658 serves (decision 0037): an sdist's PKG-INFO may list
	// fields as Dynamic, which installing then fills.
	Core bool
}

// ReadDist reads the distribution named name in ra, of size bytes: its
// core metadata, from where its type keeps it. ErrNotDist when the format
// cannot index it; any other error is a failure to read the content.
func ReadDist(ra io.ReaderAt, size int64, name string) (*Dist, error) {
	t, ext, ok := typeOf(name)
	if !ok {
		return nil, fmt.Errorf("%w: %s is of no distribution type", ErrNotDist, name)
	}
	src := &firstErr{ra: ra}
	text, whole, err := dist{src: src, size: size, name: name}.readMetadata(t, ext)
	switch {
	case src.err != nil:
		return nil, fmt.Errorf("pypi: read %s: %w", name, src.err)
	case err != nil:
		return nil, fmt.Errorf("%w: %w", ErrNotDist, err)
	}
	return parseDist(text, whole, t)
}

// ReadStored reads the stored content sha as the distribution named name;
// ok is false when the format cannot index it. The upload API reads what
// it stored with it, to find the canonical path.
func ReadStored(ctx context.Context, blobs format.Blobs, sha, name string) (*Dist, bool, error) {
	rc, info, err := blobs.Open(ctx, sha)
	if err != nil {
		return nil, false, fmt.Errorf("pypi: open %s: %w", sha, err)
	}
	defer func() { _ = rc.Close() }()
	d, err := ReadDist(archive.ReaderAt(rc), info.Size, name)
	switch {
	case errors.Is(err, ErrNotDist):
		return nil, false, nil
	case err != nil:
		return nil, false, err
	}
	return d, true, nil
}

// firstErr remembers the first read error that is not the end of the
// content. Without it, a blob store failing mid-read would look like
// content that is no distribution, and the file would silently go
// unindexed.
type firstErr struct {
	ra  io.ReaderAt
	err error
}

func (r *firstErr) ReadAt(p []byte, off int64) (int, error) {
	n, err := r.ra.ReadAt(p, off)
	if err != nil && !errors.Is(err, io.EOF) && r.err == nil {
		r.err = err
	}
	return n, err //nolint:wrapcheck // io.ReaderAt contract
}

// errNoMetadata is a distribution without core metadata where its type
// keeps it.
var errNoMetadata = errors.New("no core metadata where its type keeps it")

// errFound ends a walk once it has what it reads.
var errFound = errors.New("found")

// dist is a distribution being read: its content, its size and its file
// name.
type dist struct {
	src  io.ReaderAt
	size int64
	name string
}

// readMetadata reads the core metadata file of the distribution, of type
// t with extension ext: at most MaxMetadata bytes of it, and whether that
// is all of it.
func (d dist) readMetadata(t Type, ext string) ([]byte, bool, error) {
	if ext == ".tar.bz2" {
		head := make([]byte, 3)
		if n, _ := d.src.ReadAt(head, 0); n < len(head) || string(head) != "BZh" {
			return nil, false, fmt.Errorf("%w: not bzip2", archive.ErrUnusable)
		}
		s := &sdistFile{}
		tr := bzip2.NewReader(io.NewSectionReader(d.src, 0, d.size))
		return s.result(archive.ScanTar(tr, limits, s.visit))
	}
	kind := archive.KindOf(d.src, d.name)
	if kind == archive.None || t != Sdist && kind != archive.Zip {
		return nil, false, fmt.Errorf("%w: not a %s archive", archive.ErrUnusable, ext)
	}
	switch t {
	case Wheel:
		return d.readWheel()
	case Egg:
		return d.readEgg()
	}
	s := &sdistFile{}
	return s.result(archive.Scan(d.src, d.size, kind, limits, s.visit))
}

// readEgg reads an egg's EGG-INFO/PKG-INFO.
func (d dist) readEgg() ([]byte, bool, error) {
	var text []byte
	var whole bool
	err := archive.Scan(d.src, d.size, archive.Zip, limits, func(e archive.Entry) error {
		n, err := archive.Clean(e.Name)
		if err != nil || !e.Regular || n != "EGG-INFO/PKG-INFO" {
			return nil //nolint:nilerr // an unusable entry is just not the one
		}
		if text, whole, err = readEntry(e); err != nil {
			return err
		}
		return errFound
	})
	switch {
	case errors.Is(err, errFound):
		return text, whole, nil
	case err != nil:
		return nil, false, err //nolint:wrapcheck // archive names its own
	}
	return nil, false, errNoMetadata
}

// readWheel reads METADATA from the wheel's top-level .dist-info folder:
// the only one, or, when there are several, the one named for the
// distribution and version the file name gives (decision 0037). A zip's
// entries can be opened after the walk, so the walk only lists them.
func (d dist) readWheel() ([]byte, bool, error) {
	var found []archive.Entry
	var folders []string
	err := archive.Scan(d.src, d.size, archive.Zip, limits, func(e archive.Entry) error {
		n, err := archive.Clean(e.Name)
		dir, file := path.Split(n)
		dir = strings.TrimSuffix(dir, "/")
		if err != nil || !e.Regular || file != "METADATA" || strings.Contains(dir, "/") ||
			!strings.HasSuffix(dir, ".dist-info") {
			return nil //nolint:nilerr // an unusable entry is just not the one
		}
		found = append(found, e)
		folders = append(folders, strings.TrimSuffix(dir, ".dist-info"))
		return nil
	})
	if err != nil {
		return nil, false, err //nolint:wrapcheck // archive names its own
	}
	if len(found) == 1 {
		return readEntry(found[0])
	}
	stem := strings.TrimSuffix(path.Base(d.name), ".whl")
	fields := strings.Split(stem, "-")
	for i, f := range folders {
		dist, version, ok := strings.Cut(f, "-")
		if ok && len(fields) > 1 && Normalize(dist) == Normalize(fields[0]) &&
			NormalizeVersion(version) == NormalizeVersion(fields[1]) {
			return readEntry(found[i])
		}
	}
	return nil, false, errNoMetadata
}

// sdistFile finds an sdist's PKG-INFO in one walk: at the root, else in
// the single top-level folder, the root preferred, as 7.x reads it at
// depth 0 or 1 (RTFACT-17032, RTFACT-16868). A tar entry can be read only
// while the walk is on it, so the folder's is read when it is passed.
type sdistFile struct {
	root, folder []byte
	rootWhole    bool
	folderWhole  bool
	// folders are the distinct top-level folders, of which the first two
	// are all the rule needs.
	folders []string
}

func (s *sdistFile) visit(e archive.Entry) error {
	n, err := archive.Clean(e.Name)
	if err != nil || n == "" {
		return nil //nolint:nilerr // an unusable entry is just not the one
	}
	top, rest, nested := strings.Cut(n, "/")
	if (nested || e.Dir) && len(s.folders) < 2 && !slices.Contains(s.folders, top) {
		s.folders = append(s.folders, top)
	}
	if !e.Regular {
		return nil
	}
	switch {
	case n == "PKG-INFO":
		if s.root, s.rootWhole, err = readEntry(e); err != nil {
			return err
		}
		return errFound
	case nested && rest == "PKG-INFO" && s.folder == nil:
		s.folder, s.folderWhole, err = readEntry(e)
		return err
	}
	return nil
}

// result is the PKG-INFO the walk that ended with err found.
func (s *sdistFile) result(err error) ([]byte, bool, error) {
	switch {
	case errors.Is(err, errFound):
		return s.root, s.rootWhole, nil
	case err != nil:
		return nil, false, err //nolint:wrapcheck // archive names its own
	case s.folder != nil && len(s.folders) == 1:
		return s.folder, s.folderWhole, nil
	}
	return nil, false, errNoMetadata
}

// readEntry reads at most MaxMetadata bytes of e, and whether that is all
// of it.
func readEntry(e archive.Entry) ([]byte, bool, error) {
	r, err := e.Open()
	if err != nil {
		return nil, false, fmt.Errorf("%w: %s: %w", archive.ErrUnusable, e.Name, err)
	}
	if c, ok := r.(io.Closer); ok {
		defer func() { _ = c.Close() }()
	}
	b, err := io.ReadAll(io.LimitReader(r, MaxMetadata+1))
	if err != nil {
		return nil, false, fmt.Errorf("%w: %s: %w", archive.ErrUnusable, e.Name, err)
	}
	if len(b) > MaxMetadata {
		return b[:MaxMetadata], false, nil
	}
	return b, true, nil
}

// utf8BOM starts some core metadata files; Python's reader skips it.
var utf8BOM = []byte("\xef\xbb\xbf")

// parseDist reads a distribution of type t from its core metadata file,
// text, which is the whole file when whole and its first MaxMetadata
// bytes otherwise.
func parseDist(text []byte, whole bool, t Type) (*Dist, error) {
	f, block, ok := parseHeader(string(bytes.TrimPrefix(text, utf8BOM)), whole)
	if !ok {
		return nil, fmt.Errorf("%w: the header block passes %d bytes", ErrNotDist, MaxMetadata)
	}
	d := &Dist{
		Name:           strings.TrimSpace(f.Get("Name")),
		Version:        strings.TrimSpace(f.Get("Version")),
		Summary:        strings.TrimSpace(f.Get("Summary")),
		RequiresPython: strings.TrimSpace(f.Get("Requires-Python")),
	}
	if !ValidName(d.Name) || !validVersion(d.Version) {
		return nil, fmt.Errorf("%w: the metadata names no valid project and version", ErrNotDist)
	}
	d.Metadata = strings.ToValidUTF8(string(text), "�")
	if !whole {
		d.Metadata = strings.ToValidUTF8(block, "�")
	}
	d.Core = t == Wheel && whole && utf8.Valid(text)
	return d, nil
}
