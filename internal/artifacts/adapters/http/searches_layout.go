package artifactshttp

import (
	"context"
	"net/http"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/layout"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/search"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// The layout searches, GAVC, versions and latest version (decision 0012,
// research 22 §2), read Maven coordinates from a file's path through its
// repository's layout (internal/artifacts/core/layout). The query only
// narrows, to the files under the start each layout gives the group and module
// asked for; Match, with the file's own repository's layout, decides.

const (
	mediaGavcSearch     = "application/vnd.org.jfrog.artifactory.search.GavcSearchResult+json"
	mediaVersionsSearch = "application/vnd.org.jfrog.artifactory.search.ArtifactVersionsResult+json"
)

func (r searchResponse) VisitSearchGavcResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (r searchResponse) VisitSearchVersionsResponse(w http.ResponseWriter) error {
	return r.write(w)
}

// SearchGavc answers the GAVC search: the files whose layout coordinates
// match each of g, a, v and c given. With specific, a virtual key in repos
// searches its members, and each result names the download URL under the
// key the search named, with the extension, version and classifier.
func (s ArtifactoryAPI) SearchGavc(
	ctx context.Context,
	req artgen.SearchGavcRequestObject,
) (artgen.SearchGavcResponseObject, error) {
	r, err := s.newSearch(ctx, false)
	if err != nil {
		return nil, err
	}
	p := req.Params
	if httpkit.Deref(p.G) == "" && httpkit.Deref(p.A) == "" && httpkit.Deref(p.V) == "" &&
		httpkit.Deref(p.C) == "" {
		return nil, httpkit.BadRequestf("name at least one of g, a, v and c")
	}
	specific := httpkit.Deref(p.Specific)
	remote, err := gavcRemote(ctx, specific, p.G, p.A)
	if err != nil {
		return nil, err
	}
	scope, _ := r.RepoScope(p.Repos, specific)
	ls, err := r.LayoutScope(ctx, scope, p.G, p.A)
	if err != nil {
		return nil, err
	}
	g, a := search.Wildcard(p.G), search.Wildcard(p.A)
	v, c := search.Wildcard(p.V), search.Wildcard(p.C)
	d := detailOf(p.XResultDetail)
	if specific {
		d = resultDetail{}
	}
	found, err := ls.Find(
		ctx,
		r.SearchRun,
		d.include(),
		r.UserLimit(),
		func(co layout.Coordinates) bool {
			return search.Matches(g, co.Org) && search.Matches(a, co.Module) &&
				search.Matches(v, co.Version()) &&
				search.Matches(c, co.Classifier)
		},
	)
	if err != nil {
		return nil, err
	}
	upstream, routes := r.upstreamSearch(ls, p.Repos, p.G, p.A, remote)
	if upstream != nil {
		additional, err := upstream.Gavc(ctx, p.V, p.C)
		if err != nil {
			return nil, err
		}
		found = routes.filterHits(found)
		seen := map[string]bool{}
		for _, hit := range found {
			seen[hit.Item.RepoKey+"/"+hit.Item.FullPath()] = true
		}
		for _, hit := range additional {
			identity := hit.Item.RepoKey + "/" + hit.Item.FullPath()
			if !seen[identity] {
				seen[identity] = true
				found = append(found, hit)
			}
		}
		if limit := r.UserLimit(); limit >= 0 && len(found) > limit {
			found = found[:limit]
		}
	}
	if !specific {
		hits := make([]aql.Hit, len(found))
		for n, f := range found {
			hits[n] = f.Hit
		}
		entries, err := r.entries(ctx, hits, d)
		if err != nil {
			return nil, err
		}
		return answer(ctx, mediaGavcSearch, results(entries)), nil
	}
	named := r.NamedKeys(scope)
	// The scope's cache references also name the upstream files it discovers.
	for ref, key := range named {
		if ref.Cached {
			named[aql.RepoRef{Key: ref.Key}] = key
		}
	}
	entries := make([]map[string]any, 0, len(found))
	for _, f := range found {
		key, ok := named[aql.RepoRef{Key: f.Item.RepoKey, Cached: f.Item.Cached}]
		if !ok {
			key = f.Item.ReportedRepoKey()
		}
		if upstream != nil {
			key = routes.RouteKey(f.Item.RepoKey, f.Item.FullPath(), false, f.Item.Cached)
		}
		e := map[string]any{
			"downloadUri": itemkit.DownloadURI(ctx, key, f.Item.FullPath()),
			"ext":         f.Ext, "version": f.Version(),
		}
		if f.Classifier != "" {
			e["classifier"] = f.Classifier
		}
		entries = append(entries, e)
	}
	if err = upstream.Revalidate(ctx); err != nil {
		return nil, err
	}
	return answer(ctx, mediaGavcSearch, results(entries)), nil
}

// The versions and latest version searches answer from one list, as
// Artifactory's do (its search add-on's getArtifactVersions, read by
// ArtifactVersionsSearchResource and ArtifactLatestVersionSearchResource):
// the module's versions, newest first in Mercury order.

// Artifactory's messages for a versions or latest version search that finds
// nothing.
const (
	noVersions          = "Unable to find artifact versions"
	noLatestRelease     = "Latest release version not found"
	noLatestIntegration = "Latest integration version not found"
)

// moduleUpstream is u as search.ModuleVersions takes it: a nil
// *UpstreamSearch stored in the interface is not nil, and ModuleVersions
// would call into it, so no upstream search is an untyped nil.
func moduleUpstream(u *search.UpstreamSearch) search.Upstream {
	if u == nil {
		return nil
	}
	return u
}

// SearchVersions answers the versions search: the module versions of the
// files whose organisation and module match g and a, with whether each is
// an integration version, newest first in Mercury order. A v with wildcards
// keeps the versions it matches; any other v lists the integration
// versions of that module version. Nothing to list is a 404.
func (s ArtifactoryAPI) SearchVersions(
	ctx context.Context,
	req artgen.SearchVersionsRequestObject,
) (artgen.SearchVersionsResponseObject, error) {
	p := req.Params
	if err := search.ModuleSearch(p.G, p.A, p.Remote); err != nil {
		return nil, err
	}
	r, err := s.newSearch(ctx, false)
	if err != nil {
		return nil, err
	}
	ls, err := r.ModuleScope(ctx, p.Repos, p.G, p.A)
	if err != nil {
		return nil, err
	}
	v := httpkit.Deref(p.V)
	upstream, _ := r.upstreamSearch(ls, p.Repos, p.G, p.A, httpkit.Deref(p.Remote) == 1)
	versions, err := search.ModuleVersions(
		ctx, r.SearchRun, ls, p.G, p.A, v, false, moduleUpstream(upstream),
	)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, apierror.New(http.StatusNotFound, noVersions)
	}
	pattern := search.Wildcard(p.V)
	entries := make([]map[string]any, 0, len(versions))
	for _, x := range versions {
		if search.HasWildcard(v) && !search.Matches(pattern, x.V) {
			continue
		}
		entries = append(entries, map[string]any{"version": x.V, "integration": x.IsIntegration})
	}
	if err = upstream.Revalidate(ctx); err != nil {
		return nil, err
	}
	return answer(ctx, mediaVersionsSearch, results(entries)), nil
}

