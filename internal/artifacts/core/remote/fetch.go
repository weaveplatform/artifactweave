package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func (p *Policy) fill(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	opts Options,
	key model.RemoteCacheKey,
) (fillResult, error) {
	e, err := p.ReadEntry(ctx, key, permit)
	if err != nil && !errors.Is(err, kernel.ErrNotFound) {
		return fillResult{key: key}, err
	}
	if p.negativeFresh(q, e) {
		return fillResult{key: key}, kernel.ErrNotFound
	}
	if p.fresh(q, e) {
		return fillResult{key: key, entry: e}, nil
	}
	state, err := p.State(ctx, q, opts.Revision)
	if err != nil {
		return fillResult{key: key}, err
	}
	if failure := p.Cooldown(state); failure != nil {
		return p.fallback(q, key, e, failure)
	}
	if !state.OfflineUntil.IsZero() {
		// Recovery is source-wide. Different paths share one probe; after a
		// successful probe each path still resolves its own representation.
		probeKey := p.probeKey(key, permit, state)
		filled, probeErr := p.probes.Do(
			ctx,
			probeKey,
			func(shared context.Context) (fillResult, error) { return p.fetch(shared, q, permit, opts, key, e) },
		)
		if filled.key == key {
			if filled.failure != nil {
				return p.fallback(q, key, e, filled.failure)
			}
			return filled, probeErr
		}
		if probeErr == nil {
			probeErr = filled.failure
		}
		latest, stateErr := p.State(ctx, q, opts.Revision)
		if stateErr != nil {
			return fillResult{key: key}, stateErr
		}
		if failure := p.Cooldown(latest); failure != nil {
			return p.fallback(q, key, e, failure)
		}
		if probeErr != nil && !errors.Is(probeErr, kernel.ErrNotFound) {
			return p.fallback(q, key, e, probeErr)
		}
	}
	return p.fetch(ctx, q, permit, opts, key, e)
}

func (p *Policy) fetch(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	opts Options,
	key model.RemoteCacheKey,
	e model.RemoteCacheEntry,
) (fillResult, error) {
	out := fillResult{key: key}
	token, err := p.Store.RemoteCache().Begin(ctx, key)
	if err != nil {
		return out, PolicyError(err)
	}
	if token.Source != permit.Identity {
		return out, source.ErrSuperseded
	}
	res, err := p.fetchResponse(ctx, q, permit, opts, TransportRequest(q, e))
	if err != nil {
		failure := PolicyError(err)
		if stateErr := p.RecordFailure(ctx, q, permit, opts.Revision, failure); stateErr != nil {
			return out, stateErr
		}
		return p.fallback(q, key, e, failure)
	}
	defer func() { _ = res.Body.Close() }()
	if err = projectModuleLocation(q, res); err != nil {
		return out, err
	}
	if p.ProjectResponse != nil {
		if err = p.ProjectResponse(q, res); err != nil {
			return out, err
		}
	}
	normalizeMissing(q, res)
	if res.StatusCode != http.StatusOK {
		if err = p.RecordResponse(ctx, q, permit, opts.Revision, res); err != nil {
			return out, err
		}
	}
	switch res.StatusCode {
	case http.StatusNotFound:
		if err = p.negative(ctx, q, permit, opts.Revision, token); err != nil {
			return out, err
		}
		return out, kernel.ErrNotFound
	case http.StatusNotModified:
		out.entry, err = p.revalidate(ctx, q, permit, opts.Revision, token, e, res)
		return out, err
	case http.StatusOK:
		if mustNotPersist(res.Header) {
			// Complete the probe before recording recovery. Headers alone cannot
			// establish that a body will survive validation or truncation.
			candidate, cacheErr := p.cache(
				ctx, q, permit, opts,
				p.upload(q, permit, opts.Revision, token, res, true),
			)
			if cacheErr != nil {
				failure := PolicyError(cacheErr)
				if stateErr := p.RecordFailure(
					ctx,
					q,
					permit,
					opts.Revision,
					failure,
				); stateErr != nil {
					return out, stateErr
				}
				if refused := mimeRefusal(q, res, cacheErr); refused != nil {
					return out, refused
				}
				return p.fallback(q, key, e, failure)
			}
			_ = candidate.Body.Close()
			if err = p.RecordResponse(ctx, q, permit, opts.Revision, res); err != nil {
				return out, err
			}
			out.isTransient = true
			return out, nil
		}
	default:
		return p.fallback(q, key, e, ResponseError(res, p.now()))
	}
	u := p.upload(q, permit, opts.Revision, token, res, false)
	committed, err := p.cache(ctx, q, permit, opts, u)
	if err != nil {
		failure := PolicyError(err)
		if stateErr := p.RecordFailure(ctx, q, permit, opts.Revision, failure); stateErr != nil {
			return out, stateErr
		}
		if refused := mimeRefusal(q, res, err); refused != nil {
			return out, refused
		}
		return p.fallback(q, key, e, failure)
	}
	out.entry = committed.Entry
	if err = p.RecordResponse(ctx, q, permit, opts.Revision, res); err != nil {
		return out, err
	}
	return out, nil
}

