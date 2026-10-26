package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/alpine"
	"github.com/weaveplatform/artifactweave/internal/format/debian"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/format/maven"
	"github.com/weaveplatform/artifactweave/internal/format/opkg"
	"github.com/weaveplatform/artifactweave/internal/format/p2"
	"github.com/weaveplatform/artifactweave/internal/format/rpm"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// getRemoteItem returns false for existing folder/archive browsing, which remains
// cache-only. Native adapters and virtual member fetching require their own opt-in.
func (s artifactoryService) getRemoteItem(w http.ResponseWriter, r *http.Request, c repo.Config,
	req itemRequest, p auth.Principal, acc access,
) bool {
	ctx := r.Context()
	if strings.HasSuffix(req.key, types.CacheSuffix) && maven.Serves(c.PackageType) &&
		!maven.IsMetadata(req.path) {
		return false
	}
	existing, err := s.cfg.Store.Items().Get(ctx, c.Key, true, req.path)
	if err == nil && existing.Folder {
		return false
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeErr(w, r, s.itemError(ctx, "cached item", err))
		return true
	}
	if errors.Is(err, store.ErrNotFound) && strings.Contains(req.path, "!/") {
		return false
	}
	resource := remoteResource(r, req.path)
	sidecar := ""
	if artifact, alg, ok := item.Sidecar(req.path); ok && errors.Is(err, store.ErrNotFound) {
		// A checksum cannot disclose a file the caller cannot read. Cold SHA-256
		// retrieval is an explicit transient operation; other cold sidecars stay404.
		if !acc.file(c, artifact, types.ActionRead) {
			writeErr(w, r, itemNotFound())
			return true
		}
		cached, getErr := s.cfg.Store.Items().Get(ctx, c.Key, true, artifact)
		switch {
		case maven.Serves(c.PackageType):
			sidecar = string(alg)
			resource.Path = artifact
			resource.EscapedPath = strings.TrimSuffix(resource.EscapedPath, "."+sidecar)
		case getErr == nil && !cached.Folder:
			sidecar = string(alg)
			resource.Path = artifact
			resource.EscapedPath = strings.TrimSuffix(resource.EscapedPath, "."+sidecar)
		case errors.Is(getErr, store.ErrNotFound) && string(alg) == "sha256" &&
			c.RetrieveSha256FromServer != nil && *c.RetrieveSha256FromServer:
			resource.ChecksumOnly = true
		default:
			writeErr(w, r, itemNotFound())
			return true
		}
	}
	query := source.Request{
		Repo:      c,
		Subject:   p.Subject(),
		Access:    acc.d,
		Resource:  resource,
		Matrix:    req.matrix(),
		Project:   c.ProjectKey,
		CacheOnly: strings.HasSuffix(req.key, types.CacheSuffix),
	}
	query, err = s.formatRemoteSourceRequest(ctx, query, acc)
	if err != nil {
		writeErr(w, r, pkgroute.SourceError(err, p.Anonymous))
		return true
	}
	resource = query.Resource
	result, err := s.cfg.RemoteSources.Read(ctx, query)
	if err != nil {
		writeErr(w, r, s.itemError(ctx, "read source", pkgroute.SourceError(err, p.Anonymous)))
		return true
	}
	if result.Stream != nil {
		s.serveRemoteStream(w, r, result, c, req, resource, sidecar, p.Anonymous)
		return true
	}
	check := req
	check.path = resource.Path
	ok, matchErr := matchesMatrix(ctx, s.cfg.Store, *result.Item, check)
	switch {
	case matchErr != nil:
		writeErr(w, r, s.internalError(ctx, "item properties", matchErr))
	case !ok:
		writeErr(w, r, itemNotFound())
	default:
		if !s.remoteDeliveryAllowed(w, r, result, p.Anonymous) {
			return true
		}
		if sidecar != "" {
			sw := &statusWriter{ResponseWriter: w}
			s.serveRemoteChecksum(
				sw,
				r,
				item.ServedChecksum(c, *result.Item, item.Algorithm(sidecar)),
				result.Item.Modified,
			)
			if sw.complete(r) && result.Used != nil {
				_ = result.Used(ctx)
			}
		} else {
			s.serveFile(w, r, s.cfg.Store, found{repo: c, item: *result.Item, used: result.Used}, p)
		}
	}
	return true
}

