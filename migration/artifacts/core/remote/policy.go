package remote

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/migration/platform/tx"
	"github.com/weaveplatform/artifactweave/migration/platform/work"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Store is the persistence that the policy, Smart Remote and the ecosystem
// remote rules reach through Policy.Store: the remote cache, repositories
// and items, the work queue and the lease table, in transactions. Every
// call made inside a policy transaction joins it, and work.New builds the
// Smart and Maven companion queues on it, so work they enqueue exists
// exactly when the change that needs it commits (decision 0033).
// CheckCurrent is handed it. It names no access store: credentials reach
// the policy through Options. Each composite store in app is a Store.
type Store interface {
	tx.Runner
	RemoteCache() port.RemoteCacheStore
	Repos() port.RepoStore
	Items() port.ItemStore
	WorkQueue() work.WorkQueue
	Leases() work.LeaseStore
}

// Policy implements the shared remote retrieval table. Authorization belongs to
// source.Service; CheckCurrent repeats it inside the short publication transaction
// and verifies the effective credential revision without network access.
type Policy struct {
	// Smart reads, in place of the policy, every request whose context
	// carries no WithSmart mark; NewSmart in artifacts/core/remote/smart
	// installs itself here. Nil leaves every read to the policy.
	Smart Interceptor
	// Fetch overrides only the protocol exchange. Authentication adapters retain
	// the source identity and credential revision used by this cache operation.
	Fetch func(context.Context, source.Request, source.Permit, Options, Request) (*http.Response, error)
	// GitFetch answers a request for a CocoaPods Git checkout (q.Resource.CocoaPodsGit
	// set). The application installs cocoapods.FetchCocoaPodsGit; nil answers
	// source.ErrUnsupported, as a policy without a pool does.
	GitFetch func(ctx context.Context, p *Policy, q source.Request, permit source.Permit, opts Options) (*http.Response, error)
	// PrepareUpload performs bounded adapter work outside SQL, after the main
	// response succeeds and before validation/publication. It uses the same
	// transport security and cannot replace the final CheckCurrent fence.
	PrepareUpload func(context.Context, source.Request, source.Permit, Options, *item.CacheUpload) error
	// ProjectResponse may rewrite an upstream response before admission, for a
	// format whose protocol gives a status a meaning of its own (NuGet v2 reads a
	// bodyless 204 as an empty feed). It runs after the module-location projection
	// and before missing-status normalisation; nil leaves every response as sent.
	// The application installs nuget.ProjectEmptyFeed. A hook that replaces
	// res.Body closes the body it replaces; the policy closes whichever body res
	// holds when the fetch ends. An error it returns ends the fetch as it is, with
	// no fallback to a cached copy, so it is one of the source service's errors.
	ProjectResponse func(q source.Request, res *http.Response) error
	Store           Store
	Blobs           blob.Store
	Items           *item.Service
	// Pool opens the upstream connections. Left nil, with Fetch nil too, every
	// read that needs the network answers source.ErrUnavailable. Assign a
	// *Pool only when it is not nil: a nil *Pool is a non-nil Upstream, which
	// passes that check and the format rules' own.
	Pool         Upstream
	Options      func(context.Context, repo.Config) (Options, error)
	CheckCurrent func(context.Context, Store, source.Request, source.Permit, string) error
	// Key is a stable keyring-derived HMAC key, never a logged cache identifier.
	Key           []byte
	Now           func() time.Time
	MetadataLimit int64
	flights       FlightGroup[fillResult]
	probes        FlightGroup[fillResult]
	capacity      Admission
}

type fillResult struct {
	key         types.RemoteCacheKey
	entry       types.RemoteCacheEntry
	isTransient bool
	failure     error
}

func (p *Policy) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