func (p *Policy) direct(
	ctx context.Context, q source.Request, permit source.Permit, opts Options,
	key model.RemoteCacheKey, noStore bool,
) (source.Result, error) {
	var old model.RemoteCacheEntry
	if !noStore {
		var err error
		old, err = p.ReadEntry(ctx, key, permit)
		if err != nil && !errors.Is(err, kernel.ErrNotFound) {
			return source.Result{}, err
		}
	}
	state, err := p.State(ctx, q, opts.Revision)
	if err != nil {
		return source.Result{}, err
	}
	fallback := func(failure error) (source.Result, error) {
		filled, e := p.fallback(q, key, old, failure)
		if e != nil {
			return source.Result{}, e
		}
		return p.resultFor(ctx, q, filled.entry)
	}
	if failure := p.Cooldown(state); failure != nil {
		return fallback(failure)
	}
	if !state.OfflineUntil.IsZero() {
		probe, probeErr := p.probes.Do(
			ctx,
			p.probeKey(key, permit, state),
			func(shared context.Context) (fillResult, error) {
				// Transient bodies have exactly one owner. A recovery probe validates and
				// discards its body; callers then fetch their own response after recovery.
				result, e := p.directOnce(
					shared,
					q,
					permit,
					opts,
					key,
					model.RemoteCacheEntry{},
					true,
				)
				if result.Stream != nil {
					_ = result.Stream.Body.Close()
				}
				return fillResult{}, e
			},
		)
		if probeErr == nil {
			probeErr = probe.failure
		}
		latest, stateErr := p.State(ctx, q, opts.Revision)
		if stateErr != nil {
			return source.Result{}, stateErr
		}
		if failure := p.Cooldown(latest); failure != nil {
			return fallback(failure)
		}
		if probeErr != nil && !errors.Is(probeErr, kernel.ErrNotFound) {
			return fallback(probeErr)
		}
	}
	result, err := p.directOnce(ctx, q, permit, opts, key, old, noStore)
	if err != nil {
		return fallback(err)
	}
	return result, nil
}

