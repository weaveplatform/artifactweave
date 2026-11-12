package itemkit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/alpine"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/cran"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/debian"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/maven"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/opkg"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/p2"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/rpm"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// getRemoteItem returns false for existing folder/archive browsing, which remains
// cache-only. Native adapters and virtual member fetching require their own opt-in.
func (s *Kit) getRemoteItem(w http.ResponseWriter, r *http.Request, c repo.Config,
	req ItemRequest, p kernel.Principal, acc Access,
) bool {
	ctx := r.Context()
	if strings.HasSuffix(req.Key, model.CacheSuffix) && maven.Serves(c.PackageType) &&
		!maven.IsMetadata(req.Path) {
		return false
	}
	existing, err := s.Store.Items().Get(ctx, c.Key, true, req.Path)
	if err == nil && existing.Folder {
		return false
	}
	if err != nil && !errors.Is(err, kernel.ErrNotFound) {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "cached item", err))
		return true
	}
	if errors.Is(err, kernel.ErrNotFound) && strings.Contains(req.Path, "!/") {
		return false
	}
	resource := RemoteResource(r, req.Path)
	sidecar := ""
	if artifact, alg, ok := item.Sidecar(req.Path); ok && errors.Is(err, kernel.ErrNotFound) {
		// A checksum cannot disclose a file the caller cannot read. Cold SHA-256
		// retrieval is an explicit transient operation; other cold sidecars stay404.
		if !acc.File(c, artifact, accessapi.ActionRead) {
			httpkit.WriteErr(w, r, ItemNotFound())
			return true
		}
		cached, getErr := s.Store.Items().Get(ctx, c.Key, true, artifact)
		switch {
		case maven.Serves(c.PackageType):
			sidecar = string(alg)
			resource.Path = artifact
			resource.EscapedPath = strings.TrimSuffix(resource.EscapedPath, "."+sidecar)
		case getErr == nil && !cached.Folder:
			sidecar = string(alg)
			resource.Path = artifact
			resource.EscapedPath = strings.TrimSuffix(resource.EscapedPath, "."+sidecar)
		case errors.Is(getErr, kernel.ErrNotFound) && string(alg) == "sha256" &&
			c.RetrieveSha256FromServer != nil && *c.RetrieveSha256FromServer:
			resource.ChecksumOnly = true
		default:
			httpkit.WriteErr(w, r, ItemNotFound())
			return true
		}
	}
	query := source.Request{
		Repo:      c,
		Subject:   accessapi.SubjectOf(p),
		Access:    acc.Decider,
		Resource:  resource,
		Matrix:    req.matrix(),
		Project:   c.ProjectKey,
		CacheOnly: strings.HasSuffix(req.Key, model.CacheSuffix),
	}
	query, err = s.formatRemoteSourceRequest(ctx, query, acc)
	if err != nil {
		httpkit.WriteErr(w, r, pkgroute.SourceError(err, p.Anonymous))
		return true
	}
	resource = query.Resource
	result, err := s.RemoteSources.Read(ctx, query)
	if err != nil {
		httpkit.WriteErr(
			w,
			r,
			s.ItemError(ctx, "read source", pkgroute.SourceError(err, p.Anonymous)),
		)
		return true
	}
	if result.Stream != nil {
		s.ServeRemoteStream(w, r, result, c, req, resource, sidecar, p.Anonymous)
		return true
	}
	check := req
	check.Path = resource.Path
	ok, matchErr := matchesMatrix(ctx, s.Store, *result.Item, check)
	switch {
	case matchErr != nil:
		httpkit.WriteErr(w, r, s.InternalError(ctx, "item properties", matchErr))
	case !ok:
		httpkit.WriteErr(w, r, ItemNotFound())
	default:
		if !s.RemoteDeliveryAllowed(w, r, result, p.Anonymous) {
			return true
		}
		if sidecar != "" {
			sw := &StatusWriter{ResponseWriter: w}
			s.serveRemoteChecksum(
				sw,
				r,
				item.ServedChecksum(c, *result.Item, item.Algorithm(sidecar)),
				result.Item.Modified,
			)
			if sw.Complete(r) && result.Used != nil {
				_ = result.Used(ctx)
			}
		} else {
			s.ServeFile(w, r, s.Store, Found{Repo: c, Item: *result.Item, Used: result.Used}, p)
		}
	}
	return true
}

