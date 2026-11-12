package itemkit

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/debian"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// DebianSourceRequest admits remote package bytes only after the same bounded
// parser used for local uploads has checked them. A .dsc is verified before it
// can enter the cache; the Sources index additionally checks its parts.
func DebianSourceRequest(q source.Request) (source.Request, error) {
	p := q.Resource.Path
	switch {
	case debian.ReleaseMetadataPath(p):
		if q.Resource.Query != "" {
			return q, source.ErrInvalidRequest
		}
		q.Resource.Kind = source.ResourceKindMetadata
		q.Resource.Variant = "debian-release-v1"
		q.Resource.ValidateMetadata = func(
			_ context.Context, r io.ReadSeeker, size int64, _ source.MetadataResponse,
		) error {
			return validateDebianRemoteRelease(p, r, size)
		}
	case strings.Contains(p, "/by-hash/SHA256/"):
		expected, ok := debian.ByHashMetadataPath(p)
		if !ok || q.Resource.Query != "" {
			return q, kernel.ErrNotFound
		}
		q.Resource.Kind = source.ResourceKindMetadata
		q.Resource.Variant = "debian-by-hash-v1"
		q.Resource.ValidateMetadata = func(
			_ context.Context, r io.ReadSeeker, size int64, _ source.MetadataResponse,
		) error {
			return validateDebianRemoteHash(expected, r, size)
		}
	case strings.HasPrefix(path.Base(p), "Packages") ||
		strings.HasPrefix(path.Base(p), "Sources"):
		if !debian.PackagesIndexPath(p) && !debian.SourcesIndexPath(p) ||
			q.Resource.Query != "" {
			return q, kernel.ErrNotFound
		}
		q.Resource.Kind = source.ResourceKindMetadata
		q.Resource.PermissionFolder = true
		if debian.PackagesIndexPath(p) {
			q.Resource.Variant = "debian-packages-v2"
		} else {
			q.Resource.Variant = "debian-sources-v2"
		}
		q.Resource.ValidateMetadata = func(
			_ context.Context, r io.ReadSeeker, size int64, _ source.MetadataResponse,
		) error {
			if size < 0 || size > 64<<20 {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.FailureKindIntegrity,
				}
			}
			body, err := io.ReadAll(io.LimitReader(r, 64<<20+1))
			if err != nil || int64(len(body)) != size {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.FailureKindIntegrity,
				}
			}
			if debian.PackagesIndexPath(p) {
				_, err = debian.DecodePackagesIndex(body)
			} else {
				_, err = debian.DecodeSourcesIndex(body)
			}
			if err != nil {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.FailureKindIntegrity,
				}
			}
			return nil
		}
	case strings.HasSuffix(p, ".deb") || strings.HasSuffix(p, ".ddeb"):
		if !debian.ValidBinaryPath(p) {
			return q, kernel.ErrNotFound
		}
		q.Resource.Variant = "debian-binary-v1"
		q.Resource.Validate = func(ctx context.Context, r io.ReadSeeker, size int64) error {
			if _, err := debian.ReadBinary(ctx, seekReaderAt{r}, size); err != nil {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.FailureKindIntegrity,
				}
			}
			return nil
		}
	case strings.HasSuffix(p, ".dsc"):
		if !debian.ValidSourcePath(p) {
			return q, kernel.ErrNotFound
		}
		q.Resource.Variant = "debian-source-v1"
		q.Resource.Validate = func(ctx context.Context, r io.ReadSeeker, size int64) error {
			if _, err := debian.ReadSource(ctx, r, size); err != nil {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.FailureKindIntegrity,
				}
			}
			return nil
		}
	}
	return q, nil
}

func readDebianRemoteMetadata(r io.ReadSeeker, size, limit int64) ([]byte, error) {
	if size <= 0 || size > limit {
		return nil, &source.HTTPError{
			Status: http.StatusBadGateway,
			Kind:   source.FailureKindIntegrity,
		}
	}
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil || int64(len(body)) != size {
		return nil, &source.HTTPError{
			Status: http.StatusBadGateway,
			Kind:   source.FailureKindIntegrity,
		}
	}
	return body, nil
}

func validateDebianRemoteRelease(path string, r io.ReadSeeker, size int64) error {
	body, err := readDebianRemoteMetadata(r, size, 4<<20)
	if err != nil {
		return err
	}
	if debian.ValidateReleaseMetadata(path, body) != nil {
		return &source.HTTPError{Status: http.StatusBadGateway, Kind: source.FailureKindIntegrity}
	}
	return nil
}

func validateDebianRemoteHash(expected string, r io.ReadSeeker, size int64) error {
	body, err := readDebianRemoteMetadata(r, size, 64<<20)
	if err != nil {
		return err
	}
	if fmt.Sprintf("%x", sha256.Sum256(body)) != expected {
		return &source.HTTPError{Status: http.StatusBadGateway, Kind: source.FailureKindIntegrity}
	}
	return nil
}

type seekReaderAt struct{ io.ReadSeeker }

func (r seekReaderAt) ReadAt(p []byte, offset int64) (int, error) {
	if _, err := r.Seek(offset, io.SeekStart); err != nil {
		return 0, fmt.Errorf("seek debian remote archive: %w", err)
	}
	n, err := io.ReadFull(r, p)
	if err != nil {
		return n, fmt.Errorf("read debian remote archive: %w", err)
	}
	return n, nil
}
