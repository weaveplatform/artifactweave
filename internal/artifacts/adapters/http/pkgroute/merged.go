package pkgroute

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
)

// Merged answers the merged metadata document at path of the request's
// repository with build, which merges it as the format does without the
// cache (MergeMembers, Collect). Through a virtual repository whose format
// carries virtualRetrievalCachePeriodSecs, Merged keeps the answer in the
// server's cache (Options.Cache) for the virtual repository's period and
// answers a later request from it (decision 0008, R8): 7.x stores a
// virtual's merged metadata and builds it again only once it is older
// than the period (VirtualRepo.isCachedResourceExpired). path is the one
// MergeMembers takes, and the virtual repository's download policy refuses
// it before the cache is read. document names the document and any input
// of its merge besides the members, the caller and the base URL, such as
// an Accept variant.
//
// An entry is the virtual repository's for one member list, one set of
// members the caller's walk reads, and one base URL. A caller who may read
// only part of a member the walk keeps, or only part of the virtual
// repository itself, gets a merge of its own on every request, which is
// never kept: such a merge drops rows the caller may not read, so no other
// caller may get it (decision 0006; 7.x keeps one cached index per
// permission set, RTFACT-25936). Callers who may read the whole virtual
// repository, through an "ANY" permission target, and those who may read
// none of it, which grants on members alone give, never share an entry. A
// repository that is not virtual merges on every request.
//
// build gets the request with its context values and without its
// cancellation, since its answer may serve requests waiting on it; its
// error is returned as it is, after the cache has handled it
// (virtual.Cache.Get).
func (k *Kit) Merged(
	r *http.Request,
	path, document string,
	build func(*http.Request) (virtual.Entry, error),
) (virtual.Entry, error) {
	if err := k.CheckDocument(r, path); err != nil {
		return virtual.Entry{}, err
	}
	v := RequestFrom(r).Repo
	if v.Class != repo.Virtual || k.o.Cache == nil {
		return build(r)
	}
	steps, kept, _, err := k.walk(r, path)
	if err != nil {
		return virtual.Entry{}, err
	}
	whole, ok := virtualReach(r.Context(), v)
	for _, m := range kept {
		ok = ok && readsAll(r.Context(), m.Repo)
	}
	if !ok {
		return build(r)
	}
	key := virtual.CacheKey{
		Virtual:    v.Key,
		Document:   document,
		Members:    digest(spell(steps)...),
		Visibility: digest(append(spell(kept), whole)...),
		Base:       baseurl.From(r.Context()),
	}
	ctx := r.Context()
	if k.p.MergeWait <= 0 {
		//nolint:wrapcheck // the cache preserves the builder's answer
		return k.o.Cache.Get(
			ctx,
			key,
			virtual.Period(v),
			func(ctx context.Context) (virtual.Entry, error) { return build(r.WithContext(ctx)) },
		)
	}
	waitCtx, cancel := context.WithTimeout(ctx, k.p.MergeWait)
	defer cancel()
	entry, err := k.o.Cache.Get(waitCtx, key, virtual.Period(v), func(ctx context.Context) (virtual.Entry, error) { return build(r.WithContext(ctx)) })
	if errors.Is(waitCtx.Err(), context.DeadlineExceeded) && errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return build(r)
	}
	return entry, err //nolint:wrapcheck // the cache preserves the builder's answer
}

// MergedPart is Merged for one member's part of a merged document, for a
// format that keeps only its remote members' parts for the period and
// merges them with its local members' fresh ones on every request: Conda's
// docs say a virtual's metadata reflects local members "in real-time"
// while remote members' parts are renewed on demand
// (conda-repositories.md). mr is a member request MergeMembers or Collect
// handed the format; its part is kept under virtual.PartOf(document,
// member) in the virtual repository it came through, for that virtual
// repository's period. A request that is not a member request, and a
// caller who may not read the whole of the virtual repository or of the
// member, build on every request.
func (k *Kit) MergedPart(
	mr *http.Request,
	document string,
	build func(*http.Request) (virtual.Entry, error),
) (virtual.Entry, error) {
	q := RequestFrom(mr)
	if q.virtual == nil || k.o.Cache == nil {
		return build(mr)
	}
	whole, ok := virtualReach(mr.Context(), *q.virtual)
	if !ok || !readsAll(mr.Context(), q.Repo) {
		return build(mr)
	}
	step := spell([]virtual.Member{{
		Repo: q.Repo, Upstream: q.Repo.Class == repo.Remote && !q.CacheOnly,
	}})
	key := virtual.CacheKey{
		Virtual:    q.virtual.Key,
		Document:   virtual.PartOf(document, q.Repo.Key),
		Members:    digest(step...),
		Visibility: digest(append(step, whole)...),
		Base:       baseurl.From(mr.Context()),
	}
	return k.o.Cache.Get(mr.Context(), key, virtual.Period(*q.virtual), //nolint:wrapcheck // build's own answer
		func(ctx context.Context) (virtual.Entry, error) { return build(mr.WithContext(ctx)) })
}

