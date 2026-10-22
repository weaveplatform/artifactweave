package nuget

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/archive"
)

// ValidateRemote preserves the original archive (including its signature) while
// checking identity and every ZIP CRC before the remote cache publishes bytes.
// Cryptographic signature trust is a NuGet client decision; this validator never
// re-signs an upstream package or treats a well-formed signature as trusted.
func ValidateRemote(ctx context.Context, r io.ReadSeeker, size int64, id, key string) error {
	ra := archive.ReaderAt(r)
	n, err := ReadPackage(ra, size)
	if err != nil {
		return fmt.Errorf("remote NuGet package: %w", err)
	}
	if !strings.EqualFold(n.ID, id) || strings.ToLower(n.Version.Normalized()) != key ||
		n.IsSymbols() {
		return ErrNotPackage
	}
	zr, err := zip.NewReader(ra, size)
	if err != nil {
		return fmt.Errorf("remote NuGet archive: %w", err)
	}
	var total uint64
	for _, f := range zr.File {
		if err = ctx.Err(); err != nil {
			return fmt.Errorf("remote NuGet validation: %w", err)
		}
		if f.UncompressedSize64 > 16<<30 || total > 16<<30-f.UncompressedSize64 {
			return ErrNotPackage
		}
		total += f.UncompressedSize64
		rc, openErr := f.Open()
		if openErr != nil {
			return fmt.Errorf("remote NuGet entry: %w", openErr)
		}
		_, err = io.Copy(
			io.Discard,
			io.LimitReader(&validationReader{ctx: ctx, r: rc}, int64(f.UncompressedSize64)+1),
		)
		_ = rc.Close()
		if err != nil {
			return fmt.Errorf("remote NuGet integrity: %w", err)
		}
	}
	return nil
}

// Cancellation remains effective inside a large compressed archive entry.
type validationReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *validationReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, fmt.Errorf("NuGet validation: %w", err)
	}
	n, err := r.r.Read(p)
	return n, err //nolint:wrapcheck // reader contract
}
