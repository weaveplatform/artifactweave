// Package source is the read boundary shared by remote package adapters. It is
// below HTTP routing and outside format write hooks: neither a resolver nor a
// body reader may run inside the transaction that publishes a cached item.
package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// ResourceKind distinguishes protocol metadata and version queries from bytes.
// Version lists remain authoritative upstream representations, not a listing of
// the subset of artifacts present in the local cache.
type ResourceKind int

const (
	Artifact ResourceKind = iota + 1
	Metadata
	Versions
)

// Resource contains only downstream fields a source may use. There is no
// caller-supplied header bag: credentials, cookies and forwarding headers cannot
// accidentally reach an upstream. Path is decoded and repository-relative;
// Query and Variant are opaque and must never be included in logs.
type Resource struct {
	// RequireSmart requests authenticated peer discovery before this prepared
	// metadata request. Only adapters set it; it conveys no network permission.
	RequireSmart bool
	// SmartAQL is a closed read-only items.find operation. It uses transient
	// metadata and the discovered peer search endpoint, never a generic POST.
	SmartAQL string
	// Adapter targets never change Path, which remains the authorization and
	// cache identity. UpstreamPath is escaped and repository-relative;
	// ExternalURL is a metadata-selected absolute URL subject to the transport's
	// DNS, redirect and credential-origin checks. They are mutually exclusive.
	UpstreamPath string
	ExternalURL  string
	// CheckURL narrows an adapter-selected destination and every redirect hop.
	// It never replaces transport DNS/credential policy and requires a Variant.
	CheckURL func(context.Context, *url.URL) error
	// NoCrossOriginAuth prevents a format-selected URL from borrowing remote
	// repository credentials, client certificates or cookies on another origin.
	NoCrossOriginAuth bool
	// GitLFSActionHeaders are bounded, validated headers supplied by an upstream
	// Git LFS download action. They apply only to that exact action URL, never
	// redirects or cached representations.
	GitLFSActionHeaders http.Header
	// Checksums pin the selected representation independently of the repository's
	// policy for optional upstream checksum headers. Variant identifies the
	// adapter validation schema; it must change if validation semantics change.
	Checksums           types.Checksums
	ExpectedContentType string
	// ExpectedContentTypes is an exact metadata MIME allowlist for negotiated APIs.
	// It requires complete body validation and cannot accompany the singular hint.
	ExpectedContentTypes []string
	Validate             func(context.Context, io.ReadSeeker, int64) error
	// ValidateMetadata validates complete metadata against the actual response
	// base after redirects. It is exclusive with Validate and requires Variant.
	ValidateMetadata func(context.Context, io.ReadSeeker, int64, MetadataResponse) error
	// PermissionFolder authorizes metadata against the folder it describes; the
	// adapter must filter its response to the readable descendants. Artifact
	// requests always authorize their exact cache path.
	PermissionFolder bool
	PermissionPath   string
	// PermissionRoot is a trusted metadata-discovery scope when a layout cannot
	// represent the query as a canonical folder. It requires actual root Read,
	// not a grant on any descendant, and disclosed files still need CheckRead.
	PermissionRoot bool
	// GoneIsMissing is a trusted protocol opt-in: GOPROXY permits member fallback
	// for both 404 and 410. Other protocols retain their existing error semantics.
	GoneIsMissing bool
	// ModuleDownloadLocation is a closed metadata projection for Terraform's
	// 204/X-Terraform-Get response. The backend retains the exact header in a
	// private JSON control representation; the adapter must validate its source
	// against the actual response base before admission and rewrite it on delivery.
	ModuleDownloadLocation bool
	// NuGetEmptyFeed projects a bodyless NuGet v2 204 into a fixed empty Atom
	// feed. Only trusted feed/query metadata adapters may opt in; complete
	// metadata validation, an Atom MIME expectation and a versioned schema remain required.
	NuGetEmptyFeed bool
	// ChecksumOnly is the trusted generic cold SHA-256 sidecar route. It must
	// remain transient and authorize the artifact it describes.
	ChecksumOnly bool
	// Mutable marks a refreshable artifact name, such as a non-unique Maven
	// snapshot. It does not turn a binary into metadata for zap semantics.
	Mutable     bool
	Kind        ResourceKind
	Path        string
	EscapedPath string
	Method      string
	// PostBody is a canonical, bounded format-owned metadata query. Only the
	// closed AI editor and Git LFS contracts below may use it. No caller
	// supplied headers or credentials are forwarded with it.
	PostBody []byte
	// Transient forbids persistent caching of a Git LFS batch response, whose
	// upstream transfer actions may carry expiring URLs or credentials.
	Transient       bool
	Query           string
	Variant         string
	Accept          string
	Range           string
	IfRange         string
	IfNoneMatch     string
	IfModifiedSince string
}

