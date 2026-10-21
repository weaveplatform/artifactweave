package remote

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func (p *Policy) fill(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	opts Options,
	key types.RemoteCacheKey,
) (fillResult, error) {
	e, err := p.readEntry(ctx, key, permit)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return fillResult{key: key}, err
	}
	if p.negativeFresh(q, e) {
		return fillResult{key: key}, store.ErrNotFound
	}
	if p.fresh(q, e) {
		return fillResult{key: key, entry: e}, nil
	}
	state, err := p.state(ctx, q, opts.Revision)
	if err != nil {
		return fillResult{key: key}, err
	}
	if failure := p.cooldown(state); failure != nil {
		return p.fallback(q, key, e, failure)
	}
	if !state.OfflineUntil.IsZero() {
		// Recovery is source-wide. Different paths share one probe; after a
		// successful probe each path still resolves its own representation.
		probeKey := p.probeKey(key, permit, state)
		filled, probeErr := p.probes.do(
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
		latest, stateErr := p.state(ctx, q, opts.Revision)
		if stateErr != nil {
			return fillResult{key: key}, stateErr
		}
		if failure := p.cooldown(latest); failure != nil {
			return p.fallback(q, key, e, failure)
		}
		if probeErr != nil && !errors.Is(probeErr, store.ErrNotFound) {
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
	key types.RemoteCacheKey,
	e types.RemoteCacheEntry,
) (fillResult, error) {
	out := fillResult{key: key}
	token, err := p.Store.RemoteCache().Begin(ctx, key)
	if err != nil {
		return out, policyError(err)
	}
	if token.Source != permit.Identity {
		return out, source.ErrSuperseded
	}
	client, err := p.Pool.Client(q.Repo, opts)
	if err != nil {
		return out, policyError(err)
	}
	res, err := client.Fetch(ctx, transportRequest(q, e))
	if err != nil {
		failure := policyError(err)
		if stateErr := p.recordFailure(ctx, q, permit, opts.Revision, failure); stateErr != nil {
			return out, stateErr
		}
		return p.fallback(q, key, e, failure)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		if err = p.recordResponse(ctx, q, permit, opts.Revision, res); err != nil {
			return out, err
		}
	}
	switch res.StatusCode {
	case http.StatusNotFound:
		if err = p.negative(ctx, q, permit, opts.Revision, token); err != nil {
			return out, err
		}
		return out, store.ErrNotFound
	case http.StatusNotModified:
		out.entry, err = p.revalidate(ctx, q, permit, opts.Revision, token, e, res)
		return out, err
	case http.StatusOK:
		if parseCacheRules(res.Header.Get("Cache-Control")).noStore ||
			!cacheableVary(res.Header.Get("Vary")) {
			// Complete the probe before recording recovery. Headers alone cannot
			// establish that a body will survive validation or truncation.
			candidate, cacheErr := p.Items.Cache(
				ctx,
				p.upload(q, permit, opts.Revision, token, res, true),
			)
			if cacheErr != nil {
				failure := policyError(cacheErr)
				if stateErr := p.recordFailure(
					ctx,
					q,
					permit,
					opts.Revision,
					failure,
				); stateErr != nil {
					return out, stateErr
				}
				return p.fallback(q, key, e, failure)
			}
			_ = candidate.Body.Close()
			if err = p.recordResponse(ctx, q, permit, opts.Revision, res); err != nil {
				return out, err
			}
			out.transient = true
			return out, nil
		}
	default:
		return p.fallback(q, key, e, responseError(res, p.now()))
	}
	u := p.upload(q, permit, opts.Revision, token, res, false)
	committed, err := p.Items.Cache(ctx, u)
	if err != nil {
		failure := policyError(err)
		if stateErr := p.recordFailure(ctx, q, permit, opts.Revision, failure); stateErr != nil {
			return out, stateErr
		}
		return p.fallback(q, key, e, failure)
	}
	out.entry = committed.Entry
	if err = p.recordResponse(ctx, q, permit, opts.Revision, res); err != nil {
		return out, err
	}
	return out, nil
}

func (p *Policy) direct(
	ctx context.Context, q source.Request, permit source.Permit, opts Options,
	key types.RemoteCacheKey, noStore bool,
) (source.Result, error) {
	var old types.RemoteCacheEntry
	if !noStore {
		var err error
		old, err = p.readEntry(ctx, key, permit)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return source.Result{}, err
		}
	}
	state, err := p.state(ctx, q, opts.Revision)
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
	if failure := p.cooldown(state); failure != nil {
		return fallback(failure)
	}
	if !state.OfflineUntil.IsZero() {
		probe, probeErr := p.probes.do(
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
					types.RemoteCacheEntry{},
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
		latest, stateErr := p.state(ctx, q, opts.Revision)
		if stateErr != nil {
			return source.Result{}, stateErr
		}
		if failure := p.cooldown(latest); failure != nil {
			return fallback(failure)
		}
		if probeErr != nil && !errors.Is(probeErr, store.ErrNotFound) {
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
	key types.RemoteCacheKey, old types.RemoteCacheEntry, noStore bool,
) (source.Result, error) {
	token := types.RemoteCacheToken{Key: key, Source: permit.Identity}
	var err error
	if !noStore {
		token, err = p.Store.RemoteCache().Begin(ctx, key)
		if err != nil {
			return source.Result{}, policyError(err)
		}
		if token.Source != permit.Identity {
			return source.Result{}, source.ErrSuperseded
		}
	}
	client, err := p.Pool.Client(q.Repo, opts)
	if err != nil {
		return source.Result{}, policyError(err)
	}
	res, err := client.Fetch(ctx, transportRequest(q, old))
	if err != nil {
		failure := policyError(err)
		if stateErr := p.recordFailure(ctx, q, permit, opts.Revision, failure); stateErr != nil {
			return source.Result{}, stateErr
		}
		return source.Result{}, failure
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || q.Resource.Method == http.MethodHead {
		if err = p.recordResponse(ctx, q, permit, opts.Revision, res); err != nil {
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
		return source.Result{}, store.ErrNotFound
	case http.StatusNotModified:
		if noStore {
			return source.Result{}, &source.HTTPError{Status: 502, Kind: source.ProtocolFailure}
		}
		updated, e := p.revalidate(ctx, q, permit, opts.Revision, token, old, res)
		if e != nil {
			return source.Result{}, e
		}
		return p.resultFor(ctx, q, updated)
	case http.StatusOK:
	default:
		return source.Result{}, responseError(res, p.now())
	}
	if q.Resource.Method == http.MethodHead {
		claimed, parseErr := item.ParseChecksums(types.Checksums{
			SHA256: res.Header.Get(
				"X-Checksum-Sha256",
			),
			SHA512: res.Header.Get("X-Checksum-Sha512"),
			SHA1:   res.Header.Get("X-Checksum-Sha1"),
			MD5:    res.Header.Get("X-Checksum-Md5"),
		})
		if parseErr != nil {
			return source.Result{}, &source.HTTPError{Status: 502, Kind: source.ProtocolFailure}
		}
		modified, _ := http.ParseTime(res.Header.Get("Last-Modified"))
		return source.Result{
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
	transient, err := p.Items.Cache(ctx, u)
	if err != nil {
		failure := policyError(err)
		if stateErr := p.recordFailure(ctx, q, permit, opts.Revision, failure); stateErr != nil {
			return source.Result{}, stateErr
		}
		return source.Result{}, failure
	}
	if err = p.recordResponse(ctx, q, permit, opts.Revision, res); err != nil {
		_ = transient.Body.Close()
		return source.Result{}, err
	}
	modified, _ := http.ParseTime(res.Header.Get("Last-Modified"))
	return source.Result{
		Stream: &source.Stream{
			Body:   transient.Body,
			Status: 200,
			Representation: source.Representation{
				Size:      transient.Info.Size,
				MediaType: u.ContentType,
				ETag:      u.ETag,
				Modified:  modified,
				Checksums: types.Checksums{
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
	token types.RemoteCacheToken,
) error {
	seconds := 1800
	if q.Repo.MissedRetrievalCachePeriodSecs != nil {
		seconds = *q.Repo.MissedRetrievalCachePeriodSecs
	}
	if seconds <= 0 {
		return nil
	}
	err := p.Store.Tx(ctx, func(tx context.Context) error {
		if currentErr := p.check(tx, q, permit, revision); currentErr != nil {
			return currentErr
		}
		return p.Store.RemoteCache().
			Negative(tx, token, p.now().Add(time.Duration(seconds)*time.Second))
	})
	if err != nil {
		return policyError(err)
	}
	return nil
}

func (p *Policy) probeKey(
	key types.RemoteCacheKey,
	permit source.Permit,
	state types.RemoteSourceState,
) string {
	return p.flightKey(
		types.RemoteCacheKey{
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
	token types.RemoteCacheToken,
	res *http.Response,
	noStore bool,
) item.CacheUpload {
	kind := p.kind(q)
	limit := p.metadataLimit(kind)
	if q.Resource.ChecksumOnly {
		limit = 4096
	}
	origin := ""
	if res.Request != nil && res.Request.URL != nil {
		u := *res.Request.URL
		u.User, u.RawQuery, u.Fragment, u.RawFragment, u.ForceQuery = nil, "", "", "", false
		origin = u.String()
	}
	return item.CacheUpload{
		RemoteURL: origin,
		Token:     token,
		Repo:      q.Repo,
		Body:      res.Body,
		Kind:      kind,
		Length:    res.ContentLength,
		Declared: types.Checksums{
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
		CacheControl: res.Header.Get(
			"Cache-Control",
		),
		Vary:         res.Header.Get("Vary"),
		By:           q.Subject.Name,
		NoStore:      noStore,
		MaxBytes:     limit,
		CheckCurrent: func(ctx context.Context, _ store.Store) error { return p.check(ctx, q, permit, revision) },
	}
}

func (p *Policy) metadataLimit(kind types.RemoteKind) int64 {
	if kind == types.RemoteMetadata {
		return p.MetadataLimit
	}
	return 0
}

func (p *Policy) check(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	revision string,
) error {
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
	key types.RemoteCacheKey,
	e types.RemoteCacheEntry,
	err error,
) (fillResult, error) {
	var failure *source.HTTPError
	allowed := errors.As(err, &failure) &&
		(failure.Kind == source.ConnectivityFailure || failure.Kind == source.TimeoutFailure ||
			(failure.Kind == source.HTTPFailure && (failure.Status == 429 || failure.Status >= 500)))
	if allowed && !enabled(q.Repo.HardFail) && e.NegativeUntil.IsZero() &&
		e.Blob.Checksums.SHA256 != "" &&
		parseCacheRules(e.CacheControl).permitsStale() {
		return fillResult{key: key, entry: e, failure: err}, nil
	}
	return fillResult{key: key}, err
}

func responseError(res *http.Response, now time.Time) error {
	status := res.StatusCode
	if status == 401 || status == 403 || status == 429 || status >= 500 && status <= 599 {
		failure := &source.HTTPError{Status: status, Kind: source.HTTPFailure}
		if status == 429 {
			failure.RetryAfter = retryAfter(res.Header.Get("Retry-After"), now)
		}
		return failure
	}
	return &source.HTTPError{Status: 502, Kind: source.ProtocolFailure}
}

func (p *Policy) revalidate(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	revision string,
	token types.RemoteCacheToken,
	e types.RemoteCacheEntry,
	res *http.Response,
) (types.RemoteCacheEntry, error) {
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
		return types.RemoteCacheEntry{}, &source.HTTPError{
			Status: 502,
			Kind:   source.ProtocolFailure,
		}
	}
	for name, target := range map[string]*string{"ETag": &e.ETag, "Last-Modified": &e.LastModified, "Cache-Control": &e.CacheControl, "Vary": &e.Vary} {
		if _, ok := res.Header[http.CanonicalHeaderKey(name)]; ok {
			*target = res.Header.Get(name)
		}
	}
	if !cacheableVary(e.Vary) {
		e.CacheControl = "no-store"
	}
	e.ValidatedAt = p.now()
	e.NegativeUntil = time.Time{}
	var updated types.RemoteCacheEntry
	err := p.Store.Tx(ctx, func(tx context.Context) error {
		if currentErr := p.check(tx, q, permit, revision); currentErr != nil {
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
		return types.RemoteCacheEntry{}, policyError(err)
	}
	return updated, nil
}