func (s artifactoryService) formatRemoteSourceRequest(
	ctx context.Context, q source.Request, acc access,
) (source.Request, error) {
	if maven.Serves(q.Repo.PackageType) {
		return s.mavenSourceRequest(ctx, q, acc)
	}
	if alpine.Serves(q.Repo.PackageType) {
		return alpineSourceRequest(q)
	}
	if debian.Serves(q.Repo.PackageType) {
		return debianSourceRequest(q)
	}
	if rpm.Serves(q.Repo.PackageType) {
		return rpmSourceRequest(q)
	}
	if opkg.Serves(q.Repo.PackageType) {
		return opkgSourceRequest(q)
	}
	if q.Repo.PackageType == repo.P2 {
		return p2SourceRequest(q)
	}
	return q, nil
}

func p2SourceRequest(q source.Request) (source.Request, error) {
	name := q.Resource.Path
	kind := p2.Classify(name)
	if kind == p2.Invalid {
		return q, store.ErrNotFound
	}
	if q.Resource.Query != "" || q.Resource.Range != "" {
		return q, source.ErrInvalidRequest
	}
	q.Resource.Variant = "p2-native-v1"
	if kind == p2.Index || kind == p2.Metadata {
		q.Resource.Kind = source.Metadata
		q.Resource.ValidateMetadata = func(_ context.Context, r io.ReadSeeker, size int64,
			_ source.MetadataResponse,
		) error {
			if err := p2.Validate(name, r, size); err != nil {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.IntegrityFailure,
				}
			}
			return nil
		}
	} else {
		q.Resource.Validate = func(_ context.Context, r io.ReadSeeker, size int64) error {
			if err := p2.Validate(name, r, size); err != nil {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.IntegrityFailure,
				}
			}
			return nil
		}
	}
	return q, nil
}

func (s artifactoryService) serveRemoteStream(
	w http.ResponseWriter,
	r *http.Request,
	result source.Result,
	c repo.Config,
	req itemRequest,
	resource source.Resource,
	sidecar string,
	anonymous bool,
) {
	ctx := r.Context()

	if !req.matrix().Matches(nil) {
		_ = result.Stream.Body.Close()
		writeErr(w, r, itemNotFound())
		return
	}
	if !s.remoteDeliveryAllowed(w, r, result, anonymous) {
		return
	}
	if sidecar != "" {
		_ = result.Stream.Body.Close()
		checksum := remoteStreamChecksum(c, result.Stream, item.Algorithm(sidecar))
		if r.Method == http.MethodHead && checksum == "" {
			// A cold owner HEAD proves existence but cannot compute a digest.
			// Describe the fixed-length checksum representation without a body
			// or admitting the owner merely to answer HEAD.
			size := map[string]int{"md5": 32, "sha1": 40, "sha256": 64, "sha512": 128}[sidecar]
			checksum = strings.Repeat("0", size)
		}
		sw := &statusWriter{ResponseWriter: w}
		s.serveRemoteChecksum(sw, r, checksum, result.Stream.Modified)
		if sw.complete(r) && result.Used != nil {
			_ = result.Used(ctx)
		}
		return
	}
	if resource.ChecksumOnly && r.Method != http.MethodHead {
		s.serveColdChecksum(w, r, result.Stream)
		return
	}
	if resource.ChecksumOnly {
		// GET normalizes a checksum plus optional filename to exactly 64 hex
		// characters. HEAD cannot inspect that body, but must describe the same
		// representation rather than the upstream checksum file's byte length.
		result.Stream.Size = 64
		result.Stream.MediaType = "text/plain"
		result.Stream.ETag = ""
		result.Stream.Checksums = types.Checksums{}
		result.Stream.ContentRange = ""
	}
	sw := &statusWriter{ResponseWriter: w}
	if err := pkgroute.ServeStream(sw, r, result.Stream); err != nil {
		noteCause(ctx, err)
		panic(http.ErrAbortHandler)
	}
	if sw.complete(r) && result.Used != nil {
		_ = result.Used(ctx)
	}
}