// RetrievalIntent is computed from the authorized entry point, never from an
// HTTP header or query parameter. CacheOnly cannot traverse a source or join a
// MayFetch flight, even if another request is already filling the same path.
type RetrievalIntent int

const (
	CacheOnly RetrievalIntent = iota + 1
	MayFetch
)

// Request carries the resolved repository and caller's permission snapshot.
// Adapters resolve repository existence before constructing it. Project and
// Visited retain the original consumer context for P11 and future P10 sources;
// neither is taken from caller-supplied headers.
type Request struct {
	Repo      repo.Config
	Subject   authz.Subject
	Access    *authz.Decider
	Matrix    types.Matrix
	Resource  Resource
	CacheOnly bool
	Project   string
	Visited   []types.RepositoryIncarnation
}

// Permit is request-scoped authority. A future project backend may narrow
// UpstreamNetworkAllowed while retaining MayFetch to consult a producer's stored
// bytes. Each hop must narrow this permit; it cannot borrow a more privileged
// flight's newly fetched result. It is not a persisted credential.
type Permit struct {
	Identity               types.SourceIdentity
	Intent                 RetrievalIntent
	UpstreamNetworkAllowed bool
}

// MetadataResponse is trusted adapter-only response context, never an HTTP header
// or log field. BaseURL contains only scheme, host and path after redirects.
// Adapters opting into this context must change Variant when upgrading old cache
// entries that did not retain their response base.
type MetadataResponse struct {
	BaseURL string
}

// Representation describes bytes safely, without arbitrary upstream headers or
// an upstream URL. Size is -1 when unknown. Validators describe these exact bytes;
// an adapter that rewrites metadata must replace them.
type Representation struct {
	// OriginalChecksums retain upstream declarations for Maven pass-through;
	// Checksums always describe the computed bytes.
	OriginalChecksums types.Checksums
	MediaType         string
	Size              int64
	ETag              string
	Modified          time.Time
	Checksums         types.Checksums
	ContentRange      string
}

// Stream is an unpersisted representation (including storeArtifactsLocally=false).
// Status is 200, 206 or 304. Body ownership transfers to the caller on success;
// the caller closes it on every path, including HEAD and downstream disconnect.
// Service also closes it when the request context is canceled.
type Stream struct {
	Body   io.ReadCloser
	Status int
	Representation
}

// Result holds exactly one of Item and Stream. A persisted Item belongs to the
// consumer's cache; a stream must not be presented as a fake persisted item.
// Identity allows consumers and publication code to retain the source revision.
type Result struct {
	Metadata MetadataResponse `json:"-"`

	// CheckRead checks a disclosed descendant against current authority. It is
	// bounded by the originally authorized metadata folder and never fetches.
	CheckRead func(context.Context, string) error
	// CheckFolder checks whether a disclosed descendant folder can contain
	// readable items. It preserves narrow grants without granting file access.
	CheckFolder func(context.Context, string) error
	// Revalidate fences backend-specific credentials immediately before delivery.
	Revalidate func(context.Context) error
	Item       *types.Item
	Stream     *Stream
	Identity   types.SourceIdentity
	// Used records successful content delivery using the backend's opaque cache
	// token. The delivery adapter calls it after a successful GET/range or signed
	// URL issuance, never on lookup, HEAD, 304 or a failed body. Nil for no-store.
	Used func(context.Context) error
}

// Backend resolves one already-authorized request. It owns freshness, fetching
// and optional publication, but cannot expand Permit. Network and body reads
// precede short conditional publication transactions. Any body returned with an
// error is closed by Service, so failed admission cannot leak a connection.
type Backend interface {
	Read(context.Context, Request, Permit) (Result, error)
}

// Service registers only the implemented HTTP source kind. Resolve obtains the
// durable identity matching the supplied configuration snapshot (#619), without
// network I/O. Revalidate, when supplied, rechecks current caller/repository
// authority before lookup and before returning bytes (P11 adds share revisions).
// The application must not install this service in production routes until the
// relevant adapter is ready. Nil Cache is a cache miss, never a network fallback.
type Service struct {
	Resolve    func(context.Context, repo.Config) (types.SourceIdentity, error)
	Revalidate func(context.Context, Request, Permit) error
	HTTP       Backend
	Cache      Backend
}

