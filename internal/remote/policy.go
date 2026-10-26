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

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/formats"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Policy implements the shared remote retrieval table. Authorization belongs to
// source.Service; CheckCurrent repeats it inside the short publication transaction
// and verifies the effective credential revision without network access.
type Policy struct {
	Smart *SmartRemote
	// Fetch overrides only the protocol exchange. Authentication adapters retain
	// the source identity and credential revision used by this cache operation.
	Fetch func(context.Context, source.Request, source.Permit, Options, Request) (*http.Response, error)
	// PrepareUpload performs bounded adapter work outside SQL, after the main
	// response succeeds and before validation/publication. It uses the same
	// transport security and cannot replace the final CheckCurrent fence.
	PrepareUpload func(context.Context, source.Request, source.Permit, Options, *item.CacheUpload) error
	Store         store.Store
	Blobs         blob.Store
	Items         *item.Service
	Pool          *Pool
	Options       func(context.Context, repo.Config) (Options, error)
	CheckCurrent  func(context.Context, store.Store, source.Request, source.Permit, string) error
	// Key is a stable keyring-derived HMAC key, never a logged cache identifier.
	Key           []byte
	Now           func() time.Time
	MetadataLimit int64
	flights       flightGroup[fillResult]
	probes        flightGroup[fillResult]
	capacity      admission
}

type fillResult struct {
	key       types.RemoteCacheKey
	entry     types.RemoteCacheEntry
	transient bool
	failure   error
}

func (p *Policy) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

func (p *Policy) Read(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
) (out source.Result, retErr error) {
	if p.Smart != nil && ctx.Value(smartActiveKey{}) == nil {
		return p.Smart.Read(ctx, q, permit)
	}
	var revision string
	defer func() {
		if retErr == nil && revision != "" {
			out.Revalidate = func(current context.Context) error { return p.check(current, q, permit, revision) }
		}
	}()
	if permit.Intent == source.CacheOnly || !permit.UpstreamNetworkAllowed ||
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
		return source.Result{}, policyError(err)
	}
	if !mavenCompanionRevision(ctx, opts.Revision) {
		return source.Result{}, source.ErrSuperseded
	}
	revision = opts.Revision
	key := p.cacheKey(q, opts.Revision)
	noStore := transientResource(q)
	if !noStore {
		entry, readErr := p.readEntry(ctx, key, permit)
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
	ctx, cancel := metadataContext(ctx, q)
	defer cancel()
	release, err := p.capacity.acquire(ctx, q.Repo.Key)
	if err != nil {
		return source.Result{}, policyError(err)
	}
	defer release()
	if noStore || q.Resource.Method == http.MethodHead {
		// These requests own their response. A stream or a bodyless HEAD must
		// never become the shared result of a persistent fill.
		return p.direct(ctx, q, permit, opts, key, noStore)
	}
	flightKey := p.flightKey(key, permit.Identity)
	filled, err := p.flights.do(ctx, flightKey, func(shared context.Context) (fillResult, error) {
		return p.fill(shared, q, permit, opts, key)
	})
	if err != nil {
		return source.Result{}, err
	}
	if err = p.check(ctx, q, permit, opts.Revision); err != nil {
		return source.Result{}, policyError(err)
	}
	if filled.transient {
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

func metadataContext(ctx context.Context, q source.Request) (context.Context, context.CancelFunc) {
	if q.Resource.Kind == source.Artifact {
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
		variant := p.cacheKey(q, "").Variant
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
			return source.Result{}, policyError(parseErr)
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
			return source.Result{}, policyError(verifyErr)
		}
	}
	return source.Result{Item: &it}, nil
}

func (p *Policy) cacheKey(q source.Request, revision string) types.RemoteCacheKey {
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

func (p *Policy) flightKey(key types.RemoteCacheKey, identity types.SourceIdentity) string {
	b, _ := json.Marshal([]any{key, identity})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (p *Policy) kind(q source.Request) types.RemoteKind {
	if q.Resource.Kind != source.Artifact {
		return types.RemoteMetadata
	}
	if q.Resource.Mutable {
		return types.RemoteMutable
	}
	h := formats.For(q.Repo.PackageType)
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

func (p *Policy) readEntry(
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

func transportRequest(q source.Request, e types.RemoteCacheEntry) Request {
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
		Method:            q.Resource.Method,
		Body:              append([]byte(nil), q.Resource.PostBody...),
		Path:              path,
		Query:             query,
		ExternalURL:       q.Resource.ExternalURL,
		CheckURL:          q.Resource.CheckURL,
		NoCrossOriginAuth: q.Resource.NoCrossOriginAuth,
		Header:            headers,
		Metadata:          q.Resource.Kind != source.Artifact,
	}
}

func policyError(err error) error {
	var typed *source.HTTPError
	if errors.As(err, &typed) {
		return typed
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return &source.HTTPError{Status: 504, Kind: source.TimeoutFailure}
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
		return &source.HTTPError{Status: 504, Kind: source.TimeoutFailure}
	case errors.Is(err, ErrBusy):
		return &source.HTTPError{Status: 503, Kind: source.HTTPFailure}
	case errors.Is(err, ErrSecurity), errors.Is(err, ErrRedirect):
		return &source.HTTPError{Status: 502, Kind: source.SecurityFailure}
	case errors.Is(err, ErrConnection), errors.Is(err, io.ErrUnexpectedEOF):
		return &source.HTTPError{Status: 502, Kind: source.ConnectivityFailure}
	case errors.Is(err, item.ErrChecksumMismatch),
		errors.Is(err, item.ErrBadChecksum),
		errors.Is(err, item.ErrRemoteChecksumRequired),
		errors.Is(err, item.ErrRemoteMIME),
		errors.Is(err, item.ErrRemoteLength),
		errors.Is(err, item.ErrRemoteLimit),
		errors.Is(err, blob.ErrDigestMismatch):
		return &source.HTTPError{Status: 502, Kind: source.IntegrityFailure}
	default:
		return &source.HTTPError{Status: 502, Kind: source.ProtocolFailure}
	}
}
