package conda

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"
)

const (
	maxCompressedPackage = int64(2 << 30)
	maxExpandedPackage   = int64(4 << 30)
	maxIndexJSON         = int64(1 << 20)
	maxTarEntries        = 100000
)

var ErrInvalidPackage = errors.New("conda: invalid package")

// Record is the subset of info/index.json retained in the channel index.
// Checksums, size, filename and subdirectory are supplied from trusted stored
// content and path, never from fields embedded in a package.
type Record struct {
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	Build         string   `json:"build"`
	BuildNumber   int      `json:"build_number"`
	Depends       []string `json:"depends,omitempty"`
	Constrains    []string `json:"constrains,omitempty"`
	Noarch        string   `json:"noarch,omitempty"`
	Subdir        string   `json:"subdir,omitempty"`
	Arch          string   `json:"arch,omitempty"`
	Platform      string   `json:"platform,omitempty"`
	License       string   `json:"license,omitempty"`
	LicenseFamily string   `json:"license_family,omitempty"`
	Features      string   `json:"features,omitempty"`
	TrackFeatures string   `json:"track_features,omitempty"`
	Timestamp     int64    `json:"timestamp,omitempty"`
	Size          int64    `json:"size"`
	SHA256        string   `json:"sha256"`
	MD5           string   `json:"md5,omitempty"`
}

func safeArchivePath(p string) bool {
	return p != "" && len(p) <= maxPathLength && !strings.HasPrefix(p, "/") &&
		!strings.ContainsAny(p, "\\\x00\r\n") && path.Clean(p) == p &&
		p != "." && p != ".." && !strings.HasPrefix(p, "../")
}