// Acquire waits for a slot of the policy's upstream capacity for repoKey,
// which Smart Remote's own peer requests share with the policy's fills: at
// most 8 active exchanges per repository and 64 in all, with up to 256
// callers queued per repository. It returns the release, which is safe to
// call more than once, or ErrBusy when the repository's queue is full, or
// the context's error, wrapped, when ctx ends first.
func (p *Policy) Acquire(ctx context.Context, repoKey string) (func(), error) {
	return p.capacity.Acquire(ctx, repoKey)
}

// Read is the source.Backend that the source service's HTTP kind calls once
// it has authorised q. It serves a fresh cached entry, otherwise revalidates
// or fetches from the upstream (one shared fill per cache key and source
// identity, within Acquire's capacity), and falls back to a stale entry on
// the failures decision 0007 allows. A cache-only intent, a permit without
// upstream network access, an offline repository and a derived resource
// read the cache alone. With an Interceptor installed, a read that WithSmart
// has not marked goes to it first. Errors: source.ErrUnavailable when the
// hooks a fetch needs are not installed, store.ErrNotFound for a fresh
// negative entry, source.ErrSuperseded when the credential revision moved
// on, and PolicyError's mapping of upstream and capacity failures. A
// successful result's Revalidate repeats Check before disclosure.
func (p *Policy) Read(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
) (out source.Result, retErr error) {
	if p.Smart != nil && ctx.Value(smartKey{}) == nil {
		//nolint:wrapcheck // preserves upstream error identity
		return p.Smart.Read(ctx, q, permit)
	}
	var revision string
	defer func() {
		if retErr == nil && revision != "" {
			out.Revalidate = func(current context.Context) error { return p.Check(current, q, permit, revision) }
		}
	}()
	if permit.Intent == source.RetrievalIntentCacheOnly || !permit.UpstreamNetworkAllowed ||
		enabled(q.Repo.Offline) {
		return p.cachedOnly(ctx, q)
	}
	if p.kind(q) == types.RemoteDerived {
		return p.cachedOnly(ctx, q)
	}
	if p.Options == nil || (p.Pool == nil && p.Fetch == nil) || len(p.Key) == 0 {
		return source.Result{}, source.ErrUnavailable
	}
	opts, err := p.Options(ctx, q.Repo)
	if err != nil {
		return source.Result{}, PolicyError(err)
	}
	if !mavenCompanionRevision(ctx, opts.Revision) {
		return source.Result{}, source.ErrSuperseded
	}
	revision = opts.Revision
	key := p.CacheKey(q, opts.Revision)
	noStore := transientResource(q)
	if !noStore {
		entry, readErr := p.ReadEntry(ctx, key, permit)
		if readErr == nil {
			if p.negativeFresh(q, entry) {
				return source.Result{}, store.ErrNotFound
			}
			if p.fresh(q, entry) {
				return p.resultFor(ctx, q, entry)
			}
		} else if !errors.Is(readErr, store.ErrNotFound) {
			return source.Result{}, readErr
		}
	}
	responseCtx := ctx
	ctx, cancel := MetadataContext(ctx, q)
	defer cancel()
	release, err := p.Acquire(ctx, q.Repo.Key)
	if err != nil {
		return source.Result{}, PolicyError(err)
	}
	defer release()
	if noStore || q.Resource.Method == http.MethodHead {
		// These requests own their response. A stream or a bodyless HEAD must
		// never become the shared result of a persistent fill.
		return p.direct(ctx, q, permit, opts, key, noStore)
	}
	flightKey := p.FlightKey(key, permit.Identity)
	filled, err := p.flights.Do(ctx, flightKey, func(shared context.Context) (fillResult, error) {
		return p.fill(shared, q, permit, opts, key)
	})
	if err != nil {
		return source.Result{}, err
	}
	if err = p.Check(ctx, q, permit, opts.Revision); err != nil {
		return source.Result{}, PolicyError(err)
	}
	if filled.isTransient {
		// A previously unknown no-store/Vary response cannot be handed to
		// multiple owners. Discard the shared candidate and fetch one owned
		// transient representation per caller; no persistent entry is made.
		return p.direct(ctx, q, permit, opts, key, true)
	}
	if filled.failure != nil {
		filled, err = p.fallback(q, key, filled.entry, filled.failure)
		if err != nil {
			return source.Result{}, err
		}
	}
	return p.resultFor(responseCtx, q, filled.entry)
}