// Read applies path, permission and repository restrictions before any backend
// work, then fences the returned representation against a changed source. No
// store transaction is opened here or propagated by this package.
func (s *Service) Read(ctx context.Context, q Request) (Result, error) {
	if err := authorize(q); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("read source: %w", err)
	}
	if s.Resolve == nil {
		return Result{}, ErrUnsupported
	}
	identity, err := s.Resolve(ctx, q.Repo)
	if err != nil {
		return Result{}, safeError(err)
	}
	if identity.Kind != types.ExternalHTTP {
		return Result{}, ErrUnsupported
	}
	if identity.Repository == "" || identity.ID == "" || identity.Generation == "" {
		return Result{}, ErrUnavailable
	}
	p := Permit{Identity: identity, Intent: MayFetch, UpstreamNetworkAllowed: true}
	backend := s.HTTP
	if q.CacheOnly {
		p.Intent, p.UpstreamNetworkAllowed = CacheOnly, false
		backend = s.Cache
	}
	if err = s.revalidate(ctx, q, p); err != nil {
		return Result{}, err
	}
	if backend == nil {
		if q.CacheOnly {
			return Result{}, store.ErrNotFound
		}
		return Result{}, ErrUnsupported
	}
	result, err := backend.Read(ctx, q, p)
	if err == nil {
		err = validateResult(q, result)
	}
	if err == nil && result.Revalidate != nil {
		err = result.Revalidate(ctx)
	}
	if err == nil {
		err = s.revalidate(ctx, q, p)
	}
	if err == nil {
		var current types.SourceIdentity
		current, err = s.Resolve(ctx, q.Repo)
		if err == nil && current != identity {
			err = ErrSuperseded
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		closeResult(result)
		return Result{}, safeError(err)
	}
	s.fenceDelivery(q, p, identity, &result)
	if result.Stream != nil {
		body := &ownedBody{ReadCloser: result.Stream.Body}
		body.stop = context.AfterFunc(ctx, func() { _ = body.close() })
		if seeker, ok := result.Stream.Body.(io.ReadSeeker); ok {
			result.Stream.Body = &ownedSeekBody{ownedBody: body, seeker: seeker}
		} else {
			result.Stream.Body = body
		}
	}
	return result, nil
}

func (s *Service) fenceDelivery(
	q Request,
	p Permit,
	identity types.SourceIdentity,
	result *Result,
) {
	result.Identity = identity
	backendCheck := result.Revalidate
	result.Revalidate = func(current context.Context) error {
		if backendCheck != nil {
			if err := backendCheck(current); err != nil {
				return safeError(err)
			}
		}
		if err := s.revalidate(current, q, p); err != nil {
			return err
		}
		latest, err := s.Resolve(current, q.Repo)
		if err != nil {
			return safeError(err)
		}
		if latest != identity {
			return ErrSuperseded
		}
		return nil
	}
	check := func(current context.Context, path string, folder bool) error {
		scope := q.Resource.Path
		if q.Resource.PermissionRoot {
			scope = ""
		} else if q.Resource.PermissionFolder {
			scope = q.Resource.PermissionPath
		}
		if path != scope &&
			(!q.Resource.PermissionFolder && !q.Resource.PermissionRoot || (scope != "" && !strings.HasPrefix(path, scope+"/"))) {
			return ErrDenied
		}
		exact := q
		exact.Resource = Resource{Kind: Artifact, Path: path, Method: http.MethodGet}
		if folder {
			if !q.Resource.PermissionFolder && !q.Resource.PermissionRoot {
				return ErrDenied
			}
			exact.Resource.Kind = Metadata
			exact.Resource.PermissionFolder = true
			exact.Resource.PermissionPath = path
		}
		if err := authorize(exact); err != nil {
			return safeError(err)
		}
		if err := result.Revalidate(current); err != nil {
			return err
		}
		return s.revalidate(current, exact, p)
	}
	result.CheckRead = func(ctx context.Context, path string) error { return check(ctx, path, false) }
	result.CheckFolder = func(ctx context.Context, path string) error { return check(ctx, path, true) }
}

