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
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
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
	// The item route checks an upstream's label against the client's
	// Accept, and answers a refusal 409, as 7.x's download does.
	resource.ClientMIME = &source.ClientMIME{
		Accept: append([]string(nil), r.Header.Values("Accept")...),
	}
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
			// 7.x never fetches a checksum from the upstream ("Checksums are
			// not downloadable", RemoteRepoBase.getInfo), so this retrieval
			// has no 7.x refusal to answer: its refused label stays a 502.
			resource.ClientMIME = nil
		default:
			httpkit.WriteErr(w, r, ItemNotFound())
			return true
		}
		resource.ChecksumRequest = sidecar != ""
	}
	// The copy the cache already holds is matched before the upstream is
	// asked: a mismatch is not found, and the upstream is not asked, as
	// 7.x's RemoteRepoBase.internalGetInfo returns the PROPERTY_MISMATCH
	// that DbStoringRepoMixin.getInfo answers for a cached copy
	// (shouldReturnCachedResource), in 7.161.26.
	if ok, matchErr := RemoteCopyMatches(ctx, s.Store, c, resource.Path, req); matchErr != nil {
		httpkit.WriteErr(w, r, s.InternalError(ctx, "item properties", matchErr))
		return true
	} else if !ok {
		httpkit.WriteErr(w, r, ItemNotFound())
		return true
	}
	query := source.Request{
		Repo:         c,
		Subject:      accessapi.SubjectOf(p),
		Access:       acc.Decider,
		Resource:     resource,
		Matrix:       req.matrix(),
		MatrixParams: pkgroute.UpstreamMatrix(r.URL.EscapedPath()),
		Project:      c.ProjectKey,
		CacheOnly:    strings.HasSuffix(req.Key, model.CacheSuffix),
	}
	query, err = s.formatRemoteSourceRequest(ctx, query, acc)
	if err != nil {
		httpkit.WriteErr(w, r, pkgroute.SourceError(err, p.Anonymous))
		return true
	}
	resource = query.Resource
	serveStream, err := s.remoteStreamAnswer(ctx, c, sidecar, &query)
	if err != nil {
		httpkit.WriteErr(w, r, s.InternalError(ctx, "cached copy", err))
		return true
	}
	result, err := s.RemoteSources.Read(ctx, query)
	if err != nil {
		httpkit.WriteErr(
			w,
			r,
			s.ItemError(ctx, "read source", pkgroute.SourceError(err, p.Anonymous)),
		)
		return true
	}
	// What the source returns now is the upstream's answer to a URL that
	// carried the matrix parameters, or the copy matched above, so it is
	// served without another match: 7.x's downloadAndSave hands back the
	// copy it just saved, and a remote that stores nothing streams the
	// upstream's answer ("resource will be stream directly to the user"),
	// neither matched against the request's properties (7.161.26).
	if result.Stream != nil {
		serveStream(w, r, result, c, req, resource, sidecar, p.Anonymous)
		return true
	}
	if !s.RemoteDeliveryAllowed(w, r, result, p.Anonymous) {
		return true
	}
	if sidecar != "" {
		sw := &StatusWriter{ResponseWriter: w}
		s.serveRemoteChecksum(
			sw,
			r,
			req.Path,
			item.ServedChecksum(c, *result.Item, item.Algorithm(sidecar)),
			result.Item.Actual,
			result.Item.Modified,
		)
		if sw.Complete(r) && result.Used != nil {
			_ = result.Used(ctx)
		}
	} else {
		s.ServeItemFile(w, r, s.Store, Found{Repo: c, Item: *result.Item, Used: result.Used}, p)
	}
	return true
}

// Matrix returns the request's matrix parameters as a download matches them.
func (r ItemRequest) Matrix() model.Matrix { return r.matrix() }

// MatchesMatrix reports whether it, a stored file, matches req's matrix
// parameters, as a download matches its properties: a key it carries must
// have a value asked for, and a mandatory key ("key+=") must be carried
// (JFrog's guide, Using Properties in Deployment and Resolution; 7.x's
// DbStoringRepoMixin.getInfo). With no parameters it reads nothing.
func MatchesMatrix(ctx context.Context, st Store, it model.Item, req ItemRequest) (bool, error) {
	return matchesMatrix(ctx, st, it, req)
}