func transientResource(q source.Request) bool {
	return q.Resource.ChecksumOnly || q.Resource.Transient ||
		q.Repo.StoreArtifactsLocally != nil && !*q.Repo.StoreArtifactsLocally
}

// MetadataContext bounds a metadata exchange by the repository's metadata
// retrieval timeout, 60 seconds when unset; an artifact request, or a
// timeout of zero or less, keeps ctx as it is. The caller calls the cancel
// function in every case.
func MetadataContext(ctx context.Context, q source.Request) (context.Context, context.CancelFunc) {
	if q.Resource.Kind == source.ResourceKindArtifact {
		return ctx, func() {}
	}
	seconds := 60
	if q.Repo.MetadataRetrievalTimeoutSecs != nil {
		seconds = *q.Repo.MetadataRetrievalTimeoutSecs
	}
	if seconds <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
}

func (p *Policy) cachedOnly(ctx context.Context, q source.Request) (source.Result, error) {
	if p.kind(q) == types.RemoteMetadata {
		// Cache-only reads deliberately do not resolve credentials or DNS. Retained
		// raw metadata is separate from locally generated package indexes.
		variant := p.CacheKey(q, "").Variant
		var newest types.RemoteCacheEntry
		page := store.Page{Limit: 128}
		for {
			entries, next, err := p.Store.RemoteCache().List(ctx, q.Repo.Key, q.Resource.Path, page)
			if err != nil {
				return source.Result{}, fmt.Errorf("list cached metadata: %w", err)
			}
			for _, e := range entries {
				if e.Token.Key.Path == q.Resource.Path && e.Token.Key.Variant == variant &&
					e.Blob.Checksums.SHA256 != "" &&
					(newest.Blob.Checksums.SHA256 == "" || e.FetchedAt.After(newest.FetchedAt)) {
					newest = e
				}
			}
			if next == "" {
				break
			}
			page.After = next
		}
		return p.result(ctx, newest)
	}
	it, err := p.Store.Items().Get(ctx, q.Repo.Key, true, q.Resource.Path)
	if err != nil {
		return source.Result{}, fmt.Errorf("read remote cache: %w", err)
	}
	if q.Resource.Checksums != (types.Checksums{}) {
		want, parseErr := item.ParseChecksums(q.Resource.Checksums)
		if parseErr != nil {
			return source.Result{}, PolicyError(parseErr)
		}
		if verifyErr := blob.Verify(
			blob.Digests{SHA256: want.SHA256, SHA512: want.SHA512, SHA1: want.SHA1, MD5: want.MD5},
			blob.Digests{
				SHA256: it.Actual.SHA256,
				SHA512: it.Actual.SHA512,
				SHA1:   it.Actual.SHA1,
				MD5:    it.Actual.MD5,
			},
		); verifyErr != nil {
			return source.Result{}, PolicyError(verifyErr)
		}
	}
	return source.Result{Item: &it}, nil
}

