package hex

import (
	"bytes"
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	hexfmt "github.com/weaveplatform/artifactweave/internal/format/hex"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func upstreamFailure() error {
	return apierror.New(http.StatusBadGateway, "invalid upstream Hex registry resource")
}

const maxLegacyChecksumReleases = 64

func (h handler) upstreamKey(r *http.Request) (*rsa.PublicKey, error) {
	key, err := hexfmt.ParsePublicKey(pkgroute.RequestFrom(r).Repo.HexPublicKey)
	if err != nil {
		return nil, apierror.New(
			http.StatusServiceUnavailable,
			"upstream Hex public key is missing or invalid",
		)
	}
	return key, nil
}

func (h handler) remoteMetadata(
	r *http.Request,
	path string,
	key *rsa.PublicKey,
) (source.Result, []byte, error) {
	resource := source.Resource{
		Kind:             source.ResourceKindMetadata,
		Path:             path,
		Method:           http.MethodGet,
		PermissionFolder: true,
		PermissionPath:   "tarballs",
		Variant:          "hex-registry-v2-signed",
		ValidateMetadata: func(ctx context.Context, body io.ReadSeeker, size int64, _ source.MetadataResponse) error {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("hex: validate metadata context: %w", err)
			}
			if size <= 0 || size > 16<<20 {
				return source.ErrInvalidResult
			}
			data, err := io.ReadAll(io.LimitReader(body, 16<<20+1))
			if err != nil || len(data) > 16<<20 {
				return source.ErrInvalidResult
			}
			if path == "versions" {
				_, _, err = hexfmt.DecodeVersions(data, key)
			} else {
				_, err = hexfmt.DecodePackage(data, key)
			}
			if err != nil {
				return source.ErrInvalidResult
			}
			return nil
		},
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return source.Result{}, nil, fmt.Errorf("hex: read remote metadata: %w", err)
	}
	var body io.ReadCloser
	switch {
	case result.Stream != nil:
		body = result.Stream.Body
		result.Stream = nil
	case result.Item != nil:
		body, _, err = h.k.Blobs().Open(r.Context(), result.Item.Actual.SHA256)
		if err != nil {
			return source.Result{}, nil, fmt.Errorf("hex: open cached metadata: %w", err)
		}
	default:
		return source.Result{}, nil, upstreamFailure()
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, 16<<20+1))
	if err != nil || len(data) > 16<<20 {
		return source.Result{}, nil, upstreamFailure()
	}
	return result, data, nil
}

func (h handler) remotePackages(
	r *http.Request,
	path string,
) (map[string]hexfmt.RegistryPackage, error) {
	out, _, err := h.remotePackagesWithAuthority(r, path)
	return out, err
}

