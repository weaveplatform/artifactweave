package vcs

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

const (
	MaxArchiveBytes  = 256 << 20
	MaxExpandedBytes = 1 << 30
	MaxArchiveFiles  = 100000
)

var ErrInvalidArchive = errors.New("invalid VCS archive")

// ValidateArchive checks a complete provider archive before it is admitted to
// the shared source cache. It leaves the read seeker at an unspecified offset.
func ValidateArchive(ctx context.Context, body io.ReadSeeker, size int64, ext string) error {
	if size <= 0 || size > MaxArchiveBytes || !ValidExtension(ext) {
		return ErrInvalidArchive
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return ErrInvalidArchive
	}
	if ext == "zip" {
		data, err := io.ReadAll(io.LimitReader(body, MaxArchiveBytes+1))
		if err != nil || int64(len(data)) != size {
			return ErrInvalidArchive
		}
		return validateZIP(ctx, data)
	}
	gz, err := gzip.NewReader(io.LimitReader(body, size))
	if err != nil {
		return ErrInvalidArchive
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	count := 0
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("validate VCS tar archive: %w", err)
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || hdr == nil || !validArchivePath(hdr.Name) || count >= MaxArchiveFiles {
			return ErrInvalidArchive
		}
		count++
		switch hdr.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
			if hdr.Size < 0 || hdr.Size > MaxExpandedBytes-total {
				return ErrInvalidArchive
			}
			n, readErr := io.CopyN(io.Discard, tr, hdr.Size)
			if readErr != nil || n != hdr.Size {
				return ErrInvalidArchive
			}
			total += n
		default:
			return ErrInvalidArchive
		}
	}
	// tar.Reader stops at its end marker before gzip.Reader necessarily sees
	// the footer. Drain the bounded remainder so gzip verifies its CRC.
	limit := MaxExpandedBytes - total + 1
	n, err := io.Copy(io.Discard, io.LimitReader(gz, limit))
	if err != nil || n >= limit || count == 0 || gz.Close() != nil {
		return ErrInvalidArchive
	}
	return nil
}

func validateZIP(ctx context.Context, data []byte) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(zr.File) == 0 || len(zr.File) > MaxArchiveFiles {
		return ErrInvalidArchive
	}
	var total int64
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("validate VCS zip archive: %w", err)
		}
		if !validArchivePath(f.Name) || !f.Mode().IsRegular() && !f.FileInfo().IsDir() ||
			f.UncompressedSize64 > uint64(MaxExpandedBytes-total) {
			return ErrInvalidArchive
		}
		if f.FileInfo().IsDir() {
			continue
		}
		file, err := f.Open()
		if err != nil {
			return ErrInvalidArchive
		}
		n, readErr := io.Copy(io.Discard, io.LimitReader(file, MaxExpandedBytes-total+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || n < 0 || uint64(n) != f.UncompressedSize64 ||
			n > MaxExpandedBytes-total {
			return ErrInvalidArchive
		}
		total += n
	}
	return nil
}

func validArchivePath(name string) bool {
	clean := strings.TrimSuffix(name, "/")
	if clean == "" || strings.HasPrefix(clean, "/") || strings.Contains(clean, "\\") ||
		strings.ContainsRune(clean, '\x00') || path.Clean(clean) != clean {
		return false
	}
	for _, part := range strings.Split(clean, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
