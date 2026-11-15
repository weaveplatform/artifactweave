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
	"strings"
)

// MaxSelectedFileBytes bounds a selected file, 64 MiB, whether it is taken
// from an archive (SelectFile, SelectWrappedFile, SelectCustomFile) or
// arrives from a peer (ValidateFile).
const MaxSelectedFileBytes = 64 << 20

// ValidateFile admits a peer's selected file of size bytes within
// MaxSelectedFileBytes. A 7.x peer answers the file itself, as a remote VCS
// repository answered downloadBranchFile/.../main!bazel_registry.json with
// its JSON (RTFACT-29495), so its bytes are not otherwise checked.
func ValidateFile(ctx context.Context, body io.ReadSeeker, size int64) error {
	if size < 0 || size > MaxSelectedFileBytes {
		return ErrInvalidArchive
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("validate vcs file: %w", err)
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return ErrInvalidArchive
	}
	n, err := io.Copy(io.Discard, io.LimitReader(body, MaxSelectedFileBytes+1))
	if err != nil || n != size {
		return ErrInvalidArchive
	}
	return nil
}

// SelectFile selects one file's bytes from an already validated provider
// archive by its repository-relative path.
func SelectFile(ctx context.Context, archive []byte, ext, filename string) ([]byte, error) {
	return selectFile(ctx, archive, ext, filename, false)
}

// SelectWrappedFile selects a path from providers whose archive contract
// adds one enclosing directory. Rootless providers must use SelectFile
// instead.
func SelectWrappedFile(ctx context.Context, archive []byte, ext, filename string) ([]byte, error) {
	return selectFile(ctx, archive, ext, filename, true)
}

// SelectCustomFile preserves custom templates that return rootless or
// wrapped archives. An exact repository path takes precedence; fallback
// still rejects multiple matching wrapper directories.
func SelectCustomFile(ctx context.Context, archive []byte, ext, filename string) ([]byte, error) {
	data, err := SelectFile(ctx, archive, ext, filename)
	if !errors.Is(err, ErrInvalidPath) {
		return data, err
	}
	return SelectWrappedFile(ctx, archive, ext, filename)
}

func selectFile(
	ctx context.Context,
	archive []byte,
	ext, filename string,
	wrapped bool,
) ([]byte, error) {
	if len(archive) == 0 || len(archive) > MaxArchiveBytes || !ValidExtension(ext) ||
		!ValidFilePath(filename) {
		return nil, ErrInvalidArchive
	}
	if ext == "zip" {
		return selectZIP(ctx, archive, filename, wrapped)
	}
	return selectTAR(ctx, archive, filename, wrapped)
}

// FileZIP is a ZIP holding data, a selected file, at its
// repository-relative path filename, which a file within a tag or branch
// answers when ext=zip asks for one (JFrog's VCS repositories page,
// "download a file within a tag as a zip").
func FileZIP(filename string, data []byte) ([]byte, error) {
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	w, err := zw.Create(filename)
	if err != nil {
		return nil, ErrInvalidArchive
	}
	if _, err := w.Write(data); err != nil {
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
