package server

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"path"
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

// coordinates are what a layout reads from one file's path.
type coordinates struct {
	org, module, baseRev, folderItegRev, fileItegRev, classifier, ext string
}

// version is the module version a file belongs to: the base revision with
// the folder's integration revision, or the file's when the folder has
// none, as in 1.0-SNAPSHOT.
func (c coordinates) version() string {
	return withRev(c.baseRev, cmp.Or(c.folderItegRev, c.fileItegRev))
}

// fileVersion is the file's own version: the base revision with the file's
// integration revision, as in 1.0-20260101.120000-1.
func (c coordinates) fileVersion() string {
	return withRev(c.baseRev, cmp.Or(c.fileItegRev, c.folderItegRev))
}

func (c coordinates) integration() bool { return c.folderItegRev != "" || c.fileItegRev != "" }

func withRev(base, rev string) string {
	if rev == "" {
		return base
	}
	return base + "-" + rev
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
	coordinates
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
func (ls layoutScope) read(it types.Item) (coordinates, bool) {
	l := ls.layouts[it.RepoKey]
	if l == nil {
		return coordinates{}, false
	}
	v, err := l.Match(it.FullPath())
	if err != nil {
		return coordinates{}, false
	}
	c := coordinates{
		org: v[layout.TokenOrg], module: v[layout.TokenModule], baseRev: v[layout.TokenBaseRev],
		folderItegRev: v[layout.TokenFolderItegRev], fileItegRev: v[layout.TokenFileItegRev],
		classifier: v[layout.TokenClassifier], ext: v[layout.TokenExt],
	}
	// A descriptor pattern spells its extension as a literal, as the Maven
	// layout's .pom does, so the name's is the file's.
	if c.ext == "" {
		c.ext = strings.TrimPrefix(path.Ext(it.Name), ".")
	}
	return c, c.module != "" && c.baseRev != ""
}

// find runs a layout search: the files in scope whose coordinates keep
// accepts, at most want of them (-1 for all).
func (ls layoutScope) find(
	ctx context.Context,
	r *searchRun,
	include []string,
	want int,
	keep func(coordinates) bool,
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
		return nil, badRequest("name at least one of g, a, v and c")
	}
	specific := deref(p.Specific)
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
	found, err := ls.find(ctx, r, d.include(), r.userLimit(), func(co coordinates) bool {
		return matches(g, co.org) && matches(a, co.module) && matches(v, co.version()) &&
			matches(c, co.classifier)
	})
	if err != nil {
		return nil, err
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
	entries := make([]map[string]any, 0, len(found))
	for _, f := range found {
		key, ok := named[aql.RepoRef{Key: f.Item.RepoKey, Cached: f.Item.Cached}]
		if !ok {
			key = f.Item.ReportedRepoKey()
		}
		e := map[string]any{
			"downloadUri": downloadURI(ctx, key, f.Item.FullPath()),
			"ext":         f.ext, "version": f.version(),
		}
		if f.classifier != "" {
			e["classifier"] = f.classifier
		}
		entries = append(entries, e)
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
// group or a module, and remote 0 or 1. remote=1 asks for the upstream of
// remote repositories too; until they proxy (P9), both answer from local
// repositories and caches.
func moduleSearch[R ~int](g, a *string, remote *R) error {
	if deref(g) == "" && deref(a) == "" {
		return badRequest("name the group (g), the module (a) or both")
	}
	if remote != nil && *remote != 0 && *remote != 1 {
		return badRequest("remote must be 0 or 1")
	}
	return nil
}

// SearchVersions answers the versions search: the distinct module versions
// of the files whose organisation and module match g and a, and whose
// version matches v, newest first in Maven order.
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
	scope, _ := r.repoScope(p.Repos, true)
	ls, err := r.layoutScope(ctx, scope, p.G, p.A)
	if err != nil {
		return nil, err
	}
	g, a, v := wildcard(p.G), wildcard(p.A), wildcard(p.V)
	found, err := ls.find(ctx, r, nil, r.capped(0), func(co coordinates) bool {
		return matches(g, co.org) && matches(a, co.module) && matches(v, co.version())
	})
	if err != nil {
		return nil, err
	}
	type version struct {
		v           string
		integration bool
	}
	var versions []version
	seen := map[string]bool{}
	for _, f := range found {
		if v := f.version(); !seen[v] {
			seen[v] = true
			versions = append(versions, version{v, f.integration()})
		}
	}
	slices.SortStableFunc(versions, func(x, y version) int { return types.CompareMaven(y.v, x.v) })
	entries := make([]map[string]any, 0, len(versions))
	for _, x := range versions {
		entries = append(entries, map[string]any{"version": x.v, "integration": x.integration})
	}
	return answer(ctx, mediaVersionsSearch, results(entries)), nil
}

// SearchLatestVersion answers the latest version search, in plain text: the
// highest release version of the module g and a name, in Maven order. A v
// that names an integration version under a file's layout, as 1.0-SNAPSHOT
// does under Maven's, asks for the highest integration file version of the
// matching module versions instead; any other v filters the releases.
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
	scope, _ := r.repoScope(p.Repos, true)
	ls, err := r.layoutScope(ctx, scope, p.G, p.A)
	if err != nil {
		return nil, err
	}
	g, a, v := wildcard(p.G), wildcard(p.A), wildcard(p.V)
	found, err := ls.find(ctx, r, nil, r.capped(0), func(co coordinates) bool {
		return matches(g, co.org) && matches(a, co.module) && matches(v, co.version())
	})
	if err != nil {
		return nil, err
	}
	integration := map[*layout.Layout]bool{}
	best := ""
	for _, f := range found {
		l := ls.layouts[f.Item.RepoKey]
		wantInteg, ok := integration[l]
		if !ok {
			wantInteg = integrationVersion(l, deref(p.V))
			integration[l] = wantInteg
		}
		if f.integration() != wantInteg {
			continue
		}
		cand := f.version()
		if wantInteg {
			cand = f.fileVersion()
		}
		if best == "" || types.CompareMaven(cand, best) > 0 {
			best = cand
		}
	}
	if best == "" {
		return nil, apierror.New(http.StatusNotFound, "Unable to find artifact versions")
	}
	return artgen.SearchLatestVersion200TextResponse(best), nil
}

// integrationVersion reports whether v names an integration version under
// l: after one of its hyphens comes text l's integration revision expression
// accepts in full, its folder's or, in a layout without one, its file's.
func integrationVersion(l *layout.Layout, v string) bool {
	def := l.Definition()
	expr := def.FolderIntegrationRevisionRegExp
	switch {
	case l.Has(layout.TokenFolderItegRev):
	case l.Has(layout.TokenFileItegRev):
		expr = def.FileIntegrationRevisionRegExp
	default:
		return false
	}
	// Compile substituted the same expression, so it compiles here too.
	re := regexp.MustCompile(`^(?:` + cmp.Or(expr, ".+") + `)$`)
	for i, ch := range v {
		if ch == '-' && re.MatchString(v[i+1:]) {
			return true
		}
	}
	return false
}