// RemoteCopyMatches reports whether the copy of path that the remote
// repository c's cache already holds matches req's matrix parameters: an
// item by its properties, a document the cache keeps without an item by
// none, so that only a mandatory key refuses it. With no copy it reports
// true, since the upstream, asked with the parameters, decides; 7.x
// matches only a cached copy (RemoteRepoBase.internalGetInfo, 7.161.26).
// With no parameters it reads nothing.
func RemoteCopyMatches(
	ctx context.Context, st Store, c repo.Config, path string, req ItemRequest,
) (bool, error) {
	if len(req.Props) == 0 {
		return true, nil
	}
	it, err := st.Items().Get(ctx, c.Key, true, path)
	switch {
	case err == nil && !it.Folder:
		return matchesMatrix(ctx, st, it, req)
	case err != nil && !errors.Is(err, kernel.ErrNotFound):
		return false, fmt.Errorf("cached copy of %s: %w", path, err)
	}
	page := kernel.Page{}
	for {
		entries, next, err := st.RemoteCache().Exact(ctx, c.Key, path, page)
		if err != nil {
			return false, fmt.Errorf("cached document %s: %w", path, err)
		}
		for _, e := range entries {
			if e.ItemID == 0 && e.Blob.Checksums.SHA256 != "" && e.NegativeUntil.IsZero() {
				return req.matrix().Matches(nil), nil
			}
		}
		if next == "" {
			return true, nil
		}
		page.After = next
	}
}

// remoteStreamServer answers an item GET or HEAD from a stream a remote
// source returned, with ServeRemoteStream's parameters.
type remoteStreamServer func(
	w http.ResponseWriter,
	r *http.Request,
	result source.Result,
	c repo.Config,
	req ItemRequest,
	resource source.Resource,
	sidecar string,
	anonymous bool,
)

// remoteStreamAnswer returns how getRemoteItem answers a stream for q:
// ServeRemoteStream, or, for a Maven-family checksum request that reads
// only the info of the file it names, which it makes a HEAD,
// serveInfoChecksum or serveUpstreamChecksum. 7.161.26's
// DownloadServiceImpl.respond hands a checksum request, HEAD or GET, to
// respondForChecksumRequest, which never downloads the file, before the
// branch that downloads one. DownloadRequestContext.getResourcePath strips
// the checksum's extension, so RemoteRepoBase.getInfo resolves the file,
// from the upstream's HEAD when the cache does not hold it. A copy the
// cache holds, even one due for a refresh, is read as before.
func (s *Kit) remoteStreamAnswer(
	ctx context.Context, c repo.Config, sidecar string, q *source.Request,
) (remoteStreamServer, error) {
	if sidecar == "" || !maven.Serves(c.PackageType) || q.CacheOnly {
		return s.ServeRemoteStream, nil
	}
	if c.StoreArtifactsLocally != nil && !*c.StoreArtifactsLocally {
		sum := upstreamChecksumRequest(*q, sidecar)
		q.Resource.Method = http.MethodHead
		return func(
			w http.ResponseWriter, r *http.Request, result source.Result, _ repo.Config,
			req ItemRequest, resource source.Resource, _ string, anonymous bool,
		) {
			s.serveUpstreamChecksum(w, r, result, sum, req, resource, anonymous)
		}, nil
	}
	held, err := remoteCopyHeld(ctx, s.Store, c, q.Resource.Path)
	switch {
	case err != nil:
		return nil, err
	case held:
		return s.ServeRemoteStream, nil
	}
	q.Resource.Method = http.MethodHead
	return s.serveInfoChecksum, nil
}

