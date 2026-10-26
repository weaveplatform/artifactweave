package jetbrainsplugins

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/source"
)

const (
	maxArchiveBytes    = 1 << 30
	maxExpandedBytes   = 4 << 30
	maxNestedJarBytes  = 256 << 20
	maxDescriptorBytes = 1 << 20
	maxArchiveEntries  = 20_000
)

func decimal(n int64) string { return strconv.FormatInt(n, 10) }

func validateArchive(id string) func(context.Context, io.ReadSeeker, int64) error {
	return func(ctx context.Context, body io.ReadSeeker, size int64) error {
		if size <= 0 || size > maxArchiveBytes {
			return source.ErrInvalidResult
		}
		defer func() { _, _ = body.Seek(0, io.SeekStart) }()
		reader, cleanup, err := archiveReader(body, size)
		if err != nil {
			return source.ErrInvalidResult
		}
		defer cleanup()
		z, err := zip.NewReader(reader, size)
		if err != nil || len(z.File) == 0 || len(z.File) > maxArchiveEntries {
			return source.ErrInvalidResult
		}
		var descriptor *zip.File
		var expanded uint64
		for _, file := range z.File {
			if !safeArchivePath(file.Name) {
				return source.ErrInvalidResult
			}
			if file.UncompressedSize64 > maxExpandedBytes-expanded ||
				!validEntryBytes(ctx, file) {
				return source.ErrInvalidResult
			}
			expanded += file.UncompressedSize64
			if file.Name == "META-INF/plugin.xml" {
				if descriptor != nil {
					return source.ErrInvalidResult
				}
				descriptor = file
			}
		}
		if descriptor != nil {
			return validateDescriptor(ctx, descriptor, id)
		}
		for _, file := range z.File {
			if strings.HasSuffix(strings.ToLower(file.Name), ".jar") &&
				strings.Contains(
					file.Name,
					"/lib/",
				) && file.UncompressedSize64 <= maxNestedJarBytes {
				if err := validateNestedJar(ctx, file, id); err == nil {
					return nil
				}
			}
		}
		return source.ErrInvalidResult
	}
}

// validEntryBytes streams each entry to EOF, checking its claimed size and CRC
// without holding extracted plugin files in memory.
func validEntryBytes(ctx context.Context, file *zip.File) bool {
	if file.UncompressedSize64 > maxExpandedBytes || ctx.Err() != nil {
		return false
	}
	r, err := file.Open()
	if err != nil {
		return false
	}
	defer r.Close()
	size := int64(file.UncompressedSize64) //nolint:gosec // Bounded to 4 GiB above.
	n, err := io.Copy(io.Discard, io.LimitReader(contextReader{ctx, r}, size+1))
	return err == nil && n == size && ctx.Err() == nil
}

type contextReader struct {
	ctx context.Context
	io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, fmt.Errorf("archive validation cancelled: %w", err)
	}
	return r.Reader.Read(p) //nolint:wrapcheck // Preserves the stream's EOF and CRC error.
}

func archiveReader(body io.ReadSeeker, size int64) (io.ReaderAt, func(), error) {
	if reader, ok := body.(io.ReaderAt); ok {
		return reader, func() {}, nil
	}
	tmp, err := os.CreateTemp("", "artifactweave-jetbrains-archive-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create temporary archive: %w", err)
	}
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("seek archive: %w", err)
	}
	n, err := io.CopyN(tmp, body, size)
	if err != nil || n != size {
		cleanup()
		return nil, nil, source.ErrInvalidResult
	}
	return tmp, cleanup, nil
}

func safeArchivePath(name string) bool {
	if name == "" || len(name) > 1024 || strings.HasPrefix(name, "/") ||
		strings.Contains(name, "\\") || strings.Contains(name, "\x00") ||
		path.Clean(name) != strings.TrimSuffix(name, "/") {
		return false
	}
	for _, part := range strings.Split(strings.TrimSuffix(name, "/"), "/") {
		if part == "." || part == ".." || part == "" {
			return false
		}
	}
	return true
}

func validateNestedJar(ctx context.Context, file *zip.File, id string) error {
	r, err := file.Open()
	if err != nil {
		return source.ErrInvalidResult
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(contextReader{ctx, r}, maxNestedJarBytes+1))
	if err != nil || uint64(len(data)) != file.UncompressedSize64 ||
		len(data) > maxNestedJarBytes {
		return source.ErrInvalidResult
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(z.File) > maxArchiveEntries {
		return source.ErrInvalidResult
	}
	var descriptor *zip.File
	var expanded uint64
	for _, inner := range z.File {
		if !safeArchivePath(inner.Name) {
			return source.ErrInvalidResult
		}
		if inner.UncompressedSize64 > maxExpandedBytes-expanded ||
			!validEntryBytes(ctx, inner) {
			return source.ErrInvalidResult
		}
		expanded += inner.UncompressedSize64
		if inner.Name == "META-INF/plugin.xml" {
			if descriptor != nil {
				return source.ErrInvalidResult
			}
			descriptor = inner
		}
	}
	if descriptor == nil {
		return source.ErrInvalidResult
	}
	return validateDescriptor(ctx, descriptor, id)
}

func validateDescriptor(ctx context.Context, file *zip.File, expectedID string) error {
	if file.UncompressedSize64 == 0 || file.UncompressedSize64 > maxDescriptorBytes {
		return source.ErrInvalidResult
	}
	r, err := file.Open()
	if err != nil {
		return source.ErrInvalidResult
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, maxDescriptorBytes+1))
	if err != nil || uint64(len(data)) != file.UncompressedSize64 || ctx.Err() != nil {
		return source.ErrInvalidResult
	}
	var d struct {
		XMLName xml.Name `xml:"idea-plugin"`
		ID      string   `xml:"id"`
		Name    string   `xml:"name"`
		Version string   `xml:"version"`
		Idea    struct {
			Since string `xml:"since-build,attr"`
			Until string `xml:"until-build,attr"`
		} `xml:"idea-version"`
	}
	if xml.Unmarshal(data, &d) != nil || d.ID != expectedID || d.Name == "" ||
		len(d.Name) > 512 || !safeVersion(d.Version) ||
		!safeBuild(d.Idea.Since) || d.Idea.Until != "" && !safeBuildWildcard(d.Idea.Until) {
		return source.ErrInvalidResult
	}
	return nil
}