// CacheKey is the remote cache key of q under the credential revision: the
// repository key and resource path, revision as SettingsRevision, and as
// Variant a keyed HMAC-SHA256 (Key) of every term that changes the
// representation. The terms are the resource's kind, mutability, escaped
// path, variant and Accept, its query when the repository propagates query
// parameters, its upstream path, external URL (not for a Git LFS object, which
// its SHA-256 names), checksums and expected content type when any is set, and
// each opt-in rule set on it, the NuGet empty feed ("nuget-empty-feed-v1") and
// the accepted media types among them. The keyed fingerprint keeps sensitive
// query values out of an observable key. Cached entries are stored under the
// key, so each term's encoding must stay as it is, and a new opt-in term is
// added only when its option is set, which leaves every existing key unchanged.
// It reads neither the store nor the network.
func (p *Policy) CacheKey(q source.Request, revision string) types.RemoteCacheKey {
	query := ""
	if enabled(q.Repo.PropagateQueryParams) {
		query = q.Resource.Query
	}
	identity := []any{
		q.Resource.Kind, q.Resource.Mutable, q.Resource.EscapedPath,
		q.Resource.Variant, q.Resource.Accept, query,
	}
	// Preserve existing generic cache keys across this opt-in extension.
	externalURL := q.Resource.ExternalURL
	// An LFS object is named and validated by its SHA-256. Batch actions may
	// rotate their signed URL, while the verified object keeps one cache key.
	if q.Repo.PackageType == repo.GitLFS && q.Resource.Variant == "gitlfs-object-v1" {
		externalURL = ""
	}
	if q.Resource.UpstreamPath != "" || externalURL != "" ||
		q.Resource.Checksums != (types.Checksums{}) || q.Resource.ExpectedContentType != "" {
		identity = append(identity, q.Resource.UpstreamPath, externalURL,
			q.Resource.Checksums, q.Resource.ExpectedContentType)
	}
	if q.Resource.ModuleDownloadLocation {
		identity = append(identity, "module-download-location-v1")
	}
	if q.Resource.NuGetEmptyFeed {
		identity = append(identity, "nuget-empty-feed-v1")
	}
	if q.Resource.GoneIsMissing {
		identity = append(identity, "gone-is-missing")
	}
	if q.Resource.RequireSmart {
		identity = append(identity, "require-smart-v1")
	}
	if len(q.Resource.ExpectedContentTypes) > 0 {
		identity = append(identity, q.Resource.ExpectedContentTypes)
	}
	b, _ := json.Marshal(identity)
	mac := hmac.New(sha256.New, p.Key)
	_, _ = mac.Write(b)
	return types.RemoteCacheKey{
		RepoKey:          q.Repo.Key,
		Path:             q.Resource.Path,
		Variant:          hex.EncodeToString(mac.Sum(nil)),
		SettingsRevision: revision,
	}
}

