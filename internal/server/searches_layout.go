package server

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/layout"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The layout searches, GAVC, versions and latest version (decision 0012,
// research 22 §2), read Maven coordinates from a file's path through its
// repository's layout (pkg/layout). The query only narrows, to the files
// under the start each layout gives the group and module asked for; Match,
// with the file's own repository's layout, decides.

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

// layoutScope is what a layout search reads: the layout of each repository
// it may find files in, by stored key, and the criteria that narrow to them.
type layoutScope struct {
	layouts  map[string]*layout.Layout
	criteria aql.Node
}

// layoutHit is a file a layout search found, with its coordinates.
type layoutHit struct {
	aql.Hit
	layout.Coordinates
}

// layoutScope resolves the repositories scope covers (nil for every one) to
// their layouts, keeping those that read an organisation, a module and a
// base revision, and narrows the query to the files under the start each
// layout gives the organisation g and the module a. A layout that does not
// compile reads nothing.
func (r *searchRun) layoutScope(
	ctx context.Context,
	scope aql.Node,
	g, a *string,
) (layoutScope, error) {
	var keys []string
	if scope == nil {
		for k, c := range r.configs {
			if c.Class != repo.Virtual {
				keys = append(keys, k)
			}
		}
	} else {
		for _, k := range scopeKeys(scope) {
			for _, ref := range r.repos.Expand(k) {
				keys = append(keys, ref.Key)
			}
		}
	}
	values := layout.Values{}
	if deref(g) != "" {
		values[layout.TokenOrg] = *g
	}
	if deref(a) != "" {
		values[layout.TokenModule] = *a
	}
	items := &item.Service{Store: r.st}
	byName := map[string]*layout.Layout{}
	out := layoutScope{layouts: map[string]*layout.Layout{}}
	var prefixes []string
	for _, k := range keys {
		c, ok := r.configs[k]
		if !ok {
			continue
		}
		l, seen := byName[c.RepoLayoutRef]
		if !seen {
			var err error
			l, err = items.Layout(ctx, c.RepoLayoutRef)
			switch {
			case errors.Is(err, layout.ErrPattern):
				l = nil
			case err != nil:
				return out, r.s.internalError(ctx, "search layout", err)
			case !l.Has(layout.TokenOrg) || !l.Has(layout.TokenModule) ||
				!l.Has(layout.TokenBaseRev):
				l = nil
			default:
				prefixes = append(prefixes, l.Prefixes(values)...)
			}
			byName[c.RepoLayoutRef] = l
		}
		if l != nil {
			out.layouts[k] = l
		}
	}
	out.criteria = all(files(), scope, under(prefixes...))
	return out, nil
}

// scopeKeys lists the repository keys of a repoScope criterion.
func scopeKeys(scope aql.Node) []string {
	var out []string
	either, _ := scope.(aql.Or)
	for _, n := range either {
		if c, ok := n.(*aql.FieldCriterion); ok {
			out = append(out, c.Text)
		}
	}
	return out
}

// under is the criterion that an item's full path starts with one of the
// prefixes, which may be globs; nil when one is empty, since every path
// starts with that. Both $match patterns may also match deeper or longer
// paths, which is all narrowing needs.
func under(prefixes ...string) aql.Node {
	either := aql.Or{}
	for _, p := range prefixes {
		if p == "" {
			return nil
		}
		dir, rest := types.RootPath, p
		if i := strings.LastIndex(p, "/"); i >= 0 {
			dir, rest = p[:i], p[i+1:]
		}
		either = append(either,
			&aql.FieldCriterion{Field: aql.FieldPath, Op: aql.OpMatch, Text: p + "*"},
			aql.And{
				&aql.FieldCriterion{Field: aql.FieldPath, Op: aql.OpMatch, Text: dir},
				&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpMatch, Text: rest + "*"},
			},
		)
	}
	return either
}

// read returns the coordinates the layout of it's repository reads from
// its path; ok is false when there is no such layout or the path does not
// fit it.
func (ls layoutScope) read(it types.Item) (layout.Coordinates, bool) {
	l := ls.layouts[it.RepoKey]
	if l == nil {
		return layout.Coordinates{}, false
	}
	return l.Read(it.FullPath())
}

// find runs a layout search: the files in scope whose coordinates keep
// accepts, at most want of them (-1 for all).
func (ls layoutScope) find(
	ctx context.Context,
	r *searchRun,
	include []string,
	want int,
	keep func(layout.Coordinates) bool,
) ([]layoutHit, error) {
	if len(ls.layouts) == 0 {
		return nil, nil
	}
	hits, err := r.collect(ctx, ls.criteria, include, want, func(h aql.Hit) bool {
		c, ok := ls.read(h.Item)
		return ok && keep(c)
	})
	if err != nil {
		return nil, err
	}
	out := make([]layoutHit, len(hits))
	for n, h := range hits {
		c, _ := ls.read(h.Item)
		out[n] = layoutHit{h, c}
	}
	return out, nil
}