// EvictMerged drops document, with its members' parts, of the virtual
// repository the request addressed or came through from the cache, for a
// format whose research says a member's 404 drops the cached document
// (decision 0008, R8; ArtifactoryVirtualDownloadStrategy.getInfoInternal
// 90-136). A build that finds no member serving the document drops its
// entry by itself (virtual.Cache.Get). Through a repository that is not
// virtual it does nothing.
func (k *Kit) EvictMerged(r *http.Request, document string) {
	q := RequestFrom(r)
	v := q.Repo
	if q.virtual != nil {
		v = *q.virtual
	}
	if v.Class == repo.Virtual {
		k.o.Cache.Evict(v.Key, document)
	}
}

// readsAll reports whether the caller of ctx may read every path of c.
func readsAll(ctx context.Context, c repo.Config) bool {
	d, ok := accessapi.DeciderFrom(ctx)
	return ok && d.AllowsAll(accessapi.ResourceArtifact, accessapi.Repo{
		Key: c.Key, Class: string(c.Class),
	}, "", accessapi.ActionRead)
}

// virtualReach is what the caller of ctx may read of the virtual
// repository v itself, as a key part: "all" or "none". ok is false when it
// may read part of it, whose merge a format may filter by Read on the
// virtual repository's paths. Only an "ANY" target reaches a virtual
// repository, since none can be a permission target of its own.
func virtualReach(ctx context.Context, v repo.Config) (string, bool) {
	if readsAll(ctx, v) {
		return "all", true
	}
	d, ok := accessapi.DeciderFrom(ctx)
	some := ok && d.AllowsFolder(accessapi.ResourceArtifact, accessapi.Repo{
		Key: v.Key, Class: string(v.Class),
	}, "", accessapi.ActionRead)
	return "none", !some
}

// spell names a walk's steps: each member's key, with "^" for an upstream
// step.
func spell(steps []virtual.Member) []string {
	out := make([]string, len(steps))
	for i, m := range steps {
		out[i] = m.Repo.Key
		if m.Upstream {
			out[i] += "^"
		}
	}
	return out
}

// digest hashes parts for a cache key, so a long member list keeps the key
// short.
func digest(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

// MergedRawPart caches a remote member's immutable, unfiltered intermediate for
// formats that re-authorize and filter it on every request, including scoped
// callers. It deliberately does not change MergedPart's conservative contract
// for already-filtered documents. visibility identifies the incoming subject's
// scope; document must hash sensitive query/source inputs because cache logs name
// it. The format checks current source authority before lookup and disclosure.
func (k *Kit) MergedRawPart(mr *http.Request, document, visibility string, build func(*http.Request) (virtual.Entry, error)) (virtual.Entry, error) {
	q := RequestFrom(mr)
	if q.virtual == nil || q.Repo.Class != repo.Remote || k.o.Cache == nil || visibility == "" {
		return build(mr)
	}
	step := spell([]virtual.Member{{Repo: q.Repo, Upstream: !q.CacheOnly}})
	key := virtual.CacheKey{
		Virtual:    q.virtual.Key,
		Document:   virtual.PartOf(document, q.Repo.Key),
		Members:    digest(step...),
		Visibility: digest(append(step, visibility)...),
		Base:       baseurl.From(mr.Context()),
	}
	entry, err := k.o.Cache.Get(mr.Context(), key, virtual.Period(*q.virtual), func(ctx context.Context) (virtual.Entry, error) {
		value, err := build(mr.WithContext(ctx))
		if err != nil {
			return virtual.Entry{}, &rawPartFailure{cause: err}
		}
		return value, nil
	})
	var failure *rawPartFailure
	if errors.As(err, &failure) {
		return virtual.Entry{}, failure.cause
	}
	return entry, err //nolint:wrapcheck // preserves cache cancellation and the builder's answer
}

// Raw intermediates must not hide a failed refresh behind stale fallback. The
// marker evicts only this entry; callers still receive the original typed fault.
type rawPartFailure struct{ cause error }

func (e *rawPartFailure) Error() string   { return e.cause.Error() }
func (e *rawPartFailure) Unwrap() []error { return []error{virtual.ErrPassOver, e.cause} }