func (s *Kit) formatRemoteSourceRequest(
	ctx context.Context, q source.Request, acc Access,
) (source.Request, error) {
	if maven.Serves(q.Repo.PackageType) {
		return s.mavenSourceRequest(ctx, q, acc)
	}
	if alpine.Serves(q.Repo.PackageType) {
		return AlpineSourceRequest(q)
	}
	if cran.Serves(q.Repo.PackageType) {
		return CranSourceRequest(q)
	}
	if debian.Serves(q.Repo.PackageType) {
		return DebianSourceRequest(q)
	}
	if rpm.Serves(q.Repo.PackageType) {
		return RPMSourceRequest(q)
	}
	if opkg.Serves(q.Repo.PackageType) {
		return OpkgSourceRequest(q)
	}
	if q.Repo.PackageType == repo.P2 {
		return P2SourceRequest(q)
	}
	return q, nil
}

// P2SourceRequest admits only the P2 resources p2.Classify names, without
// a query or range, and validates their bytes before they are served or
// cached; an index or metadata file is metadata to the source boundary.
func P2SourceRequest(q source.Request) (source.Request, error) {
	name := q.Resource.Path
	kind := p2.Classify(name)
	if kind == p2.KindInvalid {
		return q, kernel.ErrNotFound
	}
	if q.Resource.Query != "" || q.Resource.Range != "" {
		return q, source.ErrInvalidRequest
	}
	q.Resource.Variant = "p2-native-v1"
	if kind == p2.KindIndex || kind == p2.KindMetadata {
		q.Resource.Kind = source.ResourceKindMetadata
		q.Resource.ValidateMetadata = func(_ context.Context, r io.ReadSeeker, size int64,
			_ source.MetadataResponse,
		) error {
			if err := p2.Validate(name, r, size); err != nil {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.FailureKindIntegrity,
				}
			}
			return nil
		}
	} else {
		q.Resource.Validate = func(_ context.Context, r io.ReadSeeker, size int64) error {
			if err := p2.Validate(name, r, size); err != nil {
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

// ServeRemoteStream answers an item GET or HEAD from a remote source's
// result: the stream itself, or the checksum sidecar of the file it
// describes, once RemoteDeliveryAllowed admits it. A use is recorded only
// for a complete response.
func (s *Kit) ServeRemoteStream(
	w http.ResponseWriter,
	r *http.Request,
	result source.Result,
	c repo.Config,
	req ItemRequest,
	resource source.Resource,
	sidecar string,
	anonymous bool,
) {
	ctx := r.Context()

	if !req.matrix().Matches(nil) {
		_ = result.Stream.Body.Close()
		httpkit.WriteErr(w, r, ItemNotFound())
		return
	}
	if !s.RemoteDeliveryAllowed(w, r, result, anonymous) {
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
		sw := &StatusWriter{ResponseWriter: w}
		s.serveRemoteChecksum(sw, r, checksum, result.Stream.Modified)
		if sw.Complete(r) && result.Used != nil {
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
		result.Stream.Checksums = kernel.Checksums{}
		result.Stream.ContentRange = ""
	}
	sw := &StatusWriter{ResponseWriter: w}
	if err := pkgroute.ServeStream(sw, r, result.Stream); err != nil {
		httpkit.NoteCause(ctx, err)
		panic(http.ErrAbortHandler)
	}
	if sw.Complete(r) && result.Used != nil {
		_ = result.Used(ctx)
	}
}

// RemoteDeliveryAllowed revalidates a result that asks for it before it is
// delivered, and on a failure closes its stream, answers with the source
// error and reports false.
func (s *Kit) RemoteDeliveryAllowed(
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
			httpkit.WriteErr(w, r, pkgroute.SourceError(err, anonymous))
			return false
		}
	}
	return true
}

// RemoteResource is the source resource an item request for path names,
// with the query the client sent less the controls that only this server
// reads, which are never forwarded upstream.
func RemoteResource(r *http.Request, path string) source.Resource {
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

// AlpineSourceRequest refuses a query on an Alpine package or index, and
// validates a remote APKINDEX as metadata before it is served or cached.
func AlpineSourceRequest(q source.Request) (source.Request, error) {
	path := q.Resource.Path
	if q.Resource.Query != "" &&
		(strings.HasSuffix(path, ".apk") || strings.HasSuffix(path, "/"+alpine.IndexName)) {
		return q, source.ErrInvalidRequest
	}
	if strings.HasSuffix(path, "/"+alpine.IndexName) {
		if _, _, _, ok := alpine.ParseIndexPath(path); !ok {
			return q, kernel.ErrNotFound
		}
		q.Resource.Kind = source.ResourceKindMetadata
		q.Resource.PermissionFolder = true
		q.Resource.Variant = "alpine-index-v2"
		q.Resource.ValidateMetadata = func(ctx context.Context, r io.ReadSeeker, size int64,
			_ source.MetadataResponse,
		) error {
			if size <= 0 || size > 36<<20 {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.FailureKindIntegrity,
				}
			}
			body, err := io.ReadAll(io.LimitReader(r, 36<<20+1))
			if err != nil || int64(len(body)) != size {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.FailureKindIntegrity,
				}
			}
			if _, err := alpine.DecodeIndex(ctx, body, path); err != nil {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.FailureKindIntegrity,
				}
			}
			return nil
		}
	}
	if strings.HasSuffix(path, ".apk") {
		if _, _, _, _, ok := alpine.ParsePath(path); !ok {
			return q, kernel.ErrNotFound
		}
		q.Resource.Variant = "alpine-package-v2"
		q.Resource.Validate = func(ctx context.Context, r io.ReadSeeker, size int64) error {
			if _, err := alpine.ReadPackage(ctx, r, size, path); err != nil {
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

// CranSourceRequest validates a remote CRAN index as metadata and a
// package with the parser local uploads go through, refuses a query on
// either, and answers the paths CRAN never serves remotely as not found.
func CranSourceRequest(q source.Request) (source.Request, error) {
	p := q.Resource.Path
	if feed, _, ok := cran.IndexPath(p); ok {
		if q.Resource.Query != "" {
			return q, source.ErrInvalidRequest
		}
		q.Resource.Kind = source.ResourceKindMetadata
		q.Resource.PermissionFolder = true
		q.Resource.PermissionPath = feed
		q.Resource.Variant = "cran-packages-v1"
		q.Resource.ValidateMetadata = func(_ context.Context, body io.ReadSeeker, size int64,
			_ source.MetadataResponse,
		) error {
			if size < 0 || size > 32<<20 {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.FailureKindIntegrity,
				}
			}
			data, err := io.ReadAll(io.LimitReader(body, size+1))
			if err == nil && int64(len(data)) == size {
				_, err = cran.DecodeIndex(data, p)
			}
			if err != nil || int64(len(data)) != size {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.FailureKindIntegrity,
				}
			}
			return nil
		}
		return q, nil
	}
	if cran.IsPackage(p) {
		if q.Resource.Query != "" {
			return q, source.ErrInvalidRequest
		}
		q.Resource.Variant = "cran-package-v1"
		q.Resource.Validate = func(ctx context.Context, body io.ReadSeeker, size int64) error {
			if _, err := cran.ReadPackage(ctx, body, size, p); err != nil {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.FailureKindIntegrity,
				}
			}
			return nil
		}
		return q, nil
	}
	if strings.HasSuffix(p, "PACKAGES.rds") || strings.Contains(p, "src/contrib/") ||
		strings.HasPrefix(p, "bin/") {
		return q, kernel.ErrNotFound
	}
	return q, nil
}

// OpkgSourceRequest refuses a query on an opkg feed index or package, and
// validates a remote feed index as metadata before it is served or cached.
func OpkgSourceRequest(q source.Request) (source.Request, error) {
	p := q.Resource.Path
	if q.Resource.Query != "" && (opkg.IsIndex(p) || strings.HasSuffix(p, ".ipk")) {
		return q, source.ErrInvalidRequest
	}
	if opkg.IsIndex(p) {
		if _, ok := opkg.FeedPath(p); !ok {
			return q, kernel.ErrNotFound
		}
		q.Resource.Kind = source.ResourceKindMetadata
		q.Resource.PermissionFolder = true
		q.Resource.Variant = "opkg-feed-v1"
		q.Resource.ValidateMetadata = func(_ context.Context, r io.ReadSeeker, size int64,
			_ source.MetadataResponse,
		) error {
			if size <= 0 || size > 36<<20 {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.FailureKindIntegrity,
				}
			}
			body, err := io.ReadAll(io.LimitReader(r, size+1))
			if err != nil || int64(len(body)) != size {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.FailureKindIntegrity,
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
					Kind:   source.FailureKindIntegrity,
				}
			}
			return nil
		}
	}
	if strings.HasSuffix(p, ".ipk") {
		if !opkg.ValidPath(p) {
			return q, kernel.ErrNotFound
		}
		q.Resource.Variant = "opkg-package-v1"
		q.Resource.Validate = func(ctx context.Context, r io.ReadSeeker, size int64) error {
			if _, err := opkg.ReadPackage(ctx, r, size, p); err != nil {
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

// RPMSourceRequest refuses a query on an RPM package or repodata file, and
// validates a remote package and the repodata files before they are served
// or cached.
func RPMSourceRequest(q source.Request) (source.Request, error) {
	p := q.Resource.Path
	if q.Resource.Query != "" && (strings.HasSuffix(p, ".rpm") ||
		strings.Contains(p, "repodata/")) {
		return q, source.ErrInvalidRequest
	}
	if strings.HasSuffix(p, ".rpm") {
		q.Resource.Variant = "rpm-package-v1"
		q.Resource.Validate = func(ctx context.Context, r io.ReadSeeker, size int64) error {
			if _, err := rpm.ReadPackage(ctx, r, size, p); err != nil {
				return RPMSourceIntegrity()
			}
			return nil
		}
		return q, nil
	}
	root, name, ok := strings.Cut(p, "repodata/")
	if !ok {
		return q, nil
	}
	q.Resource.Kind = source.ResourceKindMetadata
	q.Resource.PermissionFolder = true
	switch {
	case name == "repomd.xml":
		q.Resource.Variant = "rpm-repomd-v1"
		q.Resource.ValidateMetadata = func(_ context.Context, r io.ReadSeeker, size int64,
			_ source.MetadataResponse,
		) error {
			if size <= 0 || size > 36<<20 {
				return RPMSourceIntegrity()
			}
			body, err := io.ReadAll(io.LimitReader(r, 36<<20+1))
			if err != nil || int64(len(body)) != size {
				return RPMSourceIntegrity()
			}
			if _, _, err := rpm.DecodeRepomd(body, root); err != nil {
				return RPMSourceIntegrity()
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
				return RPMSourceIntegrity()
			}
			body, err := io.ReadAll(io.LimitReader(r, 36<<20+1))
			if err != nil || int64(len(body)) != size {
				return RPMSourceIntegrity()
			}
			if _, err := rpm.DecodePrimary(body, root, want); err != nil {
				return RPMSourceIntegrity()
			}
			return nil
		}
	}
	return q, nil
}

// RPMSourceIntegrity is the integrity failure, a 502, that a remote RPM
// file which fails validation answers.
func RPMSourceIntegrity() error {
	return &source.HTTPError{Status: http.StatusBadGateway, Kind: source.FailureKindIntegrity}
}

func (s *Kit) serveRemoteChecksum(
	w http.ResponseWriter,
	r *http.Request,
	value string,
	modified time.Time,
) {
	if value == "" {
		httpkit.WriteErr(w, r, ItemNotFound())
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	http.ServeContent(w, r, "", modified, strings.NewReader(value))
}

// Cache alias downloads have no source lookup token. Touch only entries naming
// these exact stored bytes; an invalidated token cannot revive an old generation.
func (s *Kit) recordCacheUse(ctx context.Context, f Found) {
	if f.Used != nil {
		_ = f.Used(ctx)
		return
	}
	if f.Repo.Class != repo.Remote || !f.Item.Cached {
		return
	}
	page := kernel.Page{}
	for {
		entries, next, err := s.Store.RemoteCache().
			List(ctx, f.Repo.Key, f.Item.FullPath(), page)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if entry.ItemID == f.Item.ID && entry.Token.Key.Path == f.Item.FullPath() &&
				entry.Blob.Checksums == f.Item.Actual {
				_ = s.Store.RemoteCache().Touch(ctx, entry.Token, time.Now().UTC())
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
func (s *Kit) serveColdChecksum(
	w http.ResponseWriter,
	r *http.Request,
	stream *source.Stream,
) {
	defer func() { _ = stream.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(stream.Body, 4097))
	value, _, _ := strings.Cut(strings.TrimSpace(string(data)), " ")
	sums, parseErr := item.ParseChecksums(kernel.Checksums{SHA256: value})
	if err != nil || len(data) > 4096 || parseErr != nil || sums.SHA256 == "" {
		httpkit.WriteErr(
			w,
			r,
			pkgroute.SourceError(
				&source.HTTPError{Status: 502, Kind: source.FailureKindIntegrity},
				false,
			),
		)
		return
	}
	s.serveRemoteChecksum(w, r, sums.SHA256, stream.Modified)
}

func (s *Kit) mavenSourceRequest(
	ctx context.Context,
	q source.Request,
	acc Access,
) (source.Request, error) {
	if !acc.File(q.Repo, q.Resource.Path, accessapi.ActionRead) {
		return q, source.ErrDenied
	}
	if err := generic.CheckDownload(q.Repo, q.Resource.Path, false); err != nil {
		return q, fmt.Errorf("resolve maven source: %w", err)
	}
	items := s.Items(s.Store)
	if _, err := items.Resolve(
		ctx,
		q.Repo,
		format.Download{Path: q.Resource.Path, Cached: false},
	); err != nil {
		return q, fmt.Errorf("resolve maven source: %w", err)
	}
	if !q.CacheOnly {
		resolved, err := maven.ResolveMavenSource(ctx, s.RemoteSources, items, q)
		if err != nil {
			return q, fmt.Errorf("resolve maven source: %w", err)
		}
		q.Resource.Path = resolved
		q.Resource.EscapedPath = (&url.URL{Path: resolved}).EscapedPath()
	}
	_, err := items.Resolve(ctx, q.Repo, format.Download{Path: q.Resource.Path, Cached: false})
	if err != nil {
		return q, fmt.Errorf("resolve maven source: %w", err)
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