func (p *Policy) directOnce(
	ctx context.Context, q source.Request, permit source.Permit, opts Options,
	key model.RemoteCacheKey, old model.RemoteCacheEntry, noStore bool,
) (source.Result, error) {
	token := model.RemoteCacheToken{Key: key, Source: permit.Identity}
	var err error
	if !noStore {
		token, err = p.Store.RemoteCache().Begin(ctx, key)
		if err != nil {
			return source.Result{}, PolicyError(err)
		}
		if token.Source != permit.Identity {
			return source.Result{}, source.ErrSuperseded
		}
	}
	res, err := p.fetchResponse(ctx, q, permit, opts, TransportRequest(q, old))
	if err != nil {
		failure := PolicyError(err)
		if stateErr := p.RecordFailure(ctx, q, permit, opts.Revision, failure); stateErr != nil {
			return source.Result{}, stateErr
		}
		return source.Result{}, failure
	}
	defer func() { _ = res.Body.Close() }()
	if err = projectModuleLocation(q, res); err != nil {
		return source.Result{}, err
	}
	if p.ProjectResponse != nil {
		if err = p.ProjectResponse(q, res); err != nil {
			return source.Result{}, err
		}
	}
	normalizeMissing(q, res)
	if res.StatusCode != http.StatusOK || q.Resource.Method == http.MethodHead {
		if err = p.RecordResponse(ctx, q, permit, opts.Revision, res); err != nil {
			return source.Result{}, err
		}
	}
	switch res.StatusCode {
	case http.StatusNotFound:
		if !noStore {
			if err = p.negative(ctx, q, permit, opts.Revision, token); err != nil {
				return source.Result{}, err
			}
		}
		return source.Result{}, kernel.ErrNotFound
	case http.StatusNotModified:
		if noStore {
			return source.Result{}, &source.HTTPError{Status: 502, Kind: source.FailureKindProtocol}
		}
		updated, e := p.revalidate(ctx, q, permit, opts.Revision, token, old, res)
		if e != nil {
			return source.Result{}, e
		}
		return p.resultFor(ctx, q, updated)
	case http.StatusOK:
	default:
		return source.Result{}, ResponseError(res, p.now())
	}
	if q.Resource.Method == http.MethodHead {
		if refused := headMIME(q, res); refused != nil {
			return source.Result{}, refused
		}
		claimed, parseErr := item.ParseChecksums(kernel.Checksums{
			SHA256: res.Header.Get(
				"X-Checksum-Sha256",
			),
			SHA512: res.Header.Get("X-Checksum-Sha512"),
			SHA1:   res.Header.Get("X-Checksum-Sha1"),
			MD5:    res.Header.Get("X-Checksum-Md5"),
		})
		if parseErr != nil {
			return source.Result{}, &source.HTTPError{Status: 502, Kind: source.FailureKindProtocol}
		}
		modified, _ := http.ParseTime(res.Header.Get("Last-Modified"))
		return source.Result{
			Metadata: metadataResponse(res, p.kind(q)),
			Stream: &source.Stream{
				Body:   http.NoBody,
				Status: 200,
				Representation: source.Representation{
					Checksums: claimed,
					Size:      res.ContentLength,
					MediaType: res.Header.Get("Content-Type"),
					ETag:      res.Header.Get("ETag"),
					Modified:  modified,
				},
			},
		}, nil
	}
	u := p.upload(q, permit, opts.Revision, token, res, true)
	transient, err := p.cache(ctx, q, permit, opts, u)
	if err != nil {
		failure := PolicyError(err)
		if stateErr := p.RecordFailure(ctx, q, permit, opts.Revision, failure); stateErr != nil {
			return source.Result{}, stateErr
		}
		if refused := mimeRefusal(q, res, err); refused != nil {
			return source.Result{}, refused
		}
		return source.Result{}, failure
	}
	if err = p.RecordResponse(ctx, q, permit, opts.Revision, res); err != nil {
		_ = transient.Body.Close()
		return source.Result{}, err
	}
	modified, _ := http.ParseTime(res.Header.Get("Last-Modified"))
	return source.Result{
		Metadata: metadataResponse(res, u.Kind),
		Stream: &source.Stream{
			Body:   transient.Body,
			Status: 200,
			Representation: source.Representation{
				OriginalChecksums: transient.Declared,
				Size:              transient.Info.Size,
				MediaType:         u.ContentType,
				ETag:              u.ETag,
				Modified:          modified,
				Checksums: kernel.Checksums{
					SHA256: transient.Info.Digests.SHA256,
					SHA512: transient.Info.Digests.SHA512,
					SHA1:   transient.Info.Digests.SHA1,
					MD5:    transient.Info.Digests.MD5,
				},
			},
		},
	}, nil
}

func (p *Policy) negative(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	revision string,
	token model.RemoteCacheToken,
) error {
	seconds := 1800
	if q.Repo.MissedRetrievalCachePeriodSecs != nil {
		seconds = *q.Repo.MissedRetrievalCachePeriodSecs
	}
	if seconds <= 0 {
		return nil
	}
	err := p.Store.Tx(ctx, func(tx context.Context) error {
		if currentErr := p.Check(tx, q, permit, revision); currentErr != nil {
			return currentErr
		}
		return p.Store.RemoteCache().
			Negative(tx, token, p.now().Add(time.Duration(seconds)*time.Second))
	})
	if err != nil {
		return PolicyError(err)
	}
	return nil
}

