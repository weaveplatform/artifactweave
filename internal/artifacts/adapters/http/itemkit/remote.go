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

// remoteRead is getRemoteItem's read of a remote file, or of the file a
// checksum request names, before anything is answered: the source's result
// for resource, or err, which the route answers in its place. infoSum is
// set for a checksum request read from the file's info alone
// (remoteStreamAnswer): the read of the upstream's checksum file that
// serveInfoChecksum may make.
type remoteRead struct {
	err      error
	result   source.Result
	resource source.Resource
	sidecar  string
	infoSum  *source.Request
}

// getRemoteItem returns false for existing folder/archive browsing, which remains
// cache-only. Native adapters and virtual member fetching require their own opt-in.
func (s *Kit) getRemoteItem(w http.ResponseWriter, r *http.Request, c repo.Config,
	req ItemRequest, p kernel.Principal, acc Access,
) bool {
	read, ok := s.readRemoteItem(r, c, req, p, acc, nil)
	if !ok {
		return false
	}
	if read.err != nil {
		httpkit.WriteErr(w, r, read.err)
		return true
	}
	ctx := r.Context()
	result, resource, sidecar := read.result, read.resource, read.sidecar
	// What the source returns now is the upstream's answer to a URL that
	// carried the matrix parameters, or the copy matched above, so it is
	// served without another match: 7.x's downloadAndSave hands back the
	// copy it just saved, and a remote that stores nothing streams the
	// upstream's answer ("resource will be stream directly to the user"),
	// neither matched against the request's properties (7.161.26).
	if result.Stream != nil {
		if read.infoSum != nil {
			s.serveInfoChecksum(w, r, result, c, *read.infoSum, req, resource, sidecar, p.Anonymous)
		} else {
			s.ServeRemoteStream(w, r, result, c, req, resource, sidecar, p.Anonymous)
		}
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
			c,
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

// readRemoteItem makes getRemoteItem's read, which a trace of the same
// request makes too (traceRemoteItem), recording its steps in t when t is
// not nil. It reports false where the route leaves the path to the cache
// lookup.
func (s *Kit) readRemoteItem(r *http.Request, c repo.Config, req ItemRequest,
	p kernel.Principal, acc Access, t *resolutionTrace,
) (remoteRead, bool) {
	ctx := r.Context()
	if strings.HasSuffix(req.Key, model.CacheSuffix) && maven.Serves(c.PackageType) &&
		!maven.IsMetadata(req.Path) {
		return remoteRead{}, false
	}
	existing, err := s.Store.Items().Get(ctx, c.Key, true, req.Path)
	// A remote that reaches this route only for what its cache lookup did
	// not find (fileChecksumRoute, getColdRemoteItem) leaves whatever its
	// cache holds at the path to that lookup, as before.
	if err == nil && (existing.Folder || !remoteDownloadType(c, false)) {
		return remoteRead{}, false
	}
	if err != nil && !errors.Is(err, kernel.ErrNotFound) {
		return remoteRead{err: s.ItemError(ctx, "cached item", err)}, true
	}
	if errors.Is(err, kernel.ErrNotFound) && strings.Contains(req.Path, "!/") {
		return remoteRead{}, false
	}
	resource := RemoteResource(r, req.Path)
	// The item route checks an upstream's label against the client's
	// Accept, and answers a refusal 409, as 7.x's download does.
	resource.ClientMIME = &source.ClientMIME{
		Accept: append([]string(nil), r.Header.Values("Accept")...),
	}
	sidecar := ""
	if artifact, alg, ok := item.Sidecar(req.Path); ok && errors.Is(err, kernel.ErrNotFound) {
		t.stepf("Request is for a checksum")
		// A checksum cannot disclose a file the caller cannot read.
		if !acc.File(c, artifact, accessapi.ActionRead) {
			return remoteRead{err: checksumRefused(c, req, p)}, true
		}
		// Every type resolves a checksum request through the file it names:
		// 7.161.26's DownloadRequestContext.getResourcePath strips the
		// checksum's extension whatever the remote's type, so
		// RemoteRepoBase.getInfo resolves the file, from the upstream's HEAD
		// when the cache does not hold it (#1495).
		cached, getErr := s.Store.Items().Get(ctx, c.Key, true, artifact)
		switch {
		case maven.Serves(c.PackageType), getErr == nil && !cached.Folder,
			errors.Is(getErr, kernel.ErrNotFound):
		default:
			return remoteRead{err: ItemNotFound()}, true
		}
		if cran.Serves(c.PackageType) || opkg.Serves(c.PackageType) {
			// A CRAN or opkg copy the cache holds answers its checksum from
			// the cache lookup, as it did before this route took the cold
			// ones (fileChecksumRoute).
			held, heldErr := remoteCopyHeld(ctx, s.Store, c, artifact)
			if heldErr != nil {
				return remoteRead{err: s.InternalError(ctx, "cached copy", heldErr)}, true
			}
			if held {
				return remoteRead{}, false
			}
		}
		sidecar = string(alg)
		resource.Path = artifact
		resource.EscapedPath = strings.TrimSuffix(resource.EscapedPath, "."+sidecar)
		resource.ChecksumRequest = true
	}
	// The copy the cache already holds is matched before the upstream is
	// asked: a mismatch is not found, and the upstream is not asked, as
	// 7.x's RemoteRepoBase.internalGetInfo returns the PROPERTY_MISMATCH
	// that DbStoringRepoMixin.getInfo answers for a cached copy
	// (shouldReturnCachedResource), in 7.161.26.
	if ok, matchErr := RemoteCopyMatches(ctx, s.Store, c, resource.Path, req); matchErr != nil {
		return remoteRead{err: s.InternalError(ctx, "item properties", matchErr)}, true
	} else if !ok {
		t.stepf("Found resource in local cache, but property doesn't match.")
		return remoteRead{err: mismatched(c, req)}, true
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
		return remoteRead{err: sourceAnswer(c, req, p, err)}, true
	}
	read := remoteRead{resource: query.Resource, sidecar: sidecar}
	read.infoSum, err = s.remoteStreamAnswer(ctx, c, sidecar, &query)
	if err != nil {
		return remoteRead{err: s.InternalError(ctx, "cached copy", err)}, true
	}
	t.stepf("Resolving remote source with current read authorization")
	read.result, err = s.RemoteSources.Read(ctx, query)
	if err != nil {
		return remoteRead{
			err: s.ItemError(ctx, "read source", sourceAnswer(c, req, p, err)),
		}, true
	}
	return read, true
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

// remoteStreamAnswer returns how getRemoteItem answers a stream for q: nil
// for ServeRemoteStream, or, for a checksum request that reads only the
// info of the file it names, which it makes a HEAD, the read of the
// upstream's checksum file that serveInfoChecksum may make. 7.161.26's
// DownloadServiceImpl.respond hands a checksum request, HEAD or GET, to
// respondForChecksumRequest, which never downloads the file, before the
// branch that downloads one, on a remote of any type.
// DownloadRequestContext.getResourcePath strips the checksum's extension,
// so RemoteRepoBase.getInfo resolves the file, from the upstream's HEAD
// when the cache does not hold it. A copy the cache of a remote that
// stores artifacts locally holds, even one due for a refresh, is read as
// before; a remote that stores nothing reads no cache (getChecksum calls
// getRemoteChecksum there, whatever the cache holds).
func (s *Kit) remoteStreamAnswer(
	ctx context.Context, c repo.Config, sidecar string, q *source.Request,
) (*source.Request, error) {
	if sidecar == "" || q.CacheOnly {
		return nil, nil //nolint:nilnil // nil: ServeRemoteStream answers
	}
	if c.StoreArtifactsLocally == nil || *c.StoreArtifactsLocally {
		held, err := remoteCopyHeld(ctx, s.Store, c, q.Resource.Path)
		switch {
		case err != nil:
			return nil, err
		case held:
			return nil, nil //nolint:nilnil // nil: the held copy answers
		}
	}
	sum := upstreamChecksumRequest(*q, sidecar)
	q.Resource.Method = http.MethodHead
	return &sum, nil
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
		s.serveRemoteChecksum(sw, r, c, req.Path, checksum, result.Stream.Checksums,
			result.Stream.Modified)
		if sw.Complete(r) && result.Used != nil {
			_ = result.Used(ctx)
		}
		return
	}
	// A remote file served from the upstream's stream answers If-Match and
	// If-Unmodified-Since as its cached copy does: 7.x answers a remote
	// download through the same isNotModified.
	r = withoutUnevaluatedPreconditions(r)
	// Whatever the stream holds, a file the upstream sent or a document
	// the cache keeps without an item, the item route serves it as the
	// file at the path asked for, as 7.x's download serves any resource
	// it finds there (RemoteRepoResource, FileResource), and names it in
	// the item route's form as it names the copy the remote keeps.
	result.Stream.Path = req.Path
	sw := &StatusWriter{ResponseWriter: w}
	names := StreamNames(c, path.Base(req.Path), ItemDisposition)
	if err := pkgroute.ServeNamedStream(sw, r, result.Stream, names); err != nil {
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
	if err := remoteDeliveryErr(r.Context(), result, anonymous); err != nil {
		httpkit.WriteErr(w, r, err)
		return false
	}
	return true
}

// remoteDeliveryErr is RemoteDeliveryAllowed's refusal of result, or nil,
// for a caller that answers it itself, as a trace does. A refused stream
// is closed.
func remoteDeliveryErr(ctx context.Context, result source.Result, anonymous bool) error {
	if result.Revalidate == nil {
		return nil
	}
	err := result.Revalidate(ctx)
	if err == nil {
		return nil
	}
	if result.Stream != nil {
		_ = result.Stream.Body.Close()
	}
	return pkgroute.SourceError(err, anonymous) //nolint:wrapcheck // the answer the route writes
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

// serveRemoteChecksum serves value, the remote repository c's checksum
// file at name, of a file whose checksums are sums (empty when only the
// checksum is known) and which last changed at modified, as a local one is
// served (serveChecksum): 7.x answers both through the same
// DownloadServiceImpl.respond. c is the repository the request named and
// the one whose cache holds the file, which reads c's content browsing
// flag (LocalCacheRepoDescriptor.isArchiveBrowsingEnabled).
func (s *Kit) serveRemoteChecksum(
	w http.ResponseWriter,
	r *http.Request,
	c repo.Config,
	name, value string,
	sums kernel.Checksums,
	modified time.Time,
) {
	if value == "" {
		httpkit.WriteErr(w, r, ItemNotFound())
		return
	}
	serveChecksum(w, r, c, c, name, value, sums, modified)
}

// serveInfoChecksum answers the checksum request at req.Path for the file
// at resource.Path from result, the upstream's answer to a HEAD of that
// file, which found it, as 7.161.26's RemoteRepoBase.getChecksum answers
// one on a remote of any type:
//
//   - A remote that stores artifacts locally reads the file's info through
//     its checksum policy (localCacheRepo.getChecksum, RepoBase.getChecksum,
//     the cache taking the remote's policy), and that info holds only the
//     checksums the upstream declared, each as its original value
//     (HttpUtils.getChecksums). pass-thru answers the original
//     (ChecksumPolicyPassThru); generate-if-absent, fail and
//     ignore-and-generate answer the actual value, which a file never
//     downloaded does not have.
//   - A Maven-family remote that stores nothing reads the upstream's
//     checksum file, sum (getRemoteChecksum); any other type that stores
//     nothing has no value there.
//   - Still without a value, a generic remote with retrieveSha256FromServer
//     reads the upstream's checksum file, sum, for any algorithm
//     (getChecksumByExternalSha256 downloads the checksum path it is given,
//     as RTFACT-22923 asked for SHA-256, SHA-1 and MD5 alike).
//
// With no value respondForChecksumRequest answers 404 "Checksum not found
// for <file>", before any conditional header is read, as a local checksum
// file does (getSidecar). A declared SHA-512 answers as the other three do
// (DV-073). The file's headers carry what the upstream declared on its
// HEAD, as #1459 settled for a checksum that streams nothing.
func (s *Kit) serveInfoChecksum(
	w http.ResponseWriter,
	r *http.Request,
	result source.Result,
	c repo.Config,
	sum source.Request,
	req ItemRequest,
	resource source.Resource,
	sidecar string,
	anonymous bool,
) {
	value, err := s.infoChecksum(r.Context(), result, c, sum, resource, sidecar, anonymous)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	s.serveRemoteChecksum(w, r, c, req.Path, value, result.Stream.Checksums, result.Stream.Modified)
}

// infoChecksum is the value serveInfoChecksum answers, or the error it
// answers in its place, which a trace reports without serving it. It
// closes result's stream.
func (s *Kit) infoChecksum(
	ctx context.Context,
	result source.Result,
	c repo.Config,
	sum source.Request,
	resource source.Resource,
	sidecar string,
	anonymous bool,
) (string, error) {
	if err := remoteDeliveryErr(ctx, result, anonymous); err != nil {
		return "", err
	}
	_ = result.Stream.Body.Close()
	storesLocally := c.StoreArtifactsLocally == nil || *c.StoreArtifactsLocally
	value := ""
	if storesLocally && c.RemoteRepoChecksumPolicyType == repo.PassThru {
		value = item.Algorithm(sidecar).Of(result.Stream.Checksums)
	}
	if value == "" && (!storesLocally && maven.Serves(c.PackageType) ||
		c.PackageType == repo.Generic && c.RetrieveSha256FromServer != nil &&
			*c.RetrieveSha256FromServer) {
		var err error
		if value, err = s.upstreamChecksumValue(ctx, sum, anonymous); err != nil {
			return "", err
		}
	}
	if value == "" {
		return "", apierror.New(http.StatusNotFound, "Checksum not found for "+resource.Path)
	}
	return value, nil
}

// upstreamChecksumRequest is the read of the checksum file the upstream
// keeps beside the file q reads, <file>.<sidecar>, which 7.161.26's
// RemoteRepoBase.getRemoteChecksumFromPath downloads, for a Maven-family
// remote that stores nothing (getRemoteChecksum) and for a generic remote
// with retrieveSha256FromServer (getChecksumByExternalSha256).
// HttpRepo.downloadResource sends it with a NullRequestContext: none of the
// client's matrix parameters, query or headers.
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

// upstreamChecksumValue reads sum, the upstream's checksum file, and
// returns its first token, which checksumFileValue reads, under every
// checksum policy, as 7.161.26's getRemoteChecksumFromPath reads it
// (ChecksumUtils.checksumStringFromStream). An upstream with no checksum
// file, or one with no checksum in it, is no value: getChecksum's
// handleRemoteRequestException returns null for the upstream's 404. Any
// other failure is the error the route answers.
func (s *Kit) upstreamChecksumValue(
	ctx context.Context, sum source.Request, anonymous bool,
) (string, error) {
	value := ""
	fetched, err := s.RemoteSources.Read(ctx, sum)
	switch {
	case err != nil:
	case fetched.Stream == nil:
		err = fmt.Errorf("checksum file %s: %w", sum.Resource.Path, source.ErrInvalidRequest)
	default:
		if refused := remoteDeliveryErr(ctx, fetched, anonymous); refused != nil {
			return "", refused
		}
		value, err = checksumFileValue(fetched.Stream.Body)
		_ = fetched.Stream.Body.Close()
	}
	if err != nil && !errors.Is(err, kernel.ErrNotFound) {
		return "", s.ItemError(ctx, "read source", pkgroute.SourceError(err, anonymous))
	}
	return value, nil
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