func authorize(q Request) error {
	r := q.Resource
	if q.Repo.Class != repo.Remote {
		return ErrUnsupported
	}
	if r.Kind < Artifact || r.Kind > Versions ||
		(r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost) ||
		r.Path != strings.Trim(r.Path, "/") || strings.ContainsAny(r.Path, "\r\n") {
		return ErrInvalidRequest
	}
	if r.EscapedPath != "" {
		decoded, err := url.PathUnescape(r.EscapedPath)
		if err != nil || decoded != r.Path || strings.ContainsAny(r.EscapedPath, "?#") {
			return ErrInvalidRequest
		}
	}
	if _, _, _, err := types.SplitItemPath(r.Path); err != nil {
		return ErrInvalidRequest
	}
	if r.UpstreamPath != "" {
		decoded, err := url.PathUnescape(r.UpstreamPath)
		if err != nil || decoded != strings.Trim(decoded, "/") ||
			strings.ContainsAny(r.UpstreamPath, "?#\r\n") ||
			strings.ContainsAny(decoded, "\r\n") {
			return ErrInvalidRequest
		}
		if _, _, _, err := types.SplitItemPath(decoded); err != nil {
			return ErrInvalidRequest
		}
	}
	if r.ExternalURL != "" && r.UpstreamPath != "" ||
		(r.Validate != nil || r.ValidateMetadata != nil) && r.Variant == "" {
		return ErrInvalidRequest
	}
	if err := validateMetadataPOST(q); err != nil {
		return err
	}
	if err := validateAdapterContract(r); err != nil {
		return err
	}
	checkedPath, err := permissionPath(r)
	if err != nil {
		return err
	}
	return authorizePath(q, checkedPath)
}

func validateMetadataPOST(q Request) error {
	r := q.Resource
	if r.Method != http.MethodPost {
		if len(r.PostBody) != 0 || r.Transient &&
			(q.Repo.PackageType != repo.GitLFS || validateGitLFSLocksGET(r) != nil) {
			return ErrInvalidRequest
		}
		return nil
	}
	if q.Repo.PackageType == repo.GitLFS {
		return validateGitLFSPOST(r)
	}
	if r.Transient {
		return ErrInvalidRequest
	}
	return validateGalleryPOST(q)
}

// validateGitLFSLocksGET admits one fresh, read-only upstream lock listing.
// In particular, its caller cannot turn the remote source into a generic
// metadata proxy or cache a stale "no locks" response.
func validateGitLFSLocksGET(r Resource) error {
	if r.Kind != Metadata || r.Method != http.MethodGet ||
		r.Path != "locks" || r.UpstreamPath != "locks" || r.ExternalURL != "" ||
		r.CheckURL == nil || r.EscapedPath != "" ||
		r.Variant != "gitlfs-locks-v1" || !r.Transient ||
		r.Accept != "application/vnd.git-lfs+json" ||
		r.ExpectedContentType != "application/vnd.git-lfs+json" ||
		r.ValidateMetadata == nil || r.Validate != nil ||
		!r.PermissionFolder || r.PermissionPath != "" || r.PermissionRoot ||
		r.Checksums != (types.Checksums{}) || len(r.PostBody) != 0 ||
		r.Range != "" || r.IfRange != "" || r.IfNoneMatch != "" ||
		r.IfModifiedSince != "" || len(r.Query) > 4096 ||
		galleryPOSTHasUnsupportedOptions(r) {
		return ErrInvalidRequest
	}
	values, err := url.ParseQuery(r.Query)
	if err != nil {
		return ErrInvalidRequest
	}
	for key, parts := range values {
		if !slices.Contains([]string{"path", "id", "cursor", "limit", "refspec"}, key) ||
			len(parts) != 1 || len(parts[0]) > 2048 {
			return ErrInvalidRequest
		}
	}
	return nil
}