func (p *Policy) probeKey(
	key model.RemoteCacheKey,
	permit source.Permit,
	state model.RemoteSourceState,
) string {
	return p.FlightKey(
		model.RemoteCacheKey{
			RepoKey:          key.RepoKey,
			SettingsRevision: key.SettingsRevision,
			Variant:          state.OfflineUntil.String(),
		},
		permit.Identity,
	)
}

func (p *Policy) upload(
	q source.Request,
	permit source.Permit,
	revision string,
	token model.RemoteCacheToken,
	res *http.Response,
	noStore bool,
) item.CacheUpload {
	kind := p.kind(q)
	limit := p.metadataLimit(kind)
	if q.Resource.ChecksumOnly {
		limit = 4096
	}
	metadata := metadataResponse(res, kind)
	validate := q.Resource.Validate
	if q.Resource.ValidateMetadata != nil {
		validate = func(ctx context.Context, r io.ReadSeeker, size int64) error {
			return q.Resource.ValidateMetadata(ctx, r, size, metadata)
		}
	}
	return item.CacheUpload{
		Expected:             q.Resource.Checksums,
		ExpectedContentType:  q.Resource.ExpectedContentType,
		ExpectedContentTypes: q.Resource.ExpectedContentTypes,
		Validate:             validate,
		RemoteURL:            responseBase(res),
		Token:                token,
		Repo:                 q.Repo,
		Body:                 res.Body,
		Kind:                 kind,
		Length:               res.ContentLength,
		Declared: kernel.Checksums{
			SHA256: res.Header.Get("X-Checksum-Sha256"),
			SHA512: res.Header.Get("X-Checksum-Sha512"),
			SHA1:   res.Header.Get("X-Checksum-Sha1"),
			MD5:    res.Header.Get("X-Checksum-Md5"),
		},
		ContentType: res.Header.Get(
			"Content-Type",
		),
		ETag:         res.Header.Get("ETag"),
		LastModified: res.Header.Get("Last-Modified"),
		CacheControl: persistedCacheControl(res.Header.Get("Cache-Control")),
		Vary:         res.Header.Get("Vary"),
		By:           q.Subject.Name,
		NoStore:      noStore,
		MaxBytes:     limit,
		CheckCurrent: func(ctx context.Context, _ item.Store) error { return p.Check(ctx, q, permit, revision) },
		ClientAccept: clientAccept(q),
	}
}

func clientAccept(q source.Request) []string {
	if q.Resource.ClientMIME == nil {
		return nil
	}
	return q.Resource.ClientMIME.Accept
}

// headMIME is the MIME check of an upstream HEAD for a resource with
// ClientMIME and no format hint, as 7.x checks the label of its info
// request before it downloads anything (HttpRepo.handleGetInfoResponse);
// a HEAD otherwise relays the label unchecked.
func headMIME(q source.Request, res *http.Response) error {
	r := q.Resource
	if r.ClientMIME == nil || r.ExpectedContentType != "" || len(r.ExpectedContentTypes) > 0 {
		return nil
	}
	err := item.RefuseLabel(q.Repo, r.Path, res.Header.Get("Content-Type"), r.ClientMIME.Accept)
	return mimeRefusal(q, res, err)
}

// mimeRefusal is 7.x's answer to a file whose upstream label err refuses,
// for a resource with ClientMIME: 409 with the message of
// HttpRepo.blockMismatchingMimeTypes, ended as
// DownloadServiceImpl.respondResourceNotFound ends a refusal that the info
// request met, with "; Path: '<repository>:<path>'". 7.x checks the label
// of that HEAD before its GET, so a refusal here, where the GET's label is
// checked, is answered as one it would have met there [I]. Nil for any
// other error or resource.
func mimeRefusal(q source.Request, res *http.Response, err error) error {
	var refused *item.MIMERefusal
	if q.Resource.ClientMIME == nil || !errors.As(err, &refused) {
		return nil
	}
	return &source.MIMERefusal{RejectionError: &format.RejectionError{
		Status: http.StatusConflict,
		Message: refused.Message(requestURL(res)) +
			"; Path: '" + q.Repo.Key + ":" + q.Resource.Path + "'",
	}}
}

