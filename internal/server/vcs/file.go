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
	"strings"
)

const MaxSelectedFileBytes = 64 << 20

const MaxFileZIPBytes = MaxSelectedFileBytes + 1<<20

// ValidateFileZIP admits only a bounded ZIP containing the exact selected file.
// A peer response is returned unchanged after validation, without root inference.
func ValidateFileZIP(ctx context.Context, body io.ReadSeeker, size int64, filename string) error {
	if size <= 0 || size > MaxFileZIPBytes || !ValidFilePath(filename) {
		return ErrInvalidArchive
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return ErrInvalidArchive
	}
	data, err := io.ReadAll(io.LimitReader(body, MaxFileZIPBytes+1))
	if err != nil || int64(len(data)) != size {
		return ErrInvalidArchive
	}
	zr, err := zip.NewReader(bytes.NewReader(data), size)
	if err != nil {
		return ErrInvalidArchive
	}
	selected := false
	for _, file := range zr.File {
		if file.FileInfo().IsDir() && strings.HasSuffix(file.Name, "/") &&
			strings.HasPrefix(filename, file.Name) {
			continue
		}
		if selected || file.Name != filename || !file.Mode().IsRegular() ||
			file.UncompressedSize64 > MaxSelectedFileBytes {
			return ErrInvalidArchive
		}
		selected = true
	}
	if !selected {
		return ErrInvalidArchive
	}
	return validateZIP(ctx, data, "")
}

// FileZIP selects one file from an already validated provider archive and
// returns a ZIP with that file at its repository-relative path.
func FileZIP(ctx context.Context, archive []byte, ext, filename string) ([]byte, error) {
	return fileZIP(ctx, archive, ext, filename, false)
}

// WrappedFileZIP selects a path from providers whose archive contract adds one
// enclosing directory. Rootless providers must use FileZIP instead.
func WrappedFileZIP(ctx context.Context, archive []byte, ext, filename string) ([]byte, error) {
	return fileZIP(ctx, archive, ext, filename, true)
}

func fileZIP(
	ctx context.Context,
	archive []byte,
	ext, filename string,
	wrapped bool,
) ([]byte, error) {
	if len(archive) == 0 || len(archive) > MaxArchiveBytes || !ValidExtension(ext) ||
		!ValidFilePath(filename) {
		return nil, ErrInvalidArchive
	}
	var selected []byte
	var err error
	if ext == "zip" {
		selected, err = selectZIP(ctx, archive, filename, wrapped)
	} else {
		selected, err = selectTAR(ctx, archive, filename, wrapped)
	}
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	w, err := zw.Create(filename)
	if err != nil {
		return nil, ErrInvalidArchive
	}
	if _, err := w.Write(selected); err != nil {
		return nil, ErrInvalidArchive
	}
	if err := zw.Close(); err != nil {
		return nil, ErrInvalidArchive
	}
	return out.Bytes(), nil
}

func selectZIP(ctx context.Context, archive []byte, filename string, wrapped bool) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, ErrInvalidArchive
	}
	var selected []byte
	for _, file := range zr.File {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("select vcs zip file: %w", err)
		}
		if !matchesArchiveFile(file.Name, filename, wrapped) {
			continue
		}
		if selected != nil || file.FileInfo().IsDir() ||
			file.UncompressedSize64 > MaxSelectedFileBytes {
			return nil, ErrInvalidArchive
		}
		body, err := file.Open()
		if err != nil {
			return nil, ErrInvalidArchive
		}
		selected, err = io.ReadAll(io.LimitReader(body, MaxSelectedFileBytes+1))
		closeErr := body.Close()
		if err != nil || closeErr != nil || len(selected) > MaxSelectedFileBytes ||
			uint64(len(selected)) != file.UncompressedSize64 {
			return nil, ErrInvalidArchive
		}
	}
	if selected == nil {
		return nil, ErrInvalidPath
	}
	return selected, nil
}

func selectTAR(ctx context.Context, archive []byte, filename string, wrapped bool) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, ErrInvalidArchive
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var selected []byte
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("select vcs tar file: %w", err)
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || hdr == nil {
			return nil, ErrInvalidArchive
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader ||
			!matchesArchiveFile(hdr.Name, filename, wrapped) {
			continue
		}
		if selected != nil || hdr.Typeflag != tar.TypeReg || hdr.Size < 0 ||
			hdr.Size > MaxSelectedFileBytes {
			return nil, ErrInvalidArchive
		}
		selected, err = io.ReadAll(io.LimitReader(tr, MaxSelectedFileBytes+1))
		if err != nil || len(selected) > MaxSelectedFileBytes || int64(len(selected)) != hdr.Size {
			return nil, ErrInvalidArchive
		}
	}
	if selected == nil {
		return nil, ErrInvalidPath
	}
	return selected, nil
}

func selectedArchiveFile(entry, file string) bool {
	return validArchivePath(entry) && entry == file
}

func matchesArchiveFile(entry, file string, wrapped bool) bool {
	if !wrapped {
		return selectedArchiveFile(entry, file)
	}
	if !validArchivePath(entry) {
		return false
	}
	_, relative, ok := strings.Cut(entry, "/")
	return ok && relative == file
}