func (s artifactoryService) remoteDeliveryAllowed(
	w http.ResponseWriter,
	r *http.Request,
	result source.Result,
	anonymous bool,
) bool {
	if result.Revalidate != nil {
		if err := result.Revalidate(r.Context()); err != nil {
			if result.Stream != nil {
				_ = result.Stream.Body.Close()
			}
			writeErr(w, r, pkgroute.SourceError(err, anonymous))
			return false
		}
	}
	return true
}

func remoteResource(r *http.Request, path string) source.Resource {
	escapedKey, _, _ := strings.Cut(strings.TrimPrefix(r.URL.EscapedPath(), "/artifactory/"), "/")
	resource := pkgroute.Resource(
		r,
		path,
		pkgroute.EscapedResourcePath(r, "/artifactory/"+escapedKey+"/"),
	)
	// Strip local controls without normalizing signed query bytes or ordering.
	segments := strings.Split(r.URL.RawQuery, "&")
	kept := segments[:0]
	for _, segment := range segments {
		rawKey, _, _ := strings.Cut(segment, "=")
		key, _ := url.QueryUnescape(rawKey)
		switch key {
		case "trace", "skipUpdateStats", "content", "download":
			continue
		}
		kept = append(kept, segment)
	}
	resource.Query = strings.Join(kept, "&")
	return resource
}

func alpineSourceRequest(q source.Request) (source.Request, error) {
	path := q.Resource.Path
	if q.Resource.Query != "" &&
		(strings.HasSuffix(path, ".apk") || strings.HasSuffix(path, "/"+alpine.IndexName)) {
		return q, source.ErrInvalidRequest
	}
	if strings.HasSuffix(path, "/"+alpine.IndexName) {
		if _, _, _, ok := alpine.ParseIndexPath(path); !ok {
			return q, store.ErrNotFound
		}
		q.Resource.Kind = source.Metadata
		q.Resource.PermissionFolder = true
		q.Resource.Variant = "alpine-index-v2"
		q.Resource.ValidateMetadata = func(ctx context.Context, r io.ReadSeeker, size int64,
			_ source.MetadataResponse,
		) error {
			if size <= 0 || size > 36<<20 {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.IntegrityFailure,
				}
			}
			body, err := io.ReadAll(io.LimitReader(r, 36<<20+1))
			if err != nil || int64(len(body)) != size {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.IntegrityFailure,
				}
			}
			if _, err := alpine.DecodeIndex(ctx, body, path); err != nil {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.IntegrityFailure,
				}
			}
			return nil
		}
	}
	if strings.HasSuffix(path, ".apk") {
		if _, _, _, _, ok := alpine.ParsePath(path); !ok {
			return q, store.ErrNotFound
		}
		q.Resource.Variant = "alpine-package-v2"
		q.Resource.Validate = func(ctx context.Context, r io.ReadSeeker, size int64) error {
			if _, err := alpine.ReadPackage(ctx, r, size, path); err != nil {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.IntegrityFailure,
				}
			}
			return nil
		}
	}
	return q, nil
}