// requestURL is the URL 7.x names in a refusal: the one it asked for,
// before any redirect, with its path decoded and without credentials, a
// query or a fragment.
func requestURL(res *http.Response) string {
	req := res.Request
	for req != nil && req.Response != nil && req.Response.Request != nil {
		req = req.Response.Request
	}
	if req == nil || req.URL == nil {
		return ""
	}
	return req.URL.Scheme + "://" + req.URL.Host + req.URL.Path
}

func (p *Policy) metadataLimit(kind model.RemoteKind) int64 {
	if kind == model.RemoteMetadata {
		return p.MetadataLimit
	}
	return 0
}

// Check is the authority fence of a read under revision, the credential
// revision the read's options carried. Inside a Maven companion job
// (EagerKey) it answers source.ErrSuperseded when revision is not the one
// the job was queued under. CheckCurrent then decides when it is set (the
// application's repeats the read's authorisation and compares the
// repository's current credential revision); otherwise Check answers
// source.ErrSuperseded unless the repository's source identity is still
// permit's, or the error of reading it. It reads the store, never the
// network, so it can run inside a publication transaction; format rules
// call it after their own upstream exchanges, before anything they fetched
// is used.
func (p *Policy) Check(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	revision string,
) error {
	if !mavenCompanionRevision(ctx, revision) {
		return source.ErrSuperseded
	}
	if p.CheckCurrent != nil {
		return p.CheckCurrent(ctx, p.Store, q, permit, revision)
	}
	current, err := p.Store.RemoteCache().Source(ctx, q.Repo.Key)
	if err != nil {
		return fmt.Errorf("read current remote source: %w", err)
	}
	if current != permit.Identity {
		return source.ErrSuperseded
	}
	return nil
}

func (p *Policy) fallback(
	q source.Request,
	key model.RemoteCacheKey,
	e model.RemoteCacheEntry,
	err error,
) (fillResult, error) {
	var failure *source.HTTPError
	allowed := errors.As(err, &failure) &&
		(failure.Kind == source.FailureKindConnectivity || failure.Kind == source.FailureKindTimeout ||
			(failure.Kind == source.FailureKindHTTP && (failure.Status == 429 || failure.Status >= 500)))
	if allowed && !enabled(q.Repo.HardFail) && e.NegativeUntil.IsZero() &&
		e.Blob.Checksums.SHA256 != "" &&
		parseCacheRules(e.CacheControl).permitsStale() {
		return fillResult{key: key, entry: e, failure: err}, nil
	}
	return fillResult{key: key}, err
}

// Translate only a protocol-declared absence before circuit and negative-cache
// handling, so a GOPROXY 410 neither opens the circuit nor becomes a retryable 502.
func normalizeMissing(q source.Request, res *http.Response) {
	if q.Resource.GoneIsMissing && res.StatusCode == http.StatusGone {
		res.StatusCode = http.StatusNotFound
	}
}

// ResponseError is the error of an upstream answer that is neither content
// nor a protocol-declared absence: a *source.HTTPError carrying 401, 403,
// 429 (with the Retry-After it gives, read against now) or a 5xx status as
// an HTTP failure, and any other status as a 502 protocol failure. Format
// rules that make their own upstream exchanges return it unwrapped, so the
// source service maps the failure as it does for the policy's own reads.
func ResponseError(res *http.Response, now time.Time) error {
	status := res.StatusCode
	if status == 401 || status == 403 || status == 429 || status >= 500 && status <= 599 {
		failure := &source.HTTPError{Status: status, Kind: source.FailureKindHTTP}
		if status == 429 {
			failure.RetryAfter = RetryAfter(res.Header.Get("Retry-After"), now)
		}
		return failure
	}
	return &source.HTTPError{Status: 502, Kind: source.FailureKindProtocol}
}

func responseBase(res *http.Response) string {
	if res.Request == nil || res.Request.URL == nil {
		return ""
	}
	u := *res.Request.URL
	u.User, u.RawQuery, u.Fragment, u.RawFragment, u.ForceQuery = nil, "", "", "", false
	return u.String()
}

func metadataResponse(res *http.Response, kind model.RemoteKind) source.MetadataResponse {
	if kind != model.RemoteMetadata {
		return source.MetadataResponse{}
	}
	return source.MetadataResponse{BaseURL: responseBase(res)}
}