// SearchLatestVersion answers the latest version search, in plain text, the
// first of the module's versions as the versions search lists them, by file
// version: without v the first release; with a v that has wildcards the
// first version of either kind it matches; with any other v the first
// integration version of that module version. Each way of finding nothing
// has Artifactory's own 404.
func (s ArtifactoryAPI) SearchLatestVersion(
	ctx context.Context,
	req artgen.SearchLatestVersionRequestObject,
) (artgen.SearchLatestVersionResponseObject, error) {
	p := req.Params
	if err := search.ModuleSearch(p.G, p.A, p.Remote); err != nil {
		return nil, err
	}
	r, err := s.newSearch(ctx, false)
	if err != nil {
		return nil, err
	}
	ls, err := r.ModuleScope(ctx, p.Repos, p.G, p.A)
	if err != nil {
		return nil, err
	}
	v := httpkit.Deref(p.V)
	upstream, _ := r.upstreamSearch(ls, p.Repos, p.G, p.A, httpkit.Deref(p.Remote) == 1)
	versions, err := search.ModuleVersions(
		ctx, r.SearchRun, ls, p.G, p.A, v, true, moduleUpstream(upstream),
	)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, apierror.New(http.StatusNotFound, noVersions)
	}
	keep, missing := func(e search.VersionEntry) bool { return !e.IsIntegration }, noLatestRelease
	switch pattern := search.Wildcard(p.V); {
	case search.HasWildcard(v):
		keep, missing = func(e search.VersionEntry) bool { return search.Matches(pattern, e.V) }, noLatestIntegration
	case v != "":
		keep, missing = func(e search.VersionEntry) bool { return e.IsIntegration }, noLatestIntegration
	}
	i := slices.IndexFunc(versions, keep)
	if i < 0 {
		return nil, apierror.New(http.StatusNotFound, missing)
	}
	if err = upstream.Revalidate(ctx); err != nil {
		return nil, err
	}
	return artgen.SearchLatestVersion200TextResponse(versions[i].V), nil
}
