package search

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/layout"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// SearchPage is how many rows one store query reads. Permission filtering
// runs on each page, so a search keeps reading until it holds its limit of
// readable results or the rows run out.
const SearchPage = 1000

// Store is what a search reads: items through AQL and the repositories it
// scopes by.
type Store interface {
	// Items is the item store, which runs a search's AQL queries.
	Items() port.ItemStore
	// Repos is the repository store, which lists the repositories a search
	// scopes by.
	Repos() port.RepoStore
}

// Access is the caller's view of repositories, computed by the adapter from
// the request's permissions.
type Access interface {
	// Visible reports whether the caller may see c, which decides whether a
	// key in a search's repos parameter names a repository for them.
	Visible(c repo.Config) bool
	// Readable keeps the hits the caller may read, by repository and path,
	// that their repository's include and exclude patterns accept. configs
	// are the repositories by key. Its error is a failure to read the
	// caller's permissions.
	Readable(ctx context.Context, configs map[string]repo.Config, hits []aql.Hit) ([]aql.Hit, error)
}

// Limits bound the GET searches. UserQueryLimit caps the results anonymous
// callers get, and every non-admin's when LimitInternalUsers is set, as
// Artifactory's artifactory.search.userQueryLimit and
// limitAnonymousUsersOnly do; MaxResults is the AQL hard cap, which bounds
// every search. Zero means no cap.
type Limits struct {
	UserQueryLimit, MaxResults int
	LimitInternalUsers         bool
}

// SearchRun is one GET search: the caller, the repositories as they stand,
// as configured and as repo criteria resolve, and when the search started.
// Build one with NewSearch.
type SearchRun struct {
	// Principal is the caller.
	Principal kernel.Principal
	// Configs are the repositories' configurations, by key.
	Configs map[string]repo.Config
	// Repos are the repositories as a repo criterion resolves them.
	Repos aql.Repositories
	// Now is when the search started, in UTC: the end of a time window the
	// caller leaves open, and AQL's present.
	Now time.Time
	// Internal answers a failure the caller cannot act on, labelled with
	// the operation that failed: "search", "search permissions" or "search
	// layout". The adapter logs it and answers 500; nil returns the error
	// as it is.
	Internal func(ctx context.Context, op string, err error) error

	st      Store
	limits  Limits
	access  Access
	layouts func(context.Context, string) (*layout.Layout, error)
}

// NewSearch starts a search for p over st, under l, with a's view of the
// repositories. layouts reads a repository layout by name, as the
// descriptor or the built-in set defines it. NewSearch lists the
// repositories; its only error is that listing's.
func NewSearch(
	ctx context.Context,
	st Store,
	l Limits,
	p kernel.Principal,
	a Access,
	layouts func(context.Context, string) (*layout.Layout, error),
) (*SearchRun, error) {
	configs, err := RepoConfigs(ctx, st)
	if err != nil {
		return nil, err
	}
	return &SearchRun{
		Principal: p, Configs: configs, Repos: aql.AQLRepositories(configs),
		Now: time.Now().UTC(), st: st, limits: l, access: a, layouts: layouts,
	}, nil
}

// internal passes err to Internal with its label, or returns it as it is
// when Internal is nil.
func (r *SearchRun) internal(ctx context.Context, op string, err error) error {
	if r.Internal == nil {
		return err
	}
	return r.Internal(ctx, op, err)
}

// RepoConfigs returns every repository's configuration, by key. Its error
// is the store's listing error under "repositories: ".
func RepoConfigs(ctx context.Context, st Store) (map[string]repo.Config, error) {
	configs, err := st.Repos().List(ctx, port.RepoFilter{})
	if err != nil {
		return nil, fmt.Errorf("repositories: %w", err)
	}
	byKey := make(map[string]repo.Config, len(configs))
	for _, c := range configs {
		byKey[c.Key] = c
	}
	return byKey, nil
}

// UserLimit is the most results the caller may get from a search the user
// query limit applies to, under the AQL hard cap; -1 is no limit.
func (r *SearchRun) UserLimit() int {
	l := r.limits
	if r.Principal.Anonymous || (!r.Principal.Admin && l.LimitInternalUsers) {
		return r.Capped(l.UserQueryLimit)
	}
	return r.Capped(0)
}

// Capped is limit, zero for none, under the AQL hard cap; -1 is no limit.
func (r *SearchRun) Capped(limit int) int {
	out := -1
	for _, n := range []int{limit, r.limits.MaxResults} {
		if n > 0 && (out < 0 || n < out) {
			out = n
		}
	}
	return out
}