func (p *Policy) revalidate(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	revision string,
	token model.RemoteCacheToken,
	e model.RemoteCacheEntry,
	res *http.Response,
) (model.RemoteCacheEntry, error) {
	bad := e.Blob.Checksums.SHA256 == "" || (e.ETag == "" && e.LastModified == "")
	if got := res.Header.Get(
		"ETag",
	); got != "" && e.ETag != "" &&
		strings.TrimPrefix(got, "W/") != strings.TrimPrefix(e.ETag, "W/") {
		bad = true
	}
	if got := res.Header.Get(
		"Last-Modified",
	); got != "" && e.ETag == "" && e.LastModified != "" &&
		got != e.LastModified {
		bad = true
	}
	if bad {
		return model.RemoteCacheEntry{}, &source.HTTPError{
			Status: 502,
			Kind:   source.FailureKindProtocol,
		}
	}
	if q.Resource.ValidateMetadata != nil {
		metadata := metadataResponse(res, e.Kind)
		if err := p.validateCachedMetadata(ctx, q.Resource, e, metadata); err != nil {
			return model.RemoteCacheEntry{}, PolicyError(err)
		}
		e.MetadataBaseURL = metadata.BaseURL
	}
	for name, target := range map[string]*string{"ETag": &e.ETag, "Last-Modified": &e.LastModified, "Cache-Control": &e.CacheControl, "Vary": &e.Vary} {
		if _, ok := res.Header[http.CanonicalHeaderKey(name)]; ok {
			*target = res.Header.Get(name)
		}
	}
	// A 304 keeps the entry under the repository's period whatever no-store
	// it carries, as a 200 does (persistedCacheControl).
	e.CacheControl = persistedCacheControl(e.CacheControl)
	if !cacheableVary(e.Vary) {
		e.CacheControl = "no-store"
	}
	e.ValidatedAt = p.now()
	e.NegativeUntil = time.Time{}
	var updated model.RemoteCacheEntry
	err := p.Store.Tx(ctx, func(tx context.Context) error {
		if currentErr := p.Check(tx, q, permit, revision); currentErr != nil {
			return currentErr
		}
		var putErr error
		updated, putErr = p.Store.RemoteCache().Put(tx, token, e)
		if putErr != nil {
			return fmt.Errorf("revalidate remote cache: %w", putErr)
		}
		return nil
	})
	if err != nil {
		return model.RemoteCacheEntry{}, PolicyError(err)
	}
	return updated, nil
}

// A 304 at a different redirect destination can change the meaning of every
// relative link even when the bytes are unchanged. Re-admit those complete bytes
// against the current response base before changing persisted retrieval state.
func (p *Policy) validateCachedMetadata(
	ctx context.Context,
	r source.Resource,
	e model.RemoteCacheEntry,
	metadata source.MetadataResponse,
) error {
	body, _, err := p.Blobs.Open(ctx, e.Blob.Checksums.SHA256)
	if err != nil {
		return fmt.Errorf("cached metadata validation: %w", err)
	}
	defer body.Close()
	limit := p.metadataLimit(e.Kind)
	if limit <= 0 {
		limit = 64 << 20
	}
	b, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return fmt.Errorf("cached metadata validation: %w", err)
	}
	if int64(len(b)) > limit {
		return source.ErrUnavailable
	}
	if err = r.ValidateMetadata(ctx, bytes.NewReader(b), int64(len(b)), metadata); err != nil {
		return fmt.Errorf("cached metadata admission: %w", err)
	}
	return nil
}

// cache keeps all adapter I/O outside the transaction and all final authority
// checks inside the item service's normal publication boundary.
func (p *Policy) cache(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	opts Options,
	u item.CacheUpload,
) (item.CacheResult, error) {
	if p.PrepareUpload != nil {
		if err := p.PrepareUpload(ctx, q, permit, opts, &u); err != nil {
			return item.CacheResult{}, err
		}
	}
	out, err := p.Items.Cache(ctx, u)
	out.Declared = u.Declared
	if err != nil {
		return out, fmt.Errorf("cache adapter representation: %w", err)
	}
	return out, nil
}