// FlightKey returns the key under which concurrent fills of key for one
// source identity share a single upstream exchange: the hex SHA-256 of both,
// so that callers with different credentials never share a response. Smart
// Remote keys its discovery flights the same way.
func (p *Policy) FlightKey(key types.RemoteCacheKey, identity types.SourceIdentity) string {
	b, _ := json.Marshal([]any{key, identity})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (p *Policy) kind(q source.Request) types.RemoteKind {
	if q.Resource.Kind != source.ResourceKindArtifact {
		return types.RemoteMetadata
	}
	if q.Resource.Mutable {
		return types.RemoteMutable
	}
	var h format.Handler = generic.Handler{}
	if p.Items != nil && p.Items.Formats != nil {
		h = p.Items.Formats(q.Repo.PackageType)
	}
	switch format.ClassOf(h, q.Repo, q.Resource.Path) {
	case format.ClassDerived:
		return types.RemoteDerived
	case format.ClassMetadata:
		return types.RemoteMetadata
	default:
		return types.RemoteUnique
	}
}

// ReadEntry returns the remote cache entry stored under key for the
// permit's source identity: an entry recorded under another identity
// answers store.ErrNotFound, as a missing one does, so a source that
// changed never reads its predecessor's state. A store error comes back
// wrapped.
func (p *Policy) ReadEntry(
	ctx context.Context,
	key types.RemoteCacheKey,
	permit source.Permit,
) (types.RemoteCacheEntry, error) {
	e, err := p.Store.RemoteCache().Read(ctx, key)
	if err != nil {
		// A stored item can have been replaced by another representation at
		// the same path. A miss must discard its former checksum and clock.
		return types.RemoteCacheEntry{}, fmt.Errorf("read remote retrieval state: %w", err)
	}
	if e.Token.Source != permit.Identity {
		return types.RemoteCacheEntry{}, store.ErrNotFound
	}
	return e, nil
}

func (p *Policy) fresh(q source.Request, e types.RemoteCacheEntry) bool {
	if e.Blob.Checksums.SHA256 == "" || !e.NegativeUntil.IsZero() {
		return false
	}
	ttl := 7200 * time.Second
	if q.Repo.RetrievalCachePeriodSecs != nil {
		ttl = time.Duration(*q.Repo.RetrievalCachePeriodSecs) * time.Second
	}
	return parseCacheRules(
		e.CacheControl,
	).fresh(e.Kind != types.RemoteUnique, e.ValidatedAt, p.now(), ttl)
}

func (p *Policy) negativeFresh(q source.Request, e types.RemoteCacheEntry) bool {
	return (q.Repo.MissedRetrievalCachePeriodSecs == nil || *q.Repo.MissedRetrievalCachePeriodSecs > 0) &&
		e.NegativeUntil.After(p.now())
}

func (p *Policy) resultFor(
	ctx context.Context,
	q source.Request,
	e types.RemoteCacheEntry,
) (source.Result, error) {
	if q.Resource.Method == http.MethodHead && e.ItemID == 0 && e.Blob.Checksums.SHA256 != "" {
		modified, _ := http.ParseTime(e.LastModified)
		return source.Result{
			Metadata: source.MetadataResponse{BaseURL: e.MetadataBaseURL},
			Stream: &source.Stream{
				Body:   http.NoBody,
				Status: 200,
				Representation: source.Representation{
					Size:              e.Blob.Size,
					MediaType:         e.ContentType,
					ETag:              e.ETag,
					Modified:          modified,
					Checksums:         e.Blob.Checksums,
					OriginalChecksums: e.OriginalChecksums,
				},
			},
		}, nil
	}
	return p.result(ctx, e)
}

func (p *Policy) result(ctx context.Context, e types.RemoteCacheEntry) (source.Result, error) {
	if e.ItemID > 0 {
		it, err := p.Store.Items().GetByID(ctx, e.ItemID)
		if err != nil {
			return source.Result{}, fmt.Errorf("read cached artifact: %w", err)
		}
		if it.Actual.SHA256 != e.Blob.Checksums.SHA256 {
			return source.Result{}, store.ErrNotFound
		}
		return source.Result{Item: &it, Used: p.used(e.Token)}, nil
	}
	if e.Blob.Checksums.SHA256 == "" {
		return source.Result{}, store.ErrNotFound
	}
	body, info, err := p.Blobs.Open(ctx, e.Blob.Checksums.SHA256)
	if err != nil {
		return source.Result{}, fmt.Errorf("open cached representation: %w", err)
	}
	modified, _ := http.ParseTime(e.LastModified)
	return source.Result{
		Metadata: source.MetadataResponse{BaseURL: e.MetadataBaseURL},
		Stream: &source.Stream{Body: body, Status: 200, Representation: source.Representation{
			Size:              info.Size,
			MediaType:         e.ContentType,
			ETag:              e.ETag,
			Modified:          modified,
			Checksums:         e.Blob.Checksums,
			OriginalChecksums: e.OriginalChecksums,
		}},
		Used: p.used(e.Token),
	}, nil
}

func (p *Policy) used(token types.RemoteCacheToken) func(context.Context) error {
	return func(ctx context.Context) error {
		err := p.Store.RemoteCache().Touch(ctx, token, p.now())
		if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("record remote cache use: %w", err)
		}
		return nil
	}
}