func validateGalleryPOST(q Request) error {
	r := q.Resource
	if q.Repo.PackageType != repo.AIEditorExtensions || r.Kind != Metadata ||
		galleryPOSTHasUnsupportedOptions(r) ||
		r.UpstreamPath != "extensionquery" ||
		r.ExternalURL != "" || r.CheckURL != nil || r.Query != "" ||
		r.EscapedPath != "" || r.Range != "" || r.IfRange != "" ||
		r.IfNoneMatch != "" || r.IfModifiedSince != "" ||
		r.Variant != "aieditorextensions-gallery-v1" ||
		r.Accept != "application/json;api-version=3.0-preview.1" ||
		r.ExpectedContentType != "application/json" ||
		len(r.ExpectedContentTypes) != 0 || r.ValidateMetadata == nil ||
		r.Validate != nil || !r.PermissionFolder || r.PermissionRoot ||
		r.Checksums != (types.Checksums{}) || len(r.PostBody) == 0 ||
		len(r.PostBody) > 64<<10 || !json.Valid(r.PostBody) {
		return ErrInvalidRequest
	}
	sum := sha256.Sum256(r.PostBody)
	if r.Path != "gallery/queries/"+hex.EncodeToString(sum[:])+".json" {
		return ErrInvalidRequest
	}
	return nil
}

func validateGitLFSPOST(r Resource) error {
	const lfsMedia = "application/vnd.git-lfs+json"
	if r.Kind != Metadata || r.UpstreamPath != "objects/batch" ||
		r.ExternalURL != "" || r.CheckURL != nil || r.Query != "" ||
		r.EscapedPath != "" || r.Range != "" || r.IfRange != "" ||
		r.IfNoneMatch != "" || r.IfModifiedSince != "" ||
		r.Variant != "gitlfs-batch-v1" || r.Accept != lfsMedia ||
		r.ExpectedContentType != lfsMedia || len(r.ExpectedContentTypes) != 0 ||
		r.ValidateMetadata == nil || r.Validate != nil || r.PermissionFolder ||
		r.PermissionRoot || r.Checksums != (types.Checksums{}) || !r.Transient ||
		len(r.PostBody) == 0 || len(r.PostBody) > 4096 || !json.Valid(r.PostBody) ||
		galleryPOSTHasUnsupportedOptions(r) {
		return ErrInvalidRequest
	}
	var body struct {
		Operation string `json:"operation"`
		Objects   []struct {
			OID  string `json:"oid"`
			Size int64  `json:"size"`
		} `json:"objects"`
	}
	if json.Unmarshal(r.PostBody, &body) != nil || body.Operation != "download" ||
		len(body.Objects) != 1 || body.Objects[0].Size < 0 ||
		r.Path != gitLFSObjectPath(body.Objects[0].OID) {
		return ErrInvalidRequest
	}
	return nil
}

func gitLFSObjectPath(oid string) string {
	if len(oid) != 64 {
		return ""
	}
	for _, ch := range oid {
		if ch < '0' || ch > '9' && ch < 'a' || ch > 'f' {
			return ""
		}
	}
	return "objects/" + oid[:2] + "/" + oid[2:4] + "/" + oid
}

func galleryPOSTHasUnsupportedOptions(r Resource) bool {
	return r.RequireSmart || r.SmartAQL != "" || r.ModuleDownloadLocation ||
		r.NuGetEmptyFeed || r.ChecksumOnly || r.Mutable || r.GoneIsMissing
}

func authorizePath(q Request, checkedPath string) error {
	r := q.Resource
	if q.Access == nil {
		return ErrDenied
	}
	allows := q.Access.Allows
	if r.PermissionFolder {
		allows = q.Access.AllowsFolder
	}
	if !allows(types.ResourceArtifact,
		authz.Repo{Key: q.Repo.Key, Class: string(q.Repo.Class)}, checkedPath, types.ActionRead) {
		return ErrDenied
	}
	if err := generic.CheckDownload(
		q.Repo,
		checkedPath,
		r.PermissionFolder || r.PermissionRoot,
	); err != nil {
		return fmt.Errorf("source download: %w", err)
	}
	return nil
}

func permissionPath(r Resource) (string, error) {
	checkedPath := r.Path
	if r.PermissionRoot {
		if r.Kind == Artifact || r.Method != http.MethodGet || r.PermissionFolder ||
			r.PermissionPath != "" || r.Variant == "" || r.Validate == nil && r.ValidateMetadata == nil {
			return "", ErrInvalidRequest
		}
		checkedPath = ""
	}
	if r.PermissionFolder {
		if r.Kind == Artifact || r.PermissionPath != strings.Trim(r.PermissionPath, "/") {
			return "", ErrInvalidRequest
		}
		if _, _, _, err := types.SplitItemPath(
			r.PermissionPath,
		); r.PermissionPath != "" &&
			err != nil {
			return "", ErrInvalidRequest
		}
		checkedPath = r.PermissionPath
	}
	if r.ChecksumOnly {
		if r.Kind != Artifact || !strings.HasSuffix(checkedPath, ".sha256") {
			return "", ErrInvalidRequest
		}
		checkedPath = strings.TrimSuffix(checkedPath, ".sha256")
		if checkedPath == "" {
			return "", ErrInvalidRequest
		}
	}
	return checkedPath, nil
}

