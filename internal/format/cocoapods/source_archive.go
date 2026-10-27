package cocoapods

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/archivecheck"
)

// ValidateSourceArchive checks an HTTP-sourced pod's complete archive before
// remote cache publication. Its podspec comes from the CDN, not this archive.
func ValidateSourceArchive(ctx context.Context, body io.ReadSeeker, size int64, ext string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("validate CocoaPods source archive: %w", err)
	}
	if size <= 0 || size > MaxArchiveBytes {
		return invalid("invalid CocoaPods source archive size")
	}
	data, err := io.ReadAll(io.LimitReader(sourceArchiveReader{ctx, body}, MaxArchiveBytes+1))
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("read CocoaPods source archive: %w", err)
	}
	if err != nil || int64(len(data)) != size {
		return invalid("truncated CocoaPods source archive")
	}
	switch ext {
	case ".zip":
		err = validateZIP(ctx, data)
	case ".tgz", ".tar.gz":
		err = validateTGZ(ctx, data)
	default:
		return invalid("unsupported CocoaPods source archive extension")
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return fmt.Errorf("validate CocoaPods source archive: %w", contextErr)
	}
	return err
}

func safeArchivePath(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\\x00") {
		return false
	}
	for _, part := range strings.Split(strings.TrimSuffix(name, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func validateZIP(ctx context.Context, data []byte) error {
	r, err := archivecheck.ZIP(
		ctx,
		bytes.NewReader(data),
		int64(len(data)),
		archivecheck.ZIPLimits{
			ArchiveBytes: MaxArchiveBytes, DirectoryBytes: 16 << 20, Entries: maxArchiveFiles,
		},
	)
	if err != nil || len(r.File) == 0 || len(r.File) > maxArchiveFiles {
		return invalid("invalid CocoaPods source zip")
	}
	seen := map[string]bool{}
	var expanded int64
	for _, file := range r.File {
		name := strings.TrimSuffix(file.Name, "/")
		if !safeArchivePath(file.Name) || seen[name] ||
			!file.Mode().IsRegular() && !file.FileInfo().IsDir() {
			return invalid("unsafe CocoaPods source zip member")
		}
		seen[name] = true
		if file.UncompressedSize64 > maxExpandedBytes ||
			expanded+int64(file.UncompressedSize64) > maxExpandedBytes {
			return invalid("oversized CocoaPods source zip")
		}
		expanded += int64(file.UncompressedSize64)
		rc, err := file.Open()
		if err != nil {
			return fmt.Errorf("open cocoapods zip member: %w", err)
		}
		_, copyErr := io.Copy(
			io.Discard,
			io.LimitReader(sourceArchiveReader{ctx, rc}, maxExpandedBytes+1),
		)
		closeErr := rc.Close()
		if copyErr != nil || closeErr != nil {
			return invalid("corrupt CocoaPods source zip member")
		}
	}
	return nil
}

func validateTGZ(ctx context.Context, data []byte) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("cocoapods source gzip: %w", err)
	}
	defer gz.Close()
	// Count extension records and padding consumed inside tar.Reader.Next,
	// not only the logical file sizes it exposes to the caller.
	decoded := &io.LimitedReader{R: sourceArchiveReader{ctx, gz}, N: maxExpandedBytes + 1}
	r := tar.NewReader(decoded)
	seen := map[string]bool{}
	var expanded int64
	for count := 0; ; count++ {
		h, err := r.Next()
		if decoded.N <= 0 {
			return invalid("oversized CocoaPods expanded source archive")
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || h == nil || count >= maxArchiveFiles || h.Size < 0 ||
			!safeArchivePath(h.Name) || seen[strings.TrimSuffix(h.Name, "/")] ||
			(h.Typeflag != tar.TypeReg && h.Typeflag != 0 && h.Typeflag != tar.TypeDir) {
			return invalid("unsafe CocoaPods source tar member")
		}
		seen[strings.TrimSuffix(h.Name, "/")] = true
		expanded += h.Size
		if expanded > maxExpandedBytes {
			return invalid("oversized CocoaPods source tar")
		}
		if h.Typeflag != tar.TypeDir {
			if _, err := io.CopyN(io.Discard, r, h.Size); err != nil {
				return invalid("truncated CocoaPods source tar member")
			}
		}
	}
	if len(seen) == 0 {
		return invalid("empty CocoaPods source archive")
	}
	var extra [1]byte
	if n, err := decoded.Read(extra[:]); decoded.N <= 0 || n != 0 || !errors.Is(err, io.EOF) {
		return invalid("extra CocoaPods source data or invalid gzip trailer")
	}
	return nil
}

// Check cancellation on decoded reads too: a small compressed archive can
// require substantial validation work after its bytes have been downloaded.
type sourceArchiveReader struct {
	ctx context.Context
	r   io.Reader
}

func (r sourceArchiveReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, fmt.Errorf("read CocoaPods source archive: %w", err)
	}
	return r.r.Read(p) //nolint:wrapcheck // Preserve io.Reader's EOF and partial-read semantics.
}