// remoteCopyHeld reports whether the cache of the remote repository c
// holds a copy of path, fresh or not: a file, or a document it keeps
// without an item, such as Maven's maven-metadata.xml. 7.x's
// RemoteRepoBase.internalGetInfo reads such a copy from the cache before
// it asks the upstream (7.161.26).
func remoteCopyHeld(ctx context.Context, st Store, c repo.Config, path string) (bool, error) {
	it, err := st.Items().Get(ctx, c.Key, true, path)
	switch {
	case err == nil:
		return !it.Folder, nil
	case !errors.Is(err, kernel.ErrNotFound):
		return false, fmt.Errorf("cached copy of %s: %w", path, err)
	}
	page := kernel.Page{}
	for {
		entries, next, err := st.RemoteCache().Exact(ctx, c.Key, path, page)
		if err != nil {
			return false, fmt.Errorf("cached document %s: %w", path, err)
		}
		for _, e := range entries {
			if e.ItemID == 0 && e.Blob.Checksums.SHA256 != "" && e.NegativeUntil.IsZero() {
				return true, nil
			}
		}
		if next == "" {
			return false, nil
		}
		page.After = next
	}
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
// a range, and validates their bytes before they are served or cached; an
// index or metadata file is metadata to the source boundary. A query
// refuses none: the source boundary passes it to the upstream only under
// Propagate Query Params, as for any remote file (#1350, the rule #1349
// settled for Debian from 6.20's HttpRepo.addQueryParams).
func P2SourceRequest(q source.Request) (source.Request, error) {
	name := q.Resource.Path
	kind := p2.Classify(name)
	if kind == p2.KindInvalid {
		return q, kernel.ErrNotFound
	}
	if q.Resource.Range != "" {
		return q, source.ErrInvalidRequest
	}
	q.Resource.Variant = "p2-native-v1"
	if kind == p2.KindIndex || kind == p2.KindMetadata {
		q.Resource.Kind = source.ResourceKindMetadata
		q.Resource.ValidateMetadata = func(ctx context.Context, r io.ReadSeeker, size int64,
			_ source.MetadataResponse,
		) error {
			if err := p2.Validate(ctx, name, r, size); err != nil {
				return &source.HTTPError{
					Status: http.StatusBadGateway,
					Kind:   source.FailureKindIntegrity,
				}
			}
			return nil
		}
	} else {
		q.Resource.Validate = func(ctx context.Context, r io.ReadSeeker, size int64) error {
			if err := p2.Validate(ctx, name, r, size); err != nil {
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
// for a complete response. The request's matrix parameters are not matched
// here: a caller matches a copy the cache held before the read
// (RemoteCopyMatches), and a stream the upstream answered was asked for
// with them.
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
		s.serveRemoteChecksum(sw, r, req.Path, checksum, result.Stream.Checksums,
			result.Stream.Modified)
		if sw.Complete(r) && result.Used != nil {
			_ = result.Used(ctx)
		}
		return
	}
	if resource.ChecksumOnly && r.Method != http.MethodHead {
		s.serveColdChecksum(w, r, req.Path, result.Stream)
		return
	}
	if resource.ChecksumOnly {
		// GET normalizes a checksum plus optional filename to exactly 64 hex
		// characters. HEAD cannot inspect that body, but must describe the same
		// representation rather than the upstream checksum file's byte length,
		// and answer conditional requests as the GET does.
		_ = result.Stream.Body.Close()
		s.serveRemoteChecksum(w, r, req.Path, strings.Repeat("0", 64), kernel.Checksums{},
			result.Stream.Modified)
		return
	}
	// A remote file served from the upstream's stream answers If-Match and
	// If-Unmodified-Since as its cached copy does: 7.x answers a remote
	// download through the same isNotModified.
	r = withoutUnevaluatedPreconditions(r)
	// Whatever the stream holds, a file the upstream sent or a document
	// the cache keeps without an item, the item route serves it as the
	// file at the path asked for, as 7.x's download serves any resource
	// it finds there (RemoteRepoResource, FileResource).
	result.Stream.Path = req.Path
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

// AlpineSourceRequest validates a remote APKINDEX as metadata, and a
// package with the parser local uploads go through, before either is
// served or cached. A query refuses neither: the source boundary passes it
// to the upstream only under Propagate Query Params, as for any remote file
// (#1350, the rule #1349 settled for Debian from 6.20's
// HttpRepo.addQueryParams).
func AlpineSourceRequest(q source.Request) (source.Request, error) {
	path := q.Resource.Path
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
// package with the parser local uploads go through, and answers the paths
// CRAN never serves remotely as not found. A query refuses neither: the
// source boundary passes it to the upstream only under Propagate Query
// Params, as for any remote file (#1350, the rule #1349 settled for Debian
// from 6.20's HttpRepo.addQueryParams).
func CranSourceRequest(q source.Request) (source.Request, error) {
	p := q.Resource.Path
	if feed, _, ok := cran.IndexPath(p); ok {
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

// OpkgSourceRequest validates a remote feed index as metadata before it is
// served or cached, and a package with the parser local uploads go through.
// A query refuses neither: the source boundary passes it to the upstream
// only under Propagate Query Params, as for any remote file (#1350, the
// rule #1349 settled for Debian from 6.20's HttpRepo.addQueryParams).
func OpkgSourceRequest(q source.Request) (source.Request, error) {
	p := q.Resource.Path
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

// RPMSourceRequest validates a remote package and the repodata files
// before they are served or cached. A query refuses neither: the source
// boundary passes it to the upstream only under Propagate Query Params, as
// for any remote file (#1350, the rule #1349 settled for Debian from 6.20's
// HttpRepo.addQueryParams).
func RPMSourceRequest(q source.Request) (source.Request, error) {
	p := q.Resource.Path
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

// serveRemoteChecksum serves value, a remote repository's checksum file
// at name, of a file whose checksums are sums (empty when only the
// checksum is known) and which last changed at modified, as a local one is
// served (serveChecksum): 7.x answers both through the same
// DownloadServiceImpl.respond.
func (s *Kit) serveRemoteChecksum(
	w http.ResponseWriter,
	r *http.Request,
	name, value string,
	sums kernel.Checksums,
	modified time.Time,
) {
	if value == "" {
		httpkit.WriteErr(w, r, ItemNotFound())
		return
	}
	serveChecksum(w, r, name, value, sums, modified)
}

// serveInfoChecksum answers the checksum request at req.Path for the file
// at resource.Path, which the cache does not hold, from result, the
// upstream's answer to a HEAD of that file, as 7.161.26 answers one on a
// remote that stores artifacts locally. RemoteRepoBase.getChecksum reads
// the file's info through the remote's checksum policy
// (localCacheRepo.getChecksum, RepoBase.getChecksum, the cache taking the
// remote's policy), and that info holds only the checksums the upstream
// declared, each as its original value (HttpUtils.getChecksums). pass-thru
// answers the original (ChecksumPolicyPassThru); generate-if-absent, fail
// and ignore-and-generate answer the actual value, which a file never
// downloaded does not have. With no value respondForChecksumRequest
// answers 404 "Checksum not found for <file>", before any conditional
// header is read, as a local checksum file does (getSidecar). A declared
// SHA-512 answers as the other three do (DV-073).
func (s *Kit) serveInfoChecksum(
	w http.ResponseWriter,
	r *http.Request,
	result source.Result,
	c repo.Config,
	req ItemRequest,
	resource source.Resource,
	sidecar string,
	anonymous bool,
) {
	if !s.RemoteDeliveryAllowed(w, r, result, anonymous) {
		return
	}
	_ = result.Stream.Body.Close()
	value := ""
	if c.RemoteRepoChecksumPolicyType == repo.PassThru {
		value = item.Algorithm(sidecar).Of(result.Stream.Checksums)
	}
	if value == "" {
		httpkit.WriteErr(
			w, r, apierror.New(http.StatusNotFound, "Checksum not found for "+resource.Path),
		)
		return
	}
	// The file's headers carry what the upstream declared, as #1459 settled
	// for a checksum HEAD that streams nothing.
	s.serveRemoteChecksum(w, r, req.Path, value, result.Stream.Checksums, result.Stream.Modified)
}

// upstreamChecksumRequest is the read of the checksum file the upstream
// keeps beside the file q reads, <file>.<sidecar>, which 7.161.26's
// RemoteRepoBase.getRemoteChecksum downloads for a Maven-family remote
// that stores nothing (getRemoteChecksumFromPath). HttpRepo.downloadResource
// sends it with a NullRequestContext: none of the client's matrix
// parameters, query or headers.
func upstreamChecksumRequest(q source.Request, sidecar string) source.Request {
	q.Matrix, q.MatrixParams = model.Matrix{}, ""
	q.Resource = source.Resource{
		Kind:         source.ResourceKindArtifact,
		Path:         q.Resource.Path + "." + sidecar,
		EscapedPath:  q.Resource.EscapedPath + "." + sidecar,
		Method:       http.MethodGet,
		ChecksumOnly: true,
		// HttpRepo.downloadResource checks the checksum file's label too
		// (blockMismatchingMimeTypes), with no client Accept to read.
		ClientMIME: &source.ClientMIME{},
	}
	return q
}

// serveUpstreamChecksum answers the checksum request at req.Path on a
// Maven-family remote that stores nothing, once result, the upstream's
// answer to a HEAD of the file at resource.Path, found it: with the first
// token of the upstream's checksum file, which sum reads, under every
// checksum policy, as 7.161.26's RemoteRepoBase.getChecksum answers one
// (getRemoteChecksum, ChecksumUtils.checksumStringFromStream). The file's
// body is never asked for. An upstream with no checksum file, or one
// with no checksum in it, answers 404 "Checksum not found for <file>":
// getChecksum's handleRemoteRequestException returns null for the
// upstream's 404, and respondForChecksumRequest answers that 404. The
// file's headers carry what the upstream declared on its HEAD, as #1459
// settled for a checksum that streams nothing.
func (s *Kit) serveUpstreamChecksum(
	w http.ResponseWriter,
	r *http.Request,
	result source.Result,
	sum source.Request,
	req ItemRequest,
	resource source.Resource,
	anonymous bool,
) {
	if !s.RemoteDeliveryAllowed(w, r, result, anonymous) {
		return
	}
	_ = result.Stream.Body.Close()
	ctx := r.Context()
	fetched, err := s.RemoteSources.Read(ctx, sum)
	value := ""
	switch {
	case err != nil:
	case fetched.Stream == nil:
		err = fmt.Errorf("checksum file %s: %w", sum.Resource.Path, source.ErrInvalidRequest)
	case !s.RemoteDeliveryAllowed(w, r, fetched, anonymous):
		return
	default:
		value, err = checksumFileValue(fetched.Stream.Body)
		_ = fetched.Stream.Body.Close()
	}
	if err != nil && !errors.Is(err, kernel.ErrNotFound) {
		httpkit.WriteErr(
			w,
			r,
			s.ItemError(ctx, "read source", pkgroute.SourceError(err, anonymous)),
		)
		return
	}
	if value == "" {
		httpkit.WriteErr(
			w, r, apierror.New(http.StatusNotFound, "Checksum not found for "+resource.Path),
		)
		return
	}
	s.serveRemoteChecksum(w, r, req.Path, value, result.Stream.Checksums, result.Stream.Modified)
}

// checksumFileValue reads a checksum file as 7.161.26's
// ChecksumUtils.checksumStringFromStream does: the first line that is
// not blank and does not start with "//", trimmed, less anything up to
// a ")= " (an "MD5 (name)= <sum>" line), up to its first white space.
// The value is not checked against the algorithm, as getChecksum does
// not check it. The remote policy holds the file to 4096 bytes
// (ChecksumOnly), so the whole of it is read here.
func checksumFileValue(body io.Reader) (string, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return "", fmt.Errorf("read checksum file: %w", err)
	}
	for line := range strings.Lines(string(data)) {
		if strings.HasPrefix(line, "//") {
			continue
		}
		line = strings.TrimSpace(line)
		if _, after, found := strings.Cut(line, ")= "); found {
			line = after
		}
		if fields := strings.Fields(line); len(fields) > 0 {
			return fields[0], nil
		}
	}
	return "", nil
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
	name string,
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
	s.serveRemoteChecksum(w, r, name, sums.SHA256, kernel.Checksums{}, stream.Modified)
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
