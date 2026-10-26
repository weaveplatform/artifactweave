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

// FileZIP selects one file from an already validated provider archive and
// returns a ZIP with that file at its repository-relative path.
func FileZIP(ctx context.Context, archive []byte, ext, filename string) ([]byte, error) {
	if len(archive) == 0 || len(archive) > MaxArchiveBytes || !ValidExtension(ext) ||
		!ValidFilePath(filename) {
		return nil, ErrInvalidArchive
	}
	var selected []byte
	var err error
	if ext == "zip" {
		selected, err = selectZIP(ctx, archive, filename)
	} else {
		selected, err = selectTAR(ctx, archive, filename)
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

func selectZIP(ctx context.Context, archive []byte, filename string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, ErrInvalidArchive
	}
	var selected []byte
	for _, file := range zr.File {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("select VCS zip file: %w", err)
		}
		if !selectedArchiveFile(file.Name, filename) {
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

func selectTAR(ctx context.Context, archive []byte, filename string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, ErrInvalidArchive
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var selected []byte
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("select VCS tar file: %w", err)
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || hdr == nil {
			return nil, ErrInvalidArchive
		}
		if !selectedArchiveFile(hdr.Name, filename) {
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
	if !validArchivePath(entry) {
		return false
	}
	if entry == file {
		return true // A provider may return a rootless archive.
	}
	_, relative, ok := strings.Cut(entry, "/")
	return ok && relative == file
}
