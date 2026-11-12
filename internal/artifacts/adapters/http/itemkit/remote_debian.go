package itemkit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
//
// Otherwise a remote repository relays what its upstream serves, as 7.x
// proxies and caches it (#1273). A package is admitted by its own grammar
// first, so a folder named by-hash or a file name that starts Packages does
// not hide it. Any by-hash file is checked against the SHA-256 its name
// gives, and an index apt reads by the name Packages or Sources, a Debian
// installer's included, is validated as metadata. Every other path, a
// translation or a Contents index for one, passes through unchecked; a
// path in none of these shapes that only resembles one answers not found
// without asking the upstream, as it did before.
//
// A query refuses no index and no by-hash file (#1349). 7.x reads a remote
// Debian file through its generic remote download, as the ?trace of
// RTFACT-18414 (6.7.1) and RTFACT-26349 (7.25.7) show, and passes a query to
// the upstream only when the repository's Propagate Query Params is set,
// which JFrog's Remote Repositories page lists among the settings of a
// Generic remote repository (6.20 HttpRepo.addQueryParams). The source
// boundary already applies that rule to q.Resource.Query, and the cache key
// holds the query only then. A Release or InRelease file still refuses a
// query other than the server's own controls, which the item route strips,
// with 400. The same sources point to serving it; that change is left to
// a follow-up of #1349.
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
	case debian.ValidBinaryPath(p):
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
	case debian.ValidSourcePath(p):
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
	case strings.Contains(p, "/by-hash/SHA256/"):
		expected, ok := debian.ByHashPath(p)
		if !ok {
			return q, kernel.ErrNotFound
		}
		// The index route (TryDebianItem) reads a binary-<arch> or source
		// index's copy whole, so that copy keeps the route's 64 MiB bound.
		// Any other copy, of a Contents index for one, is hashed as it
		// streams and bounded only as every remote file is.
		limit := int64(0)
		if _, index := debian.ByHashMetadataPath(p); index {
			limit = 64 << 20
		}
		q.Resource.Kind = source.ResourceKindMetadata
		q.Resource.Variant = "debian-by-hash-v1"
		q.Resource.ValidateMetadata = func(
			_ context.Context, r io.ReadSeeker, size int64, _ source.MetadataResponse,
		) error {
			return validateDebianRemoteHash(expected, r, size, limit)
		}
	case strings.HasPrefix(path.Base(p), "Packages") ||
		strings.HasPrefix(path.Base(p), "Sources"):
		return debianIndexSourceRequest(q)
	case strings.HasSuffix(p, ".deb") || strings.HasSuffix(p, ".ddeb") ||
		strings.HasSuffix(p, ".dsc"):
		return q, kernel.ErrNotFound
	}
	return q, nil
}

// debianIndexSourceRequest prepares the read of a file whose name starts
// Packages or Sources: an index apt reads, validated as metadata, or not
// found for any other such name.
//
// An installer's Packages index (debian.InstallerPackagesIndexPath) is
// metadata, so the retrieval cache period renews it, as RTFACT-18414
// (7.3.0) made the installer's SHA256SUMS metadata beside it [I]. It is
// read with the permission on its own path, as a translation or a Contents
// index is, not with the check the index route makes of every package a
// binary-<arch> or source index lists: 7.x documents only path permissions
// for Debian metadata, and the installer's index is not one artifactweave
// composes or snapshots as an index [I].
func debianIndexSourceRequest(q source.Request) (source.Request, error) {
	p := q.Resource.Path
	var decodes func([]byte) bool
	switch {
	case debian.PackagesIndexPath(p):
		q.Resource.PermissionFolder = true
		q.Resource.Variant = "debian-packages-v2"
		decodes = func(body []byte) bool {
			_, err := debian.DecodePackagesIndex(body)
			return err == nil
		}
	case debian.SourcesIndexPath(p):
		q.Resource.PermissionFolder = true
		q.Resource.Variant = "debian-sources-v2"
		decodes = func(body []byte) bool {
			_, err := debian.DecodeSourcesIndex(body)
			return err == nil
		}
	case debian.InstallerPackagesIndexPath(p):
		q.Resource.Variant = "debian-installer-packages-v1"
		decodes = func(body []byte) bool {
			_, err := debian.DecodeInstallerPackagesIndex(body)
			return err == nil
		}
	default:
		return q, kernel.ErrNotFound
	}
	q.Resource.Kind = source.ResourceKindMetadata
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
		if err != nil || int64(len(body)) != size || !decodes(body) {
			return &source.HTTPError{
				Status: http.StatusBadGateway,
				Kind:   source.FailureKindIntegrity,
			}
		}
		return nil
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

// validateDebianRemoteHash accepts a by-hash file of size bytes, at most
// limit when limit is above zero, whose SHA-256 is expected.
func validateDebianRemoteHash(expected string, r io.ReadSeeker, size, limit int64) error {
	if size <= 0 || limit > 0 && size > limit {
		return &source.HTTPError{Status: http.StatusBadGateway, Kind: source.FailureKindIntegrity}
	}
	sum := sha256.New()
	n, err := io.Copy(sum, io.LimitReader(r, size+1))
	if err != nil || n != size || hex.EncodeToString(sum.Sum(nil)) != expected {
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