// wildcard compiles a search parameter in which * is any text and ? one
// character; nil, for a parameter not given or empty, matches anything.
func wildcard(p *string) *regexp.Regexp {
	if deref(p) == "" {
		return nil
	}
	var sb strings.Builder
	sb.WriteString(`(?s)^`)
	for _, r := range *p {
		switch r {
		case '*':
			sb.WriteString(".*")
		case '?':
			sb.WriteString(".")
		default:
			sb.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	sb.WriteString("$")
	return regexp.MustCompile(sb.String())
}

func matches(re *regexp.Regexp, s string) bool { return re == nil || re.MatchString(s) }

// downloadURI is the URL an item downloads from under the key key.
func downloadURI(ctx context.Context, key, fullPath string) string {
	return baseurl.From(ctx) + "/artifactory/" + key + "/" + escapePath(fullPath)
}

// SearchGavc answers the GAVC search: the files whose layout coordinates
// match each of g, a, v and c given. With specific, a virtual key in repos
// searches its members, and each result names the download URL under the
// key the search named, with the extension, version and classifier.
func (s artifactoryService) SearchGavc(
	ctx context.Context,
	req artgen.SearchGavcRequestObject,
) (artgen.SearchGavcResponseObject, error) {
	r, err := s.newSearch(ctx, false)
	if err != nil {
		return nil, err
	}
	p := req.Params
	if deref(p.G) == "" && deref(p.A) == "" && deref(p.V) == "" && deref(p.C) == "" {
		return nil, badRequestf("name at least one of g, a, v and c")
	}
	specific := deref(p.Specific)
	remote, err := gavcRemote(ctx, specific, p.G, p.A)
	if err != nil {
		return nil, err
	}
	scope, _ := r.repoScope(p.Repos, specific)
	ls, err := r.layoutScope(ctx, scope, p.G, p.A)
	if err != nil {
		return nil, err
	}
	g, a, v, c := wildcard(p.G), wildcard(p.A), wildcard(p.V), wildcard(p.C)
	d := detailOf(p.XResultDetail)
	if specific {
		d = resultDetail{}
	}
	found, err := ls.find(ctx, r, d.include(), r.userLimit(), func(co layout.Coordinates) bool {
		return matches(g, co.Org) && matches(a, co.Module) && matches(v, co.Version()) &&
			matches(c, co.Classifier)
	})
	if err != nil {
		return nil, err
	}
	upstream := r.upstreamSearch(ls, p.Repos, p.G, p.A, remote)
	if upstream != nil {
		additional, err := upstream.gavc(ctx, p.V, p.C)
		if err != nil {
			return nil, err
		}
		found = upstream.filterHits(found)
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
		if limit := r.userLimit(); limit >= 0 && len(found) > limit {
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
	named := r.namedKeys(scope)
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
			key = upstream.routeKey(f.Item.RepoKey, f.Item.FullPath(), false, f.Item.Cached)
		}
		e := map[string]any{
			"downloadUri": downloadURI(ctx, key, f.Item.FullPath()),
			"ext":         f.Ext, "version": f.Version(),
		}
		if f.Classifier != "" {
			e["classifier"] = f.Classifier
		}
		entries = append(entries, e)
	}
	if err = upstream.revalidate(ctx); err != nil {
		return nil, err
	}
	return answer(ctx, mediaGavcSearch, results(entries)), nil
}

// namedKeys maps each stored location a repoScope criterion reaches to the
// first key of the criterion that reaches it.
func (r *searchRun) namedKeys(scope aql.Node) map[aql.RepoRef]string {
	out := map[aql.RepoRef]string{}
	for _, k := range scopeKeys(scope) {
		for _, ref := range r.repos.Expand(k) {
			if _, ok := out[ref]; !ok {
				out[ref] = k
			}
		}
	}
	return out
}

// moduleSearch checks a versions or latest version search's parameters: a
// group or a module, and remote 0 or 1.
func moduleSearch[R ~int](g, a *string, remote *R) error {
	if deref(g) == "" && deref(a) == "" {
		return badRequestf("name the group (g), the module (a) or both")
	}
	if remote != nil && *remote != 0 && *remote != 1 {
		return badRequestf("remote must be 0 or 1")
	}
	return nil
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

// versionEntry is one version a module search lists.
type versionEntry struct {
	v           string
	integration bool
}

// hasWildcard reports whether a v parameter is a pattern, which filters the
// listed versions, rather than the module version whose integration
// versions to list.
func hasWildcard(v string) bool { return strings.ContainsAny(v, "*?") }

// moduleVersions lists, each once and newest first in Mercury order, the
// versions of the files in ls whose organisation and module match g and a.
// A v without wildcards lists only the integration file versions of the
// module version v, as 1.0-20260101.120000-1 is of 1.0-SNAPSHOT. Otherwise
// every version counts: an integration one by its file version when files
// is set, else by its module version, which groups a folder's unique
// snapshots as 1.0-SNAPSHOT. A v with wildcards is the caller's to apply.
func moduleVersions(
	ctx context.Context,
	r *searchRun,
	ls layoutScope,
	g, a *string,
	v string,
	files bool,
	upstream *upstreamSearch,
) ([]versionEntry, error) {
	exact := v != "" && !hasWildcard(v)
	gp, ap := wildcard(g), wildcard(a)
	found, err := ls.find(ctx, r, nil, r.capped(0), func(co layout.Coordinates) bool {
		return matches(gp, co.Org) && matches(ap, co.Module) &&
			(!exact || co.Integration() && co.Version() == v)
	})
	if err != nil {
		return nil, err
	}
	var out []versionEntry
	seen := map[string]bool{}
	for _, f := range found {
		if upstream != nil &&
			!upstream.disclose(f.Item.RepoKey, f.Item.FullPath(), false, f.Item.Cached) {
			continue
		}
		ver := f.Version()
		if f.Integration() && (files || exact) {
			ver = f.FileVersion()
		}
		if !seen[ver] {
			seen[ver] = true
			out = append(out, versionEntry{ver, f.Integration()})
		}
	}
	if upstream != nil {
		remote, err := upstream.versions(ctx, v, files)
		if err != nil {
			return nil, err
		}
		for _, entry := range remote {
			if !seen[entry.v] {
				seen[entry.v] = true
				out = append(out, entry)
			}
		}
	}
	slices.SortStableFunc(
		out,
		func(x, y versionEntry) int { return types.CompareMercury(y.v, x.v) },
	)
	if limit := r.capped(0); upstream != nil && limit >= 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// moduleScope resolves a versions or latest version search's repositories,
// virtual keys searching their members, to the layouts that read g and a.
func (r *searchRun) moduleScope(ctx context.Context, repos, g, a *string) (layoutScope, error) {
	scope, _ := r.repoScope(repos, true)
	return r.layoutScope(ctx, scope, g, a)
}

// SearchVersions answers the versions search: the module versions of the
// files whose organisation and module match g and a, with whether each is
// an integration version, newest first in Mercury order. A v with wildcards
// keeps the versions it matches; any other v lists the integration
// versions of that module version. Nothing to list is a 404.
func (s artifactoryService) SearchVersions(
	ctx context.Context,
	req artgen.SearchVersionsRequestObject,
) (artgen.SearchVersionsResponseObject, error) {
	p := req.Params
	if err := moduleSearch(p.G, p.A, p.Remote); err != nil {
		return nil, err
	}
	r, err := s.newSearch(ctx, false)
	if err != nil {
		return nil, err
	}
	ls, err := r.moduleScope(ctx, p.Repos, p.G, p.A)
	if err != nil {
		return nil, err
	}
	v := deref(p.V)
	upstream := r.upstreamSearch(ls, p.Repos, p.G, p.A, deref(p.Remote) == 1)
	versions, err := moduleVersions(ctx, r, ls, p.G, p.A, v, false, upstream)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, apierror.New(http.StatusNotFound, noVersions)
	}
	pattern := wildcard(p.V)
	entries := make([]map[string]any, 0, len(versions))
	for _, x := range versions {
		if hasWildcard(v) && !matches(pattern, x.v) {
			continue
		}
		entries = append(entries, map[string]any{"version": x.v, "integration": x.integration})
	}
	if err = upstream.revalidate(ctx); err != nil {
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
func (s artifactoryService) SearchLatestVersion(
	ctx context.Context,
	req artgen.SearchLatestVersionRequestObject,
) (artgen.SearchLatestVersionResponseObject, error) {
	p := req.Params
	if err := moduleSearch(p.G, p.A, p.Remote); err != nil {
		return nil, err
	}
	r, err := s.newSearch(ctx, false)
	if err != nil {
		return nil, err
	}
	ls, err := r.moduleScope(ctx, p.Repos, p.G, p.A)
	if err != nil {
		return nil, err
	}
	v := deref(p.V)
	upstream := r.upstreamSearch(ls, p.Repos, p.G, p.A, deref(p.Remote) == 1)
	versions, err := moduleVersions(ctx, r, ls, p.G, p.A, v, true, upstream)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, apierror.New(http.StatusNotFound, noVersions)
	}
	keep, missing := func(e versionEntry) bool { return !e.integration }, noLatestRelease
	switch pattern := wildcard(p.V); {
	case hasWildcard(v):
		keep, missing = func(e versionEntry) bool { return matches(pattern, e.v) }, noLatestIntegration
	case v != "":
		keep, missing = func(e versionEntry) bool { return e.integration }, noLatestIntegration
	}
	i := slices.IndexFunc(versions, keep)
	if i < 0 {
		return nil, apierror.New(http.StatusNotFound, missing)
	}
	if err = upstream.revalidate(ctx); err != nil {
		return nil, err
	}
	return artgen.SearchLatestVersion200TextResponse(versions[i].v), nil
}