// debianSourceRequest admits remote package bytes only after the same bounded
// parser used for local uploads has checked them. A .dsc is verified before it
// can enter the cache; the Sources index additionally checks its parts.
func debianSourceRequest(q source.Request) (source.Request, error) {
	p := q.Resource.Path
	switch {
	case debian.ReleaseMetadataPath(p):
		if q.Resource.Query != "" {
			return q, source.ErrInvalidRequest
		}
		q.Resource.Kind = source.Metadata
		q.Resource.Variant = "debian-release-v1"
		q.Resource.ValidateMetadata = func(
			_ context.Context, r io.ReadSeeker, size int64, _ source.MetadataResponse,
		) error {
			return validateDebianRemoteRelease(p, r, size)
		}
	case strings.Contains(p, "/by-hash/SHA256/"):
		expected, ok := debian.ByHashMetadataPath(p)
		if !ok || q.Resource.Query != "" {
			return q, store.ErrNotFound
		}
		q.Resource.Kind = source.Metadata
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
			return q, store.ErrNotFound
		}
		q.Resource.Kind = source.Metadata
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
					Kind:   source.IntegrityFailure,
				}
			}
			body, err := io.ReadAll(io.LimitReader(r, 64<<20+1))
			if err != nil || int64(len(body)) != size {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.IntegrityFailure,
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
					Kind:   source.IntegrityFailure,
				}
			}
			return nil
		}
	case strings.HasSuffix(p, ".deb") || strings.HasSuffix(p, ".ddeb"):
		if !debian.ValidBinaryPath(p) {
			return q, store.ErrNotFound
		}
		q.Resource.Variant = "debian-binary-v1"
		q.Resource.Validate = func(ctx context.Context, r io.ReadSeeker, size int64) error {
			if _, err := debian.ReadBinary(ctx, seekReaderAt{r}, size); err != nil {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.IntegrityFailure,
				}
			}
			return nil
		}
	case strings.HasSuffix(p, ".dsc"):
		if !debian.ValidSourcePath(p) {
			return q, store.ErrNotFound
		}
		q.Resource.Variant = "debian-source-v1"
		q.Resource.Validate = func(ctx context.Context, r io.ReadSeeker, size int64) error {
			if _, err := debian.ReadSource(ctx, r, size); err != nil {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.IntegrityFailure,
				}
			}
			return nil
		}
	}
	return q, nil
}

func opkgSourceRequest(q source.Request) (source.Request, error) {
	p := q.Resource.Path
	if q.Resource.Query != "" && (opkg.IsIndex(p) || strings.HasSuffix(p, ".ipk")) {
		return q, source.ErrInvalidRequest
	}
	if opkg.IsIndex(p) {
		if _, ok := opkg.FeedPath(p); !ok {
			return q, store.ErrNotFound
		}
		q.Resource.Kind = source.Metadata
		q.Resource.PermissionFolder = true
		q.Resource.Variant = "opkg-feed-v1"
		q.Resource.ValidateMetadata = func(_ context.Context, r io.ReadSeeker, size int64,
			_ source.MetadataResponse,
		) error {
			if size <= 0 || size > 36<<20 {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.IntegrityFailure,
				}
			}
			body, err := io.ReadAll(io.LimitReader(r, size+1))
			if err != nil || int64(len(body)) != size {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.IntegrityFailure,
				}
			}
			switch path.Base(p) {
			case opkg.PackagesName, opkg.PackagesGzipName:
				_, err = opkg.DecodeIndex(body, p)
			case opkg.ReleaseName:
				err = opkg.ValidateRelease(body)
			case opkg.SignatureName:
				if !bytes.HasPrefix(body, []byte("-----BEGIN PGP SIGNATURE-----")) &&
					!bytes.HasPrefix(body, []byte("untrusted comment:")) {
					err = source.ErrInvalidResult
				}
			}
			if err != nil {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.IntegrityFailure,
				}
			}
			return nil
		}
	}
	if strings.HasSuffix(p, ".ipk") {
		if !opkg.ValidPath(p) {
			return q, store.ErrNotFound
		}
		q.Resource.Variant = "opkg-package-v1"
		q.Resource.Validate = func(ctx context.Context, r io.ReadSeeker, size int64) error {
			if _, err := opkg.ReadPackage(ctx, r, size, p); err != nil {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.IntegrityFailure,
				}
			}
			return nil
		}
	}
	return q, nil
}