// Collect runs criteria a page at a time, in item order, and returns the
// hits the caller may read that keep accepts (nil accepts all), at most want
// of them (-1 for all). Administrators read everything, as in AQL; anyone
// else gets the rows Access.Readable keeps, so a page may yield fewer.
func (r *SearchRun) Collect(
	ctx context.Context,
	criteria aql.Node,
	include []string,
	want int,
	keep func(aql.Hit) bool,
) ([]aql.Hit, error) {
	out := []aql.Hit{}
	for offset := 0; ; offset += SearchPage {
		q := &aql.Query{
			Criteria: criteria, Include: include,
			Offset: offset, HasOffset: true, Limit: SearchPage, HasLimit: true,
		}
		q.Resolve(r.Repos)
		hits, err := r.st.Items().Search(ctx, q, aql.Options{Now: r.Now})
		if err != nil {
			return nil, r.internal(ctx, "search", err)
		}
		read := len(hits)
		if !r.Principal.Admin {
			if hits, err = r.access.Readable(ctx, r.Configs, hits); err != nil {
				return nil, r.internal(ctx, "search permissions", err)
			}
		}
		for _, h := range hits {
			if keep != nil && !keep(h) {
				continue
			}
			out = append(out, h)
			if len(out) == want {
				return out, nil
			}
		}
		if read < SearchPage {
			return out, nil
		}
	}
}

// RepoScope turns a repos parameter into a criterion on the items'
// repositories, nil for every repository, which Collect resolves as AQL
// resolves repo criteria: a remote's key, like its cache key, selects its
// cache, and a virtual key its members, through nested virtual
// repositories. A virtual key counts only when virtual is set. Keys that
// name no repository select nothing; missing lists them, with the keys of
// repositories the caller may not see, for the search that refuses them. A
// parameter whose every key selects nothing matches no item.
func (r *SearchRun) RepoScope(repos *string, virtual bool) (scope aql.Node, missing []string) {
	if repos == nil || strings.TrimSpace(*repos) == "" {
		return nil, nil
	}
	var keys []string
	for _, key := range strings.Split(*repos, ",") {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		// A configured key wins over reading "-cache" as a remote's cache,
		// as AQL resolves it.
		c, ok := r.Configs[key]
		if !ok {
			stored, cached := model.ResolveRepoKey(key)
			c, ok = r.Configs[stored]
			ok = ok && cached && c.Class == repo.Remote
		}
		switch {
		case !ok:
			missing = append(missing, key)
			continue
		case c.Class == repo.Virtual && !virtual:
			continue
		// Permissions name no virtual repository, so a virtual key is
		// not checked here: Access.Readable drops what the caller may not read.
		case c.Class != repo.Virtual && !r.access.Visible(c):
			missing = append(missing, key)
			continue
		}
		if !slices.Contains(keys, c.Key) {
			keys = append(keys, c.Key)
		}
	}
	either := make(aql.Or, 0, len(keys))
	for _, key := range keys {
		either = append(either, &aql.FieldCriterion{Field: aql.FieldRepo, Op: aql.OpEq, Text: key})
	}
	return either, missing
}

// RefsOf is the stored locations the repository keys stand for, as a repo
// criterion's $eq resolves them; nil for no keys.
func (r *SearchRun) RefsOf(keys []string) map[aql.RepoRef]bool {
	var out map[aql.RepoRef]bool
	for _, key := range keys {
		if key = strings.TrimSpace(key); key == "" {
			continue
		}
		if out == nil {
			out = map[aql.RepoRef]bool{}
		}
		for _, ref := range r.Repos.Expand(key) {
			out[ref] = true
		}
	}
	return out
}

// Files is the criterion that leaves folders out: a hand-built query has no
// implicit type, unlike a parsed one.
func Files() aql.Node {
	return &aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"}
}

// All is the conjunction of the criteria given, skipping nil ones.
func All(nodes ...aql.Node) aql.And {
	out := make(aql.And, 0, len(nodes))
	for _, n := range nodes {
		if n != nil {
			out = append(out, n)
		}
	}
	return out
}

// Within is the criterion that a time field lies in [from, to].
func Within(f aql.Field, from, to time.Time) aql.Node {
	return aql.And{
		&aql.FieldCriterion{Field: f, Op: aql.OpGte, Time: from},
		&aql.FieldCriterion{Field: f, Op: aql.OpLte, Time: to},
	}
}

// deref is the parameter p points at, empty when it is missing.
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