func (h handler) remotePackagesWithAuthority(
	r *http.Request,
	path string,
) (map[string]hexfmt.RegistryPackage, source.Result, error) {
	key, err := h.upstreamKey(r)
	if err != nil {
		return nil, source.Result{}, err
	}
	upstream := path
	if path == "names" {
		upstream = "versions"
	}
	metadata, data, err := h.remoteMetadata(r, upstream, key)
	if err != nil {
		return nil, source.Result{}, err
	}
	if metadata.CheckRead == nil || metadata.Revalidate == nil {
		return nil, source.Result{}, upstreamFailure()
	}
	if metadata.Revalidate != nil {
		if err := metadata.Revalidate(r.Context()); err != nil {
			return nil, source.Result{}, fmt.Errorf("hex: revalidate remote metadata: %w", err)
		}
	}
	out := map[string]hexfmt.RegistryPackage{}
	if strings.HasPrefix(path, "packages/") {
		reg, err := hexfmt.DecodePackage(data, key)
		if err != nil || reg.Name != strings.TrimPrefix(path, "packages/") {
			return nil, source.Result{}, upstreamFailure()
		}
		upstreamRepository := reg.Repository
		reg.Repository = pkgroute.RequestFrom(r).Repo.Key
		legacyCount := 0
		for _, release := range reg.Releases {
			release = rebindRelease(release, upstreamRepository, reg.Repository)
			p, ok := hexfmt.TarballPath(reg.Name, release.Version)
			if !ok {
				return nil, source.Result{}, upstreamFailure()
			}
			if !h.k.Can(r, p, types.ActionRead) {
				continue
			}
			if err := metadata.CheckRead(r.Context(), p); errors.Is(err, source.ErrDenied) {
				continue
			} else if err != nil {
				return nil, source.Result{}, fmt.Errorf("hex: check remote release read: %w", err)
			}
			if release.OuterChecksum == [32]byte{} {
				legacyCount++
				if legacyCount > maxLegacyChecksumReleases {
					return nil, source.Result{}, upstreamFailure()
				}
				// Current Mix clients reject absent outer checksums at fetch,
				// though the upstream wire format permits them. Complete only
				// readable legacy releases after checking their signed inner
				// checksum and the archive's own structure.
				checksum, err := h.legacyOuterChecksum(r, p, release, metadata)
				if errors.Is(err, source.ErrDenied) {
					continue
				}
				if err != nil {
					return nil, source.Result{}, fmt.Errorf("hex: legacy checksum: %w", err)
				}
				release.OuterChecksum = checksum
			}
			entry := out[reg.Name]
			entry.Name, entry.Repository = reg.Name, reg.Repository
			entry.Advisories = reg.Advisories
			entry.Releases = append(entry.Releases, release)
			out[reg.Name] = entry
		}
		if entry, ok := out[reg.Name]; ok {
			out[reg.Name] = compactAdvisories(entry)
		}
		return out, metadata, nil
	}
	_, versions, err := hexfmt.DecodeVersions(data, key)
	if err != nil {
		return nil, source.Result{}, upstreamFailure()
	}
	for _, reg := range versions {
		for _, release := range reg.Releases {
			p, ok := hexfmt.TarballPath(reg.Name, release.Version)
			if !ok {
				return nil, source.Result{}, upstreamFailure()
			}
			if !h.k.Can(r, p, types.ActionRead) {
				continue
			}
			if err := metadata.CheckRead(r.Context(), p); errors.Is(err, source.ErrDenied) {
				continue
			} else if err != nil {
				return nil, source.Result{}, fmt.Errorf("hex: check remote release read: %w", err)
			}
			entry := out[reg.Name]
			entry.Name, entry.Repository = reg.Name, pkgroute.RequestFrom(r).Repo.Key
			entry.Releases = append(entry.Releases, release)
			out[reg.Name] = entry
		}
	}
	return out, metadata, nil
}

func splitTarball(path string) (string, string, bool) {
	base, ok := strings.CutPrefix(path, "tarballs/")
	if !ok || !strings.HasSuffix(base, ".tar") {
		return "", "", false
	}
	base = strings.TrimSuffix(base, ".tar")
	for at := strings.IndexByte(base, '-'); at >= 0; {
		name, version := base[:at], base[at+1:]
		if p, valid := hexfmt.TarballPath(name, version); valid && p == path {
			return name, version, true
		}
		next := strings.IndexByte(base[at+1:], '-')
		if next < 0 {
			break
		}
		at += next + 1
	}
	return "", "", false
}

func (h handler) remoteTarball(w http.ResponseWriter, r *http.Request, path string) {
	result, err := h.remoteResult(r, path)
	if errors.Is(err, store.ErrNotFound) {
		h.fail(w, r, http.StatusNotFound, "Hex tarball not found")
		return
	}
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	h.k.ServeResult(w, r, result)
}

func (h handler) remoteResult(r *http.Request, path string) (source.Result, error) {
	name, version, ok := splitTarball(path)
	if !ok {
		return source.Result{}, store.ErrNotFound
	}
	entries, metadata, err := h.remotePackagesWithAuthority(r, "packages/"+name)
	if err != nil {
		return source.Result{}, err
	}
	reg, ok := entries[name]
	if !ok {
		return source.Result{}, store.ErrNotFound
	}
	var expected hexfmt.Package
	for _, release := range reg.Releases {
		if release.Version == version {
			expected = release
			break
		}
	}
	if expected.Version == "" {
		return source.Result{}, store.ErrNotFound
	}
	resource := tarballResource(r, path, name, version, expected)
	result, err := h.k.Read(r, resource)
	if err != nil {
		return source.Result{}, fmt.Errorf("hex: read remote tarball: %w", err)
	}
	if _, err := h.validateRemoteTarball(r, &result, name, version, expected); err != nil {
		if result.Stream != nil && result.Stream.Body != nil {
			_ = result.Stream.Body.Close()
		}
		return source.Result{}, err
	}
	prior := result.Revalidate
	result.Revalidate = func(ctx context.Context) error {
		if prior != nil {
			if err := prior(ctx); err != nil {
				return fmt.Errorf("hex: revalidate tarball: %w", err)
			}
		}
		if err := metadata.Revalidate(ctx); err != nil {
			return fmt.Errorf("hex: revalidate tarball metadata: %w", err)
		}
		if err := metadata.CheckRead(ctx, path); err != nil {
			return fmt.Errorf("hex: check tarball read: %w", err)
		}
		return nil
	}
	return result, nil
}