func validText(s string, limit int) bool {
	if len(s) > limit || strings.ContainsAny(s, "\x00\r\n") {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func parsePackageIndex(data []byte, id PackagePath) (Record, error) {
	if len(data) == 0 || int64(len(data)) > maxIndexJSON {
		return Record{}, fmt.Errorf("%w: info/index.json size", ErrInvalidPackage)
	}
	var out Record
	if err := json.Unmarshal(data, &out); err != nil {
		return Record{}, fmt.Errorf("%w: info/index.json: %w", ErrInvalidPackage, err)
	}
	if out.Name != id.Name || out.Version != id.Version || out.Build != id.Build ||
		out.BuildNumber < 0 || out.BuildNumber > 1_000_000 ||
		out.Subdir != "" && out.Subdir != id.Subdir ||
		id.Subdir != "noarch" && out.Noarch != "" ||
		out.Noarch != "" && out.Noarch != "generic" && out.Noarch != "python" ||
		len(out.Depends) > 512 || len(out.Constrains) > 512 {
		return Record{}, fmt.Errorf("%w: index identity", ErrInvalidPackage)
	}
	for _, s := range append(out.Depends, out.Constrains...) {
		if !validText(s, 512) || s == "" {
			return Record{}, fmt.Errorf("%w: dependency metadata", ErrInvalidPackage)
		}
	}
	for _, s := range []string{
		out.License, out.LicenseFamily, out.Features, out.TrackFeatures,
		out.Arch, out.Platform,
	} {
		if !validText(s, 512) {
			return Record{}, fmt.Errorf("%w: string metadata", ErrInvalidPackage)
		}
	}
	out.Subdir = id.Subdir
	out.SHA256, out.MD5, out.Size = "", "", 0
	return out, nil
}

// ReadPackage validates one complete legacy or modern Conda archive and
// extracts only its bounded info/index.json. It does not trust package metadata
// to choose the repository path or checksums.
func ReadPackage(
	ctx context.Context,
	body io.ReadSeeker,
	size int64,
	id PackagePath,
) (Record, error) {
	if size <= 0 || size > maxCompressedPackage || id.Path == "" {
		return Record{}, fmt.Errorf("%w: archive size or path", ErrInvalidPackage)
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return Record{}, fmt.Errorf("conda: seek package: %w", err)
	}
	if id.Extension == ".tar.bz2" {
		return readTar(ctx, bzip2.NewReader(io.LimitReader(body, size)), id)
	}
	if id.Extension != ".conda" {
		return Record{}, fmt.Errorf("%w: package extension", ErrInvalidPackage)
	}
	zr, err := zip.NewReader(&lockedReaderAt{body: body}, size)
	if err != nil || len(zr.File) < 2 || len(zr.File) > 4 {
		return Record{}, fmt.Errorf("%w: outer ZIP", ErrInvalidPackage)
	}
	var info, pkg *zip.File
	for _, f := range zr.File {
		if !safeArchivePath(f.Name) || f.UncompressedSize64 > uint64(maxCompressedPackage) ||
			f.CompressedSize64 > uint64(maxCompressedPackage) || f.Mode().IsDir() {
			return Record{}, fmt.Errorf("%w: ZIP entry", ErrInvalidPackage)
		}
		switch {
		case strings.HasPrefix(f.Name, "info-") && strings.HasSuffix(f.Name, ".tar.zst"):
			if info != nil {
				return Record{}, fmt.Errorf("%w: duplicate info archive", ErrInvalidPackage)
			}
			info = f
		case strings.HasPrefix(f.Name, "pkg-") && strings.HasSuffix(f.Name, ".tar.zst"):
			if pkg != nil {
				return Record{}, fmt.Errorf("%w: duplicate pkg archive", ErrInvalidPackage)
			}
			pkg = f
		case f.Name == "metadata.json":
			if err := checkCondaMetadata(f); err != nil {
				return Record{}, err
			}
		default:
			return Record{}, fmt.Errorf("%w: ZIP entry name", ErrInvalidPackage)
		}
	}
	if info == nil || pkg == nil {
		return Record{}, fmt.Errorf("%w: missing archive member", ErrInvalidPackage)
	}
	record, err := readZstdTar(ctx, info, id)
	if err != nil {
		return Record{}, err
	}
	// The payload is checked for bounded, safe archive structure as well.
	if _, err := readZstdTar(ctx, pkg, PackagePath{}); err != nil {
		return Record{}, err
	}
	return record, nil
}

type lockedReaderAt struct {
	mu   sync.Mutex
	body io.ReadSeeker
}

func (r *lockedReaderAt) ReadAt(p []byte, off int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.body.Seek(off, io.SeekStart); err != nil {
		return 0, err //nolint:wrapcheck // ReaderAt must preserve io.Reader error semantics for archive/zip.
	}
	return io.ReadFull(r.body, p) //nolint:wrapcheck // Preserve io.EOF for archive/zip.
}

func checkCondaMetadata(f *zip.File) error {
	if f.UncompressedSize64 > 4096 {
		return fmt.Errorf("%w: metadata.json size", ErrInvalidPackage)
	}
	r, err := f.Open()
	if err != nil {
		return fmt.Errorf("%w: metadata.json read", ErrInvalidPackage)
	}
	defer func() { _ = r.Close() }()
	data, err := io.ReadAll(io.LimitReader(r, 4097))
	if err != nil || len(data) > 4096 {
		return fmt.Errorf("%w: metadata.json content", ErrInvalidPackage)
	}
	var meta struct {
		Version int `json:"conda_pkg_format_version"`
	}
	if json.Unmarshal(data, &meta) != nil || meta.Version != 2 {
		return fmt.Errorf("%w: metadata.json format version", ErrInvalidPackage)
	}
	return nil
}

func readZstdTar(ctx context.Context, f *zip.File, id PackagePath) (Record, error) {
	r, err := f.Open()
	if err != nil {
		return Record{}, fmt.Errorf("%w: ZIP member read", ErrInvalidPackage)
	}
	defer func() { _ = r.Close() }()
	z, err := zstd.NewReader(r, zstd.WithDecoderMaxMemory(uint64(maxExpandedPackage)))
	if err != nil {
		return Record{}, fmt.Errorf("%w: zstd member", ErrInvalidPackage)
	}
	defer z.Close()
	return readTar(ctx, z, id)
}

func readTar(ctx context.Context, body io.Reader, id PackagePath) (Record, error) {
	tr := tar.NewReader(&io.LimitedReader{R: body, N: maxExpandedPackage + 1})
	var index []byte
	var total int64
	for n := 0; n < maxTarEntries; n++ {
		if err := ctx.Err(); err != nil {
			return Record{}, fmt.Errorf("conda: archive canceled: %w", err)
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			if id.Path == "" {
				return Record{}, nil
			}
			if index == nil {
				return Record{}, fmt.Errorf("%w: missing info/index.json", ErrInvalidPackage)
			}
			return parsePackageIndex(index, id)
		}
		if err != nil || h == nil || h.Size < 0 || h.Size > maxExpandedPackage ||
			!safeArchivePath(strings.TrimSuffix(h.Name, "/")) ||
			h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA && //nolint:staticcheck // TAR's historic NUL regular-file flag remains valid.
				h.Typeflag != tar.TypeDir && h.Typeflag != tar.TypeSymlink &&
				h.Typeflag != tar.TypeLink {
			return Record{}, fmt.Errorf("%w: TAR entry", ErrInvalidPackage)
		}
		if h.Typeflag == tar.TypeSymlink || h.Typeflag == tar.TypeLink {
			if !safeArchivePath(h.Linkname) {
				return Record{}, fmt.Errorf("%w: TAR link", ErrInvalidPackage)
			}
		}
		total += h.Size
		if total > maxExpandedPackage {
			return Record{}, fmt.Errorf("%w: expanded archive limit", ErrInvalidPackage)
		}
		if h.Name != "info/index.json" {
			if _, err := io.CopyN(io.Discard, tr, h.Size); err != nil {
				return Record{}, fmt.Errorf("%w: TAR body", ErrInvalidPackage)
			}
			continue
		}
		if id.Path == "" || index != nil || h.Typeflag != tar.TypeReg || h.Size > maxIndexJSON {
			return Record{}, fmt.Errorf("%w: info/index.json", ErrInvalidPackage)
		}
		index, err = io.ReadAll(io.LimitReader(tr, maxIndexJSON+1))
		if err != nil || int64(len(index)) != h.Size {
			return Record{}, fmt.Errorf("%w: info/index.json content", ErrInvalidPackage)
		}
	}
	return Record{}, fmt.Errorf("%w: TAR entry limit", ErrInvalidPackage)
}