func (s *Service) revalidate(ctx context.Context, q Request, p Permit) error {
	if s.Revalidate != nil {
		return safeError(s.Revalidate(ctx, q, p))
	}
	return nil
}

func validateResult(q Request, r Result) error {
	if (r.Item == nil) == (r.Stream == nil) {
		return ErrInvalidResult
	}
	if r.Item != nil {
		if r.Item.RepoKey != q.Repo.Key || !r.Item.Cached || r.Item.FullPath() != q.Resource.Path ||
			r.Item.Folder {
			return ErrInvalidResult
		}
		return nil
	}
	if r.Stream.Body == nil || r.Stream.Size < -1 ||
		(r.Stream.Status != http.StatusOK && r.Stream.Status != http.StatusPartialContent && r.Stream.Status != http.StatusNotModified) {
		return ErrInvalidResult
	}
	return nil
}

func closeResult(r Result) {
	if r.Stream != nil && r.Stream.Body != nil {
		_ = r.Stream.Body.Close()
	}
}

type ownedBody struct {
	io.ReadCloser
	once sync.Once
	stop func() bool
	err  error
}

func (b *ownedBody) close() error {
	b.once.Do(func() { b.err = b.ReadCloser.Close() })
	return b.err
}

func (b *ownedBody) Close() error {
	b.stop()
	return b.close()
}

// Keep a validated temporary file or stored metadata seekable so delivery can
// apply ranges and conditional requests without buffering it again.
type ownedSeekBody struct {
	*ownedBody
	seeker io.Seeker
}

func (b *ownedSeekBody) Seek(offset int64, whence int) (int64, error) {
	pos, err := b.seeker.Seek(offset, whence)
	if err != nil {
		return pos, fmt.Errorf("seek source body: %w", err)
	}
	return pos, nil
}

// Safe failures are protocol-independent. Routers map them to their own error
// envelopes; upstream response bodies, URLs and credentials are never messages.
var (
	ErrDenied         = errors.New("source read denied")
	ErrInvalidRequest = errors.New("invalid source request")
	ErrUnsupported    = errors.New("source kind or operation is not implemented")
	ErrUnavailable    = errors.New("source unavailable")
	ErrSuperseded     = errors.New("source configuration changed during retrieval")
	ErrInvalidResult  = errors.New("invalid source result")
)

// FailureKind distinguishes status-bearing faults for freshness policy.
type FailureKind int

const (
	HTTPFailure FailureKind = iota + 1
	ConnectivityFailure
	TimeoutFailure
	SecurityFailure
	IntegrityFailure
	ProtocolFailure
)

// HTTPError preserves an approved status without its upstream body, URL or
// authentication challenge. Confirmed 404 uses store.ErrNotFound; other failures
// must never be recorded as negative cache entries.
type HTTPError struct {
	Kind       FailureKind
	Status     int
	RetryAfter time.Duration
}

func (f *HTTPError) Error() string { return "source: " + http.StatusText(f.Status) }

func safeError(err error) error {
	if err == nil {
		return nil
	}
	var failure *HTTPError
	if errors.As(err, &failure) {
		if failure.Status == 401 || failure.Status == 403 || failure.Status == 429 ||
			(failure.Status >= 500 && failure.Status <= 599) {
			copy := *failure
			if copy.RetryAfter < 0 {
				copy.RetryAfter = 0
			}
			if copy.RetryAfter > 5*time.Minute {
				copy.RetryAfter = 5 * time.Minute
			}
			return &copy
		}
		return ErrUnavailable
	}
	for _, allowed := range []error{
		ErrDenied, ErrInvalidRequest, ErrUnsupported,
		ErrUnavailable, ErrSuperseded, ErrInvalidResult, store.ErrNotFound,
		context.Canceled, context.DeadlineExceeded,
	} {
		if errors.Is(err, allowed) {
			return allowed
		}
	}
	return ErrUnavailable
}