func rpmSourceRequest(q source.Request) (source.Request, error) {
	p := q.Resource.Path
	if q.Resource.Query != "" && (strings.HasSuffix(p, ".rpm") ||
		strings.Contains(p, "repodata/")) {
		return q, source.ErrInvalidRequest
	}
	if strings.HasSuffix(p, ".rpm") {
		q.Resource.Variant = "rpm-package-v1"
		q.Resource.Validate = func(ctx context.Context, r io.ReadSeeker, size int64) error {
			if _, err := rpm.ReadPackage(ctx, r, size, p); err != nil {
				return rpmSourceIntegrity()
			}
			return nil
		}
		return q, nil
	}
	root, name, ok := strings.Cut(p, "repodata/")
	if !ok {
		return q, nil
	}
	q.Resource.Kind = source.Metadata
	q.Resource.PermissionFolder = true
	switch {
	case name == "repomd.xml":
		q.Resource.Variant = "rpm-repomd-v1"
		q.Resource.ValidateMetadata = func(_ context.Context, r io.ReadSeeker, size int64,
			_ source.MetadataResponse,
		) error {
			if size <= 0 || size > 36<<20 {
				return rpmSourceIntegrity()
			}
			body, err := io.ReadAll(io.LimitReader(r, 36<<20+1))
			if err != nil || int64(len(body)) != size {
				return rpmSourceIntegrity()
			}
			if _, _, err := rpm.DecodeRepomd(body, root); err != nil {
				return rpmSourceIntegrity()
			}
			return nil
		}
	case rpm.IsPrimaryMetadataPath(name) && len(name) > 64:
		want := name[:64]
		q.Resource.Variant = "rpm-primary-v1"
		q.Resource.ValidateMetadata = func(_ context.Context, r io.ReadSeeker, size int64,
			_ source.MetadataResponse,
		) error {
			if size <= 0 || size > 36<<20 {
				return rpmSourceIntegrity()
			}
			body, err := io.ReadAll(io.LimitReader(r, 36<<20+1))
			if err != nil || int64(len(body)) != size {
				return rpmSourceIntegrity()
			}
			if _, err := rpm.DecodePrimary(body, root, want); err != nil {
				return rpmSourceIntegrity()
			}
			return nil
		}
	}
	return q, nil
}

func rpmSourceIntegrity() error {
	return &source.HTTPError{Status: http.StatusBadGateway, Kind: source.IntegrityFailure}
}

func readDebianRemoteMetadata(r io.ReadSeeker, size, limit int64) ([]byte, error) {
	if size <= 0 || size > limit {
		return nil, &source.HTTPError{Status: http.StatusBadGateway, Kind: source.IntegrityFailure}
	}
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil || int64(len(body)) != size {
		return nil, &source.HTTPError{Status: http.StatusBadGateway, Kind: source.IntegrityFailure}
	}
	return body, nil
}

func validateDebianRemoteRelease(path string, r io.ReadSeeker, size int64) error {
	body, err := readDebianRemoteMetadata(r, size, 4<<20)
	if err != nil {
		return err
	}
	if debian.ValidateReleaseMetadata(path, body) != nil {
		return &source.HTTPError{Status: http.StatusBadGateway, Kind: source.IntegrityFailure}
	}
	return nil
}

func validateDebianRemoteHash(expected string, r io.ReadSeeker, size int64) error {
	body, err := readDebianRemoteMetadata(r, size, 64<<20)
	if err != nil {
		return err
	}
	if fmt.Sprintf("%x", sha256.Sum256(body)) != expected {
		return &source.HTTPError{Status: http.StatusBadGateway, Kind: source.IntegrityFailure}
	}
	return nil
}

type seekReaderAt struct{ io.ReadSeeker }

func (r seekReaderAt) ReadAt(p []byte, offset int64) (int, error) {
	if _, err := r.Seek(offset, io.SeekStart); err != nil {
		return 0, fmt.Errorf("seek Debian remote archive: %w", err)
	}
	n, err := io.ReadFull(r, p)
	if err != nil {
		return n, fmt.Errorf("read Debian remote archive: %w", err)
	}
	return n, nil
}

