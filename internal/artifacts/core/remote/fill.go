package remote

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// fills reports whether Read would keep what it fetches for q in the
// remote's cache, as a file 7.x keeps there, which needs the caller's
// Deploy/Cache (source.AuthorizeFill). Not for a read that stores nothing
// (noStore: storeArtifactsLocally false, a checksum-only or transient
// resource), whose stream 7.x hands over without its deploy check
// (RemoteRepoBase.getResourceStreamHandle, "resource will be stream
// directly to the user"); nor for a checksum request, which 7.x answers
// from the file's info and never downloads (DownloadServiceImpl.respond,
// respondForChecksumRequest); nor for a document 7.x keeps in memory
// (Resource.NoCacheFile); nor for a POST query, which names no file [I].
func fills(q source.Request, noStore bool) bool {
	return !noStore && !q.Resource.ChecksumRequest && fileRead(q)
}

// fillDenied is source.AuthorizeFill's refusal of q's caller where q
// fills the cache (fills), and nil otherwise.
func fillDenied(q source.Request, noStore bool) error {
	if !fills(q, noStore) {
		return nil
	}
	//nolint:wrapcheck // the refusal is the answer, which the item route words
	return source.AuthorizeFill(q)
}

// fileRead reports whether q reads a file 7.x would ask its repository's
// getInfo for, which answers from the cache alone while the remote is
// offline (offline): not a document 7.x keeps in memory, such as a folder
// listing or a search's metadata (Resource.NoCacheFile), nor a POST query
// [I]. Those keep the cooldown's 502, as 7.x's answer for them while the
// remote is assumed offline is not known.
func fileRead(q source.Request) bool {
	return !q.Resource.NoCacheFile && q.Resource.Method != http.MethodPost
}

// withoutFill answers q, which would fill the cache for a caller who may
// not (denied, a *source.CacheFillDenied), as 7.x answers it. 7.161.26's
// RemoteRepoBase.getInfo asks the upstream for the file's info before any
// download, and only the download checks the deploy path
// (downloadAndSave), so the upstream's HEAD decides: a miss is the miss
// (404, remembered as any miss); a copy the cache holds that the upstream
// has not changed is served and made fresh again (304, or a 200 that
// unchanged reads as the same file, as LastModifiedExpiryRemoteInterceptor
// keeps the expired copy, and unexpireIfExists makes it fresh); a file it
// would download is refused with denied. A failure serves an expired copy
// where fallback allows one, as internalGetInfo returns an expired cached
// resource when the upstream fails, and an assumed-offline upstream is not
// asked (offline). Nothing the upstream sends is kept.
func (p *Policy) withoutFill(
	ctx, responseCtx context.Context,
	q source.Request,
	permit source.Permit,
	opts Options,
	key model.RemoteCacheKey,
	denied error,
) (source.Result, error) {
	e, err := p.ReadEntry(ctx, key, permit)
	if err != nil && !errors.Is(err, kernel.ErrNotFound) {
		return source.Result{}, err
	}
	// Another caller may have answered the path since Read looked.
	if p.negativeFresh(q, e) {
		return source.Result{}, kernel.ErrNotFound
	}
	if p.fresh(q, e) {
		return p.resultFor(responseCtx, q, e)
	}
	filled, err := p.info(ctx, q, permit, opts, key, e, denied)
	if err != nil {
		return source.Result{}, err
	}
	if err = p.Check(ctx, q, permit, opts.Revision); err != nil {
		return source.Result{}, PolicyError(err)
	}
	result, err := p.resultFor(responseCtx, q, filled.entry)
	result.Refused = filled.refused
	return result, err
}

// info makes withoutFill's exchange: the upstream's HEAD for q, with the
// validators of e, the copy the cache holds, when it is complete, or a GET
// whose body is closed unread where the remote bypasses HEAD requests, as
// 7.161.26's HttpRepo.createMethodExecutionContext replaces the info
// request's HEAD with a GET (shouldReplaceHeadWithGet). Its result's entry
// is the copy served; its error the answer.
func (p *Policy) info(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	opts Options,
	key model.RemoteCacheKey,
	e model.RemoteCacheEntry,
	denied error,
) (fillResult, error) {
	out := fillResult{key: key}
	if q.Resource.CocoaPodsGit != nil {
		// A Git checkout has no info request to make; 7.x has no
		// CocoaPods Git source of its own to compare [I].
		return out, denied
	}
	state, err := p.State(ctx, q, opts.Revision)
	if err != nil {
		return out, stateFault(ctx, err)
	}
	if p.assumedOffline(state) {
		return p.offline(key, e)
	}
	if failure := p.Cooldown(state); failure != nil {
		return p.fallback(q, key, e, failure)
	}
	token, err := p.Store.RemoteCache().Begin(ctx, key)
	if err != nil {
		return out, PolicyError(err)
	}
	if token.Source != permit.Identity {
		return out, source.ErrSuperseded
	}
	head := q
	head.Resource.Method = http.MethodHead
	if enabled(q.Repo.BypassHeadRequests) {
		head.Resource.Method = http.MethodGet
	}
	res, err := p.fetchResponse(ctx, head, permit, opts, TransportRequest(head, e))
	if err != nil {
		failure := PolicyError(err)
		if stateErr := p.RecordFailure(ctx, head, permit, opts.Revision, failure); stateErr != nil {
			return out, stateErr
		}
		return p.fallback(q, key, e, failure)
	}
	// A GET's body, and the body a format's own exchange may send a HEAD,
	// as the registry's does, are closed unread.
	defer func() { _ = res.Body.Close() }()
	if err = projectModuleLocation(head, res); err != nil {
		return out, err
	}
	if p.ProjectResponse != nil {
		if err = p.ProjectResponse(head, res); err != nil {
			return out, err
		}
	}
	normalizeMissing(head, res)
	if err = p.RecordResponse(ctx, head, permit, opts.Revision, res); err != nil {
		return out, err
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
	default:
		return p.fallback(q, key, e, ResponseError(res, p.now()))
	}
	// 7.x checks the label of its info request before it downloads
	// anything (HttpRepo.handleGetInfoResponse), so a refused label is
	// answered before the deploy path is checked.
	if refused := headMIME(head, res); refused != nil {
		return p.refuse(ctx, q, permit, opts.Revision, token, e, refused)
	}
	if unchanged(e, res) {
		out.entry, err = p.unexpire(ctx, q, permit, opts.Revision, token, e)
		return out, err
	}
	return out, denied
}

// unchanged reports whether res, the upstream's 200 to a HEAD, describes
// e, the complete copy the cache holds: the same ETag when both carry one,
// otherwise a Last-Modified no later than the copy's. 7.161.26's
// LastModifiedExpiryRemoteInterceptor downloads an expired copy again only
// when the upstream's Last-Modified is later than the copy's; the ETag,
// which 7.x does not read there, stands in for an upstream that sends
// none, and an upstream that sends neither is taken to have changed the
// file [I].
func unchanged(e model.RemoteCacheEntry, res *http.Response) bool {
	if e.Blob.Checksums.SHA256 == "" || !e.NegativeUntil.IsZero() {
		return false
	}
	if tag := res.Header.Get("ETag"); tag != "" && e.ETag != "" {
		return strings.TrimPrefix(tag, "W/") == strings.TrimPrefix(e.ETag, "W/")
	}
	upstream, err := http.ParseTime(res.Header.Get("Last-Modified"))
	if err != nil {
		return false
	}
	cached, err := http.ParseTime(e.LastModified)
	return err == nil && !upstream.After(cached)
}
