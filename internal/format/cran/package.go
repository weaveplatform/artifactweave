package cran

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/ulikunitz/xz"
)

const (
	maxDescription = 256 << 10
	maxUnpacked    = 1 << 30
	maxEntries     = 50000
)

var ErrPackage = errors.New("invalid CRAN package")

type Package struct {
	Name, Version, Path, Feed string
	Fields                    map[string]string
	Size                      int64
	MD5, SHA256               string
}

// ReadPackage checks the entire archive, including trailing CRCs, before its
// DESCRIPTION is allowed to enter an index.
func ReadPackage(ctx context.Context, r io.ReadSeeker, size int64, p string) (Package, error) {
	feed, name, version, ok := PackagePath(p)
	if !ok || size <= 0 {
		return Package{}, ErrPackage
	}
	pkg := Package{Name: name, Version: version, Path: p, Feed: feed, Size: size}
	var fields map[string]string
	var err error
	if strings.HasSuffix(p, ".zip") {
		fields, err = readZip(ctx, r, size, name)
	} else {
		fields, err = readTar(ctx, r, p, name)
	}
	if err != nil || fields["Package"] != name || fields["Version"] != version {
		return Package{}, ErrPackage
	}
	pkg.Fields = fields
	return pkg, nil
}

// ReadUploaded infers the package identity from an archive whose POST URL
// supplies the feed but has no filename. The content still goes through the
// same complete archive validator as a direct path upload.
func ReadUploaded(
	ctx context.Context,
	r io.ReadSeeker,
	size int64,
	feed, suffix string,
) (Package, error) {
	if _, ok := Feed(feed); !ok || size <= 0 ||
		feed == "src/contrib" && suffix != ".tar.gz" ||
		feed != "src/contrib" && suffix != ".zip" && suffix != ".tgz" && suffix != ".tar.gz" {
		return Package{}, ErrPackage
	}
	root, err := archiveRoot(r, size, suffix)
	if err != nil || !ValidName(root) {
		return Package{}, ErrPackage
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return Package{}, fmt.Errorf("cran: rewind upload: %w", err)
	}
	var fields map[string]string
	if suffix == ".zip" {
		fields, err = readZip(ctx, r, size, root)
	} else {
		fields, err = readTar(ctx, r, "upload"+suffix, root)
	}
	if err != nil || fields["Package"] != root || !ValidVersion(fields["Version"]) {
		return Package{}, ErrPackage
	}
	p := feed + "/" + root + "_" + fields["Version"] + suffix
	if _, _, _, ok := PackagePath(p); !ok {
		return Package{}, ErrPackage
	}
	return Package{
		Name: root, Version: fields["Version"], Path: p, Feed: feed,
		Fields: fields, Size: size,
	}, nil
}

func archiveRoot(raw io.ReadSeeker, size int64, suffix string) (string, error) {
	var name string
	if suffix == ".zip" {
		zr, err := zip.NewReader(seekReaderAt{raw}, size)
		if err != nil || len(zr.File) == 0 {
			return "", ErrPackage
		}
		name = zr.File[0].Name
	} else {
		gz, err := gzip.NewReader(raw)
		if err != nil {
			return "", ErrPackage
		}
		defer func() { _ = gz.Close() }()
		h, err := tar.NewReader(gz).Next()
		if err != nil {
			return "", ErrPackage
		}
		name = h.Name
	}
	root, _, _ := strings.Cut(name, "/")
	if !ValidName(root) {
		return "", ErrPackage
	}
	return root, nil
}

func readTar(ctx context.Context, raw io.Reader, p, name string) (map[string]string, error) {
	var stream io.Reader
	switch {
	case strings.HasSuffix(p, ".tar.gz"), strings.HasSuffix(p, ".tgz"):
		gz, err := gzip.NewReader(raw)
		if err != nil {
			return nil, ErrPackage
		}
		defer func() { _ = gz.Close() }()
		stream = gz
	case strings.HasSuffix(p, ".tar.bz2"):
		stream = bzip2.NewReader(raw)
	case strings.HasSuffix(p, ".tar.xz"):
		xr, err := xz.NewReader(raw)
		if err != nil {
			return nil, ErrPackage
		}
		stream = xr
	default:
		return nil, ErrPackage
	}
	limited := &io.LimitedReader{R: stream, N: maxUnpacked + 1}
	tr := tar.NewReader(limited)
	var fields map[string]string
	count := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("cran: tar validation canceled: %w", err)
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || limited.N <= 0 || count >= maxEntries || h.Size < 0 {
			return nil, ErrPackage
		}
		count++
		if !safeEntry(h.Name, name) ||
			h.Typeflag != tar.TypeReg && h.Typeflag != 0 && h.Typeflag != tar.TypeDir {
			return nil, ErrPackage
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if strings.TrimSuffix(h.Name, "/") == name+"/DESCRIPTION" {
			if fields != nil || h.Size <= 0 || h.Size > maxDescription {
				return nil, ErrPackage
			}
			body, err := io.ReadAll(io.LimitReader(tr, maxDescription+1))
			if err != nil || int64(len(body)) != h.Size {
				return nil, ErrPackage
			}
			fields, err = parseFields(body)
			if err != nil {
				return nil, err
			}
		}
	}
	// tar.Reader stops at its terminator. Drain the compressor so its checksum
	// and trailing stream are checked before the metadata is published.
	if _, err := io.Copy(io.Discard, limited); err != nil || limited.N <= 0 || fields == nil {
		return nil, ErrPackage
	}
	return fields, nil
}