func (s artifactoryService) serveRemoteChecksum(
	w http.ResponseWriter,
	r *http.Request,
	value string,
	modified time.Time,
) {
	if value == "" {
		writeErr(w, r, itemNotFound())
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	http.ServeContent(w, r, "", modified, strings.NewReader(value))
}

// Cache alias downloads have no source lookup token. Touch only entries naming
// these exact stored bytes; an invalidated token cannot revive an old generation.
func (s artifactoryService) recordCacheUse(ctx context.Context, f found) {
	if f.used != nil {
		_ = f.used(ctx)
		return
	}
	if f.repo.Class != repo.Remote || !f.item.Cached {
		return
	}
	page := store.Page{}
	for {
		entries, next, err := s.cfg.Store.RemoteCache().
			List(ctx, f.repo.Key, f.item.FullPath(), page)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if entry.ItemID == f.item.ID && entry.Token.Key.Path == f.item.FullPath() &&
				entry.Blob.Checksums == f.item.Actual {
				_ = s.cfg.Store.RemoteCache().Touch(ctx, entry.Token, time.Now().UTC())
			}
		}
		if next == "" {
			return
		}
		page.After = next
	}
}

// Match checksum uploads: a hex digest, optionally followed by a filename.
// The digest describes an uncached artifact; this validates syntax, not its bytes.
func (s artifactoryService) serveColdChecksum(
	w http.ResponseWriter,
	r *http.Request,
	stream *source.Stream,
) {
	defer func() { _ = stream.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(stream.Body, 4097))
	value, _, _ := strings.Cut(strings.TrimSpace(string(data)), " ")
	sums, parseErr := item.ParseChecksums(types.Checksums{SHA256: value})
	if err != nil || len(data) > 4096 || parseErr != nil || sums.SHA256 == "" {
		writeErr(
			w,
			r,
			pkgroute.SourceError(
				&source.HTTPError{Status: 502, Kind: source.IntegrityFailure},
				false,
			),
		)
		return
	}
	s.serveRemoteChecksum(w, r, sums.SHA256, stream.Modified)
}

func (s artifactoryService) mavenSourceRequest(
	ctx context.Context,
	q source.Request,
	acc access,
) (source.Request, error) {
	if !acc.file(q.Repo, q.Resource.Path, types.ActionRead) {
		return q, source.ErrDenied
	}
	if err := generic.CheckDownload(q.Repo, q.Resource.Path, false); err != nil {
		return q, fmt.Errorf("resolve Maven source: %w", err)
	}
	items := s.items(s.cfg.Store)
	if _, err := items.Resolve(
		ctx,
		q.Repo,
		format.Download{Path: q.Resource.Path, Cached: false},
	); err != nil {
		return q, fmt.Errorf("resolve Maven source: %w", err)
	}
	if !q.CacheOnly {
		resolved, err := remote.ResolveMavenSource(ctx, s.cfg.RemoteSources, items, q)
		if err != nil {
			return q, fmt.Errorf("resolve Maven source: %w", err)
		}
		q.Resource.Path = resolved
		q.Resource.EscapedPath = (&url.URL{Path: resolved}).EscapedPath()
	}
	_, err := items.Resolve(ctx, q.Repo, format.Download{Path: q.Resource.Path, Cached: false})
	if err != nil {
		return q, fmt.Errorf("resolve Maven source: %w", err)
	}
	return q, nil
}

func remoteStreamChecksum(
	c repo.Config,
	stream *source.Stream,
	algorithm item.Algorithm,
) string {
	if c.RemoteRepoChecksumPolicyType == repo.PassThru && maven.Serves(c.PackageType) {
		if original := algorithm.Of(stream.OriginalChecksums); original != "" {
			return original
		}
	}
	return algorithm.Of(stream.Checksums)
}
