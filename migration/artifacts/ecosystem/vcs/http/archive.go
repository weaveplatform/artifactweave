package vcshttp

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

// MaxArchiveBytes, MaxExpandedBytes and MaxArchiveFiles bound a provider
// archive: 256 MiB as fetched, 1 GiB expanded and 100,000 entries. The
// root files read at most MaxArchiveBytes of an archive before selecting a
// file from it.
const (
	MaxArchiveBytes  = 256 << 20
	MaxExpandedBytes = 1 << 30
	MaxArchiveFiles  = 100000
)

// ErrInvalidArchive is the error the archive and file checks return for an
// archive or selected-file ZIP they refuse: over a bound, corrupt, holding
// an unsafe entry, or marked with another commit.
var ErrInvalidArchive = errors.New("vcs: invalid vcs archive")

// ValidateArchive checks a complete provider archive before it is admitted to
// the shared source cache. It leaves the read seeker at an unspecified offset.
func ValidateArchive(ctx context.Context, body io.ReadSeeker, size int64, ext string) error {
	return validateArchive(ctx, body, size, ext, "")
}

// ValidateCommitArchive also binds Git archive commit metadata, when present,
// to the immutable object requested from the provider.
func ValidateCommitArchive(
	ctx context.Context,
	body io.ReadSeeker,
	size int64,
	ext, commit string,
) error {
	if !ValidCommitID(commit) {
		return ErrInvalidArchive
	}
	return validateArchive(ctx, body, size, ext, commit)
}

func validateArchive(
	ctx context.Context,
	body io.ReadSeeker,
	size int64,
	ext, commit string,
) error {
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
		return validateZIP(ctx, data, commit)
	}
	return validateTAR(ctx, body, size, commit)
}

func validateTAR(ctx context.Context, body io.Reader, size int64, commit string) error {
	gz, err := gzip.NewReader(io.LimitReader(body, size))
	if err != nil {
		return ErrInvalidArchive
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	count := 0
	globalHeader := false
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("validate vcs tar archive: %w", err)
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || hdr == nil || !validArchivePath(hdr.Name) || count >= MaxArchiveFiles {
			return ErrInvalidArchive
		}
		if !validArchiveCommit(hdr.PAXRecords["comment"], commit) {
			return ErrInvalidArchive
		}
		switch hdr.Typeflag {
		case tar.TypeXGlobalHeader:
			// git archive emits its commit as the sole global PAX record.
			// Other global records can alter every subsequent entry's identity.
			if globalHeader || count != 0 || !validGlobalArchiveHeader(hdr) {
				return ErrInvalidArchive
			}
			globalHeader = true
			continue
		case tar.TypeDir:
			count++
			continue
		case tar.TypeReg:
			count++
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

func validateZIP(ctx context.Context, data []byte, commit string) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(zr.File) == 0 || len(zr.File) > MaxArchiveFiles {
		return ErrInvalidArchive
	}
	if ValidCommitID(zr.Comment) && commit != "" && zr.Comment != commit {
		return ErrInvalidArchive
	}
	var total int64
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("validate vcs zip archive: %w", err)
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

func validArchiveCommit(comment, expected string) bool {
	return comment == "" || ValidCommitID(comment) && (expected == "" || comment == expected)
}

func validGlobalArchiveHeader(hdr *tar.Header) bool {
	return hdr.Name == "pax_global_header" && len(hdr.PAXRecords) == 1 &&
		ValidCommitID(hdr.PAXRecords["comment"])
}