func readZip(
	ctx context.Context,
	raw io.ReadSeeker,
	size int64,
	name string,
) (map[string]string, error) {
	zr, err := zip.NewReader(seekReaderAt{raw}, size)
	if err != nil || len(zr.File) == 0 || len(zr.File) > maxEntries {
		return nil, ErrPackage
	}
	var fields map[string]string
	var total int64
	for _, file := range zr.File {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("cran: zip validation canceled: %w", err)
		}
		if !safeEntry(file.Name, name) ||
			!file.Mode().IsRegular() && !file.FileInfo().IsDir() ||
			file.UncompressedSize64 > maxUnpacked ||
			total+int64(file.UncompressedSize64) > maxUnpacked {
			return nil, ErrPackage
		}
		total += int64(file.UncompressedSize64)
		if file.FileInfo().IsDir() {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return nil, ErrPackage
		}
		if file.Name == name+"/DESCRIPTION" {
			if fields != nil || file.UncompressedSize64 == 0 ||
				file.UncompressedSize64 > maxDescription {
				_ = rc.Close()
				return nil, ErrPackage
			}
			body, readErr := io.ReadAll(io.LimitReader(rc, maxDescription+1))
			if readErr == nil {
				fields, readErr = parseFields(body)
			}
			err = errors.Join(readErr, rc.Close())
		} else {
			// The central directory size is untrusted, so bound inflation as
			// well as checking that ZIP's CRC/trailer is consumed.
			var n int64
			var readErr error
			n, readErr = io.Copy(io.Discard, io.LimitReader(rc, int64(file.UncompressedSize64)+1))
			if n != int64(file.UncompressedSize64) {
				readErr = ErrPackage
			}
			err = errors.Join(readErr, rc.Close())
		}
		if err != nil {
			return nil, ErrPackage
		}
	}
	if fields == nil {
		return nil, ErrPackage
	}
	return fields, nil
}

type seekReaderAt struct{ io.ReadSeeker }

func (r seekReaderAt) ReadAt(p []byte, offset int64) (int, error) {
	if _, err := r.Seek(offset, io.SeekStart); err != nil {
		return 0, fmt.Errorf("cran: seek zip archive: %w", err)
	}
	n, err := io.ReadFull(r, p)
	if err != nil {
		return n, fmt.Errorf("cran: read zip archive: %w", err)
	}
	return n, nil
}

func safeEntry(p, name string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") ||
		strings.ContainsAny(p, "\x00\r\n") || path.Clean(p) != strings.TrimSuffix(p, "/") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || part == "." {
			return false
		}
	}
	return p == name || strings.HasPrefix(p, name+"/")
}

func parseFields(body []byte) (map[string]string, error) {
	if len(body) == 0 || len(body) > maxDescription || strings.ContainsRune(string(body), 0) {
		return nil, ErrPackage
	}
	fields := map[string]string{}
	var last string
	for _, raw := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") {
		if raw == "" {
			continue
		}
		if raw[0] == ' ' || raw[0] == '\t' {
			if last == "" {
				return nil, ErrPackage
			}
			fields[last] += " " + strings.TrimSpace(raw)
			continue
		}
		key, value, ok := strings.Cut(raw, ":")
		_, exists := fields[key]
		if !ok || key == "" || len(key) > 64 || exists {
			return nil, ErrPackage
		}
		if !validFieldKey(key) {
			return nil, ErrPackage
		}
		value = strings.TrimSpace(value)
		if len(value) > 4096 || strings.ContainsAny(value, "\r\x00") {
			return nil, ErrPackage
		}
		fields[key], last = value, key
	}
	if !ValidName(fields["Package"]) || !ValidVersion(fields["Version"]) {
		return nil, fmt.Errorf("%w: missing package identity", ErrPackage)
	}
	return fields, nil
}

func validFieldKey(key string) bool {
	for i, ch := range key {
		letter := ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z'
		digit := ch >= '0' && ch <= '9'
		if i == 0 && !letter || i > 0 && !letter && !digit &&
			ch != '.' && ch != '/' && ch != '_' && ch != '-' && ch != '@' {
			return false
		}
	}
	return true
}