func validateAdapterContract(r Resource) error {
	if r.Variant == "gitlfs-locks-v1" && validateGitLFSLocksGET(r) != nil {
		return ErrInvalidRequest
	}
	if invalidGitLFSObjectResource(r) {
		return ErrInvalidRequest
	}
	if invalidAdapterURLGuard(r) {
		return ErrInvalidRequest
	}
	if err := validateModuleLocationContract(r); err != nil {
		return err
	}
	if err := validateNuGetEmptyFeedContract(r); err != nil {
		return err
	}
	if err := validateSmartAQLContract(r); err != nil {
		return err
	}
	if err := validateSmartContract(r); err != nil {
		return err
	}
	if (r.Validate != nil || r.ValidateMetadata != nil) && r.Variant == "" {
		return ErrInvalidRequest
	}
	if r.ValidateMetadata != nil && (r.Kind == Artifact || r.Validate != nil) {
		return ErrInvalidRequest
	}
	if len(r.ExpectedContentTypes) > 0 {
		if r.Kind == Artifact || r.ExpectedContentType != "" ||
			(r.Validate == nil && r.ValidateMetadata == nil) ||
			len(r.ExpectedContentTypes) > 16 {
			return ErrInvalidRequest
		}
		seen := map[string]bool{}
		for _, raw := range r.ExpectedContentTypes {
			mt, params, err := mime.ParseMediaType(raw)
			if err != nil || mt != raw || len(params) != 0 || strings.Count(mt, "/") != 1 ||
				strings.Contains(mt, "*") ||
				seen[mt] {
				return ErrInvalidRequest
			}
			seen[mt] = true
		}
	}
	return nil
}

func invalidAdapterURLGuard(r Resource) bool {
	return r.CheckURL != nil &&
		(r.ExternalURL == "" && r.Variant != "gitlfs-locks-v1" || r.Variant == "")
}

func invalidGitLFSObjectResource(r Resource) bool {
	if len(r.GitLFSActionHeaders) > 0 &&
		(r.Variant != "gitlfs-object-v1" || r.ExternalURL == "" || !r.NoCrossOriginAuth) {
		return true
	}
	return r.Variant == "gitlfs-object-v1" &&
		(r.Kind != Artifact || r.Method != http.MethodGet ||
			r.Path != gitLFSObjectPath(r.Checksums.SHA256) ||
			r.Checksums.SHA256 == "" || r.Query != "" ||
			r.UpstreamPath != "" || r.Validate == nil)
}

func validateModuleLocationContract(r Resource) error {
	if !r.ModuleDownloadLocation {
		return nil
	}
	if r.Kind != Metadata || r.Method != http.MethodGet ||
		r.ValidateMetadata == nil || r.Variant == "" || r.Checksums != (types.Checksums{}) ||
		r.ExpectedContentType != "application/json" || len(r.ExpectedContentTypes) > 0 {
		return ErrInvalidRequest
	}
	return nil
}

func validateNuGetEmptyFeedContract(r Resource) error {
	if !r.NuGetEmptyFeed {
		return nil
	}
	if r.Kind != Metadata || r.Method != http.MethodGet ||
		r.ValidateMetadata == nil || r.Variant == "" || r.Checksums != (types.Checksums{}) ||
		(r.ExpectedContentType != "application/atom+xml" && !slices.Contains(r.ExpectedContentTypes, "application/atom+xml")) {
		return ErrInvalidRequest
	}
	return nil
}

func validateSmartContract(r Resource) error {
	if r.RequireSmart &&
		(r.Kind != Metadata || r.ExternalURL == "" || r.Variant == "" || r.Validate == nil && r.ValidateMetadata == nil) {
		return ErrInvalidRequest
	}
	return nil
}

func validateSmartAQLContract(r Resource) error {
	if r.SmartAQL == "" {
		return nil
	}
	if !r.RequireSmart || r.Method != http.MethodGet || r.ValidateMetadata == nil ||
		r.ExpectedContentType != "application/json" ||
		len(r.ExpectedContentTypes) > 0 ||
		r.ModuleDownloadLocation ||
		!r.PermissionFolder ||
		r.PermissionPath != "" ||
		r.Checksums != (types.Checksums{}) {
		return ErrInvalidRequest
	}
	parsed, err := aql.Parse(r.SmartAQL)
	if err != nil || parsed.Transitive || !parsed.HasLimit || parsed.Limit > 10001 {
		return ErrInvalidRequest
	}
	if _, err = parsed.TransitiveTarget(); err != nil {
		return ErrInvalidRequest
	}
	return nil
}