func tarballResource(
	r *http.Request,
	path, name, version string,
	expected hexfmt.Package,
) source.Resource {
	resource := pkgroute.Resource(r, path, "")
	resource.Method = http.MethodGet
	resource.Query, resource.Accept, resource.Range, resource.IfRange = "", "", "", ""
	resource.Variant = "hex-tarball-v1"
	resource.Validate = func(ctx context.Context, body io.ReadSeeker, size int64) error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("hex: validate remote tarball context: %w", err)
		}
		if size <= 0 || size > hexfmt.MaxTarballBytes {
			return source.ErrInvalidResult
		}
		pkg, err := hexfmt.InspectTarball(body)
		if err != nil || pkg.Name != name || pkg.Version != version ||
			pkg.InnerChecksum != expected.InnerChecksum ||
			expected.OuterChecksum != [32]byte{} && pkg.OuterChecksum != expected.OuterChecksum {
			return source.ErrInvalidResult
		}
		return nil
	}
	return resource
}

func (h handler) legacyOuterChecksum(
	r *http.Request,
	path string,
	expected hexfmt.Package,
	metadata source.Result,
) ([32]byte, error) {
	var zero [32]byte
	resource := tarballResource(r, path, expected.Name, expected.Version, expected)
	result, err := h.k.Read(r, resource)
	if err != nil {
		return zero, fmt.Errorf("hex: read legacy tarball: %w", err)
	}
	if result.Stream != nil && result.Stream.Body != nil {
		defer result.Stream.Body.Close()
	}
	pkg, err := h.validateRemoteTarball(r, &result, expected.Name, expected.Version, expected)
	if err != nil {
		return zero, err
	}
	if result.Revalidate != nil {
		if err := result.Revalidate(r.Context()); err != nil {
			return zero, fmt.Errorf("hex: revalidate legacy tarball: %w", err)
		}
	}
	if err := metadata.Revalidate(r.Context()); err != nil {
		return zero, fmt.Errorf("hex: revalidate legacy metadata: %w", err)
	}
	if err := metadata.CheckRead(r.Context(), path); err != nil {
		return zero, fmt.Errorf("hex: check legacy tarball read: %w", err)
	}
	return pkg.OuterChecksum, nil
}

// Validate at the protocol boundary as well as in the HTTP source adapter.
// Cache backends and future adapters must never bypass the signed registry's
// content identity and checksum checks.
func (h handler) validateRemoteTarball(
	r *http.Request,
	result *source.Result,
	name, version string,
	expected hexfmt.Package,
) (hexfmt.Package, error) {
	var body io.ReadCloser
	switch {
	case result.Stream != nil && result.Stream.Body != nil:
		if result.Stream.Status != http.StatusOK {
			return hexfmt.Package{}, upstreamFailure()
		}
		body = result.Stream.Body
	case result.Item != nil:
		var err error
		body, _, err = h.k.Blobs().Open(r.Context(), result.Item.Actual.SHA256)
		if err != nil {
			return hexfmt.Package{}, fmt.Errorf("hex: open cached tarball: %w", err)
		}
	default:
		return hexfmt.Package{}, upstreamFailure()
	}
	data, err := io.ReadAll(io.LimitReader(body, hexfmt.MaxTarballBytes+1))
	_ = body.Close()
	if err != nil || len(data) == 0 || len(data) > hexfmt.MaxTarballBytes {
		return hexfmt.Package{}, upstreamFailure()
	}
	pkg, err := hexfmt.InspectTarball(bytes.NewReader(data))
	if err != nil || pkg.Name != name || pkg.Version != version ||
		pkg.InnerChecksum != expected.InnerChecksum ||
		expected.OuterChecksum != [32]byte{} && pkg.OuterChecksum != expected.OuterChecksum {
		return hexfmt.Package{}, upstreamFailure()
	}
	if result.Stream != nil {
		result.Stream.Body = io.NopCloser(bytes.NewReader(data))
		result.Stream.Size = int64(len(data))
	}
	return pkg, nil
}