// TransportRequest is the upstream request for q: the resource's escaped
// path, or the path mapped to the upstream's layout, or its external URL,
// with its query, Accept header and body. Given e, the cached complete
// representation of the same resource, it adds e's validator
// (If-None-Match, else If-Modified-Since) so that the upstream can answer
// 304; the caller's own validators never reach the upstream. Format rules
// build their extra exchanges, such as Maven's checksum sidecars, from it
// so that they go out as the policy's own requests do.
func TransportRequest(q source.Request, e types.RemoteCacheEntry) Request {
	path := q.Resource.EscapedPath
	if path == "" {
		path = (&url.URL{Path: q.Resource.Path}).EscapedPath()
	}
	if q.Resource.UpstreamPath != "" {
		path = q.Resource.UpstreamPath
	}
	query := q.Resource.Query
	if q.Resource.ExternalURL != "" {
		path, query = "", ""
	}
	headers := make(http.Header)
	if q.Resource.Accept != "" {
		headers.Set("Accept", q.Resource.Accept)
	}
	if q.Repo.PackageType == repo.GitLFS && q.Resource.Variant == "gitlfs-locks-v1" {
		headers.Set("Content-Type", "application/vnd.git-lfs+json")
	}
	// The caller's validators describe downstream bytes. Only validators of
	// this exact complete upstream representation may revalidate its cache.
	if e.Blob.Checksums.SHA256 != "" && !q.Resource.ModuleDownloadLocation &&
		q.Resource.Method != http.MethodPost {
		if e.ETag != "" {
			headers.Set("If-None-Match", e.ETag)
		} else if e.LastModified != "" {
			headers.Set("If-Modified-Since", e.LastModified)
		}
	}
	return Request{
		Method:              q.Resource.Method,
		Body:                append([]byte(nil), q.Resource.PostBody...),
		Path:                path,
		Query:               query,
		ExternalURL:         q.Resource.ExternalURL,
		CheckURL:            q.Resource.CheckURL,
		NoCrossOriginAuth:   q.Resource.NoCrossOriginAuth,
		GitLFSActionHeaders: q.Resource.GitLFSActionHeaders.Clone(),
		Header:              headers,
		Metadata:            q.Resource.Kind != source.ResourceKindArtifact,
	}
}

// PolicyError classifies a failure of the policy's own work for the source
// service: an error that already carries a *source.HTTPError keeps it; the
// sentinels a caller tests (source.ErrDenied, source.ErrSuperseded,
// store.ErrNotFound and context.Canceled) pass through, a store conflict
// reading as superseded; a timeout becomes a 504, a busy pool a 503, a
// security, connection or integrity failure a 502 of that kind, and
// anything else a 502 protocol failure. Format rules in the ecosystem
// slices return it unwrapped, as the policy does, so that the source
// service maps their failures as it maps the policy's own.
func PolicyError(err error) error {
	var typed *source.HTTPError
	if errors.As(err, &typed) {
		return typed
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return &source.HTTPError{Status: 504, Kind: source.FailureKindTimeout}
	}
	switch {
	case errors.Is(err, source.ErrDenied):
		return source.ErrDenied
	case errors.Is(err, source.ErrSuperseded):
		return source.ErrSuperseded
	case errors.Is(err, store.ErrNotFound):
		return store.ErrNotFound
	case errors.Is(err, store.ErrConflict):
		return source.ErrSuperseded
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return &source.HTTPError{Status: 504, Kind: source.FailureKindTimeout}
	case errors.Is(err, ErrBusy):
		return &source.HTTPError{Status: 503, Kind: source.FailureKindHTTP}
	case errors.Is(err, ErrSecurity), errors.Is(err, ErrRedirect):
		return &source.HTTPError{Status: 502, Kind: source.FailureKindSecurity}
	case errors.Is(err, ErrConnection), errors.Is(err, io.ErrUnexpectedEOF):
		return &source.HTTPError{Status: 502, Kind: source.FailureKindConnectivity}
	case errors.Is(err, item.ErrChecksumMismatch),
		errors.Is(err, item.ErrBadChecksum),
		errors.Is(err, item.ErrRemoteChecksumRequired),
		errors.Is(err, item.ErrRemoteMIME),
		errors.Is(err, item.ErrRemoteLength),
		errors.Is(err, item.ErrRemoteLimit),
		errors.Is(err, blob.ErrDigestMismatch):
		return &source.HTTPError{Status: 502, Kind: source.FailureKindIntegrity}
	default:
		return &source.HTTPError{Status: 502, Kind: source.FailureKindProtocol}
	}
}
