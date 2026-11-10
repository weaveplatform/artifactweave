package search

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/layout"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// LayoutScope is what a layout search reads: the layout of each repository
// it may find files in, by stored key, and the criteria that narrow to them.
type LayoutScope struct {
	// Layouts are the layouts by stored repository key. A file whose
	// repository has none is never a hit.
	Layouts map[string]*layout.Layout
	// Criteria select the files in scope under the start of each layout.
	Criteria aql.Node
}

// LayoutHit is a file a layout search found, with its coordinates.
type LayoutHit struct {
	aql.Hit
	layout.Coordinates
}

// LayoutScope resolves the repositories scope covers (nil for every one) to
// their layouts, keeping those that read an organisation, a module and a
// base revision, and narrows the query to the files under the start each
// layout gives the organisation g and the module a. A layout that does not
// compile reads nothing.
func (r *SearchRun) LayoutScope(
	ctx context.Context,
	scope aql.Node,
	g, a *string,
) (LayoutScope, error) {
	var keys []string
	if scope == nil {
		for k, c := range r.Configs {
			if c.Class != repo.Virtual {
				keys = append(keys, k)
			}
		}
	} else {
		for _, k := range scopeKeys(scope) {
			for _, ref := range r.Repos.Expand(k) {
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
	byName := map[string]*layout.Layout{}
	out := LayoutScope{Layouts: map[string]*layout.Layout{}}
	var prefixes []string
	for _, k := range keys {
		c, ok := r.Configs[k]
		if !ok {
			continue
		}
		l, seen := byName[c.RepoLayoutRef]
		if !seen {
			var err error
			l, err = r.layouts(ctx, c.RepoLayoutRef)
			switch {
			case errors.Is(err, layout.ErrPattern):
				l = nil
			case err != nil:
				return out, r.internal(ctx, "search layout", err)
			case !l.Has(layout.TokenOrg) || !l.Has(layout.TokenModule) ||
				!l.Has(layout.TokenBaseRev):
				l = nil
			default:
				prefixes = append(prefixes, l.Prefixes(values)...)
			}
			byName[c.RepoLayoutRef] = l
		}
		if l != nil {
			out.Layouts[k] = l
		}
	}
	out.Criteria = All(Files(), scope, under(prefixes...))
	return out, nil
}

// scopeKeys lists the repository keys of a RepoScope criterion.
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
func (ls LayoutScope) read(it types.Item) (layout.Coordinates, bool) {
	l := ls.Layouts[it.RepoKey]
	if l == nil {
		return layout.Coordinates{}, false
	}
	return l.Read(it.FullPath())
}

// Find runs a layout search: the files in scope whose coordinates keep
// accepts, at most want of them (-1 for all).
func (ls LayoutScope) Find(
	ctx context.Context,
	r *SearchRun,
	include []string,
	want int,
	keep func(layout.Coordinates) bool,
) ([]LayoutHit, error) {
	if len(ls.Layouts) == 0 {
		return nil, nil
	}
	hits, err := r.Collect(ctx, ls.Criteria, include, want, func(h aql.Hit) bool {
		c, ok := ls.read(h.Item)
		return ok && keep(c)
	})
	if err != nil {
		return nil, err
	}
	out := make([]LayoutHit, len(hits))
	for n, h := range hits {
		c, _ := ls.read(h.Item)
		out[n] = LayoutHit{h, c}
	}
	return out, nil
}

// Wildcard compiles a search parameter in which * is any text and ? one
// character; nil, for a parameter not given or empty, matches anything.
func Wildcard(p *string) *regexp.Regexp {
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

// Matches reports whether s matches re; a nil re, Wildcard's for a missing
// parameter, matches anything.
func Matches(re *regexp.Regexp, s string) bool { return re == nil || re.MatchString(s) }

// NamedKeys maps each stored location a RepoScope criterion reaches to the
// first key of the criterion that reaches it.
func (r *SearchRun) NamedKeys(scope aql.Node) map[aql.RepoRef]string {
	out := map[aql.RepoRef]string{}
	for _, k := range scopeKeys(scope) {
		for _, ref := range r.Repos.Expand(k) {
			if _, ok := out[ref]; !ok {
				out[ref] = k
			}
		}
	}
	return out
}
