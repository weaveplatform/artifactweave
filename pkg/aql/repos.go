package aql

import (
	"cmp"
	"math"
	"slices"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// RepoClass is what a repository key stands for in a repo criterion.
type RepoClass int

// Repository classes. Every class that stores its own items (local,
// federated, release bundles) is RepoLocal.
const (
	RepoLocal RepoClass = iota
	// RepoRemote holds only its cache, reported as "<key>-cache".
	RepoRemote
	// RepoVirtual holds nothing itself and stands for its members.
	RepoVirtual
)

// Repository is one configured repository as a repo criterion resolves it.
type Repository struct {
	Class RepoClass
	// Members are a virtual repository's members, in order.
	Members []string
}

// Repositories are the configured repositories, by key.
type Repositories map[string]Repository

// RepoRef is one stored location of items: a repository key and whether it
// is that repository's remote cache, the items table's (repo_key, cached).
type RepoRef struct {
	Key    string
	Cached bool
}

// RepoSet holds when an item is stored in one of Refs, or, with Negate, in
// none of them. Resolve puts it in place of repo criteria; Parse never
// produces it.
type RepoSet struct {
	Refs   []RepoRef
	Negate bool
}

func (RepoSet) node() {}

func (s RepoSet) match(it types.Item) bool {
	in := slices.Contains(s.Refs, RepoRef{Key: it.RepoKey, Cached: it.Cached})
	return in != s.Negate
}

// Resolve rewrites q's repo criteria against the configured repositories
// (decision 0012, research 22 §1.4). The JFrog CLI downloads from a remote by
// naming the remote's own key, so a key is matched by what it stands for, not
// only by the key results report:
//
//   - a remote's key, and its "<key>-cache" alias, match the remote's cache;
//   - a virtual repository's key matches its members, recursively: local
//     members' items and remote members' caches;
//   - a local key matches its own items, even one that ends in "-cache";
//   - a key no repository has keeps the plain comparison.
//
// $eq and $ne match the expansion of their key and its complement. $match
// holds for an item whose reported key matches the pattern or that lies in
// the expansion of a configured key the pattern matches, and $nmatch holds
// when $match does not. Ordering operators compare the reported key as
// before. A query whose $eq names a virtual repository returns virtual_repos
// on every row, as Artifactory does.
//
// An items query parsed from AQL text also learns the repositories' roots
// (Roots).
func (q *Query) Resolve(repos Repositories) {
	if q.Criteria != nil {
		q.Criteria = q.resolve(q.Criteria, repos)
	}
	if !q.rooted {
		return
	}
	q.roots = nil
	for _, key := range sortedKeys(repos) {
		switch repos[key].Class {
		case RepoLocal:
			q.roots = append(q.roots, RepoRef{Key: key})
		case RepoRemote:
			q.roots = append(q.roots, RepoRef{Key: key, Cached: true})
		}
	}
}

// Roots returns the repository roots an items query's rows include beside
// the stored items, by key: every repository that stores items has a root,
// a remote one in its cache, and Artifactory's items domain lists each as a
// folder with path and name "." (#445). A virtual repository stores
// nothing and has none. A root with a record (the store's EnsureRoot) is a
// stored item already; one without is the row RootHit makes. Only Resolve
// sets them, and only for a query Parse read from AQL text: the queries
// the GET searches build in code list no root.
func (q *Query) Roots() []RepoRef { return q.roots }

// RootHit is the row of a repository root that has no record: a folder
// with path and name ".", ID 0, and no times, properties or statistics.
func RootHit(r RepoRef) Hit { return Hit{Item: types.RootItem(r.Key, r.Cached)} }

// MissingRoots returns the roots of q that recorded does not hold, as
// RootHit makes them, and that satisfy q's criteria at o's clock.
func (q *Query) MissingRoots(o Options, recorded func(RepoRef) bool) []Hit {
	var out []Hit
	now := o.now()
	for _, r := range q.roots {
		if recorded(r) {
			continue
		}
		if h := RootHit(r); q.Match(h, now) {
			out = append(out, h)
		}
	}
	return out
}

// WithRoots runs search for q and merges roots, rows search cannot return
// (MissingRoots), into its rows in q's order, then applies q's offset and
// limit to the whole, as Select does. search runs a query without offset
// whose limit covers the rows the window needs.
func (q *Query) WithRoots(
	o Options,
	roots []Hit,
	search func(*Query, Options) ([]Hit, error),
) ([]Hit, error) {
	if len(roots) == 0 {
		return search(q, o)
	}
	offset, limit := q.Window(o)
	wide := *q
	wide.Offset, wide.HasOffset = 0, false
	wide.Limit, wide.HasLimit = 0, false
	if limit >= 0 && limit <= math.MaxInt-offset {
		wide.Limit, wide.HasLimit = offset+limit, true
	}
	hits, err := search(&wide, Options{Now: o.Now})
	if err != nil {
		return nil, err
	}
	hits = append(hits, roots...)
	slices.SortStableFunc(hits, func(a, b Hit) int { return q.Compare(a.Item, b.Item) })
	return window(q, o, hits), nil
}

func (q *Query) resolve(n Node, repos Repositories) Node {
	switch n := n.(type) {
	case And:
		out := make(And, len(n))
		for i, c := range n {
			out[i] = q.resolve(c, repos)
		}
		return out
	case Or:
		out := make(Or, len(n))
		for i, c := range n {
			out[i] = q.resolve(c, repos)
		}
		return out
	case *Joined:
		criteria, _ := q.resolve(n.Criteria, repos).(And)
		return &Joined{Criteria: criteria}
	case *FieldCriterion:
		if n.Field == FieldRepo {
			return q.resolveRepo(n, repos)
		}
	}
	return n
}

func (q *Query) resolveRepo(c *FieldCriterion, repos Repositories) Node {
	switch c.Op {
	case OpEq, OpNe:
		if repos[c.Text].Class == RepoVirtual && c.Op == OpEq {
			q.impliedVirtualRepos = true
		}
		return RepoSet{Refs: expandRepo(c.Text, repos), Negate: c.Op == OpNe}
	case OpMatch, OpNMatch:
		var refs []RepoRef
		for _, key := range sortedKeys(repos) {
			if globMatch(c.Text, key) {
				refs = append(refs, expandRepo(key, repos)...)
			}
		}
		if len(refs) == 0 {
			return c
		}
		set := RepoSet{Refs: uniqueRefs(refs)}
		if c.Op == OpMatch {
			return Or{c, set}
		}
		set.Negate = true
		return And{c, set}
	}
	return c
}

// Expand returns the stored locations key stands for, as a repo criterion's
// $eq resolves it: a remote's cache, a virtual repository's members through
// nested virtual repositories, or the key's own items.
func (r Repositories) Expand(key string) []RepoRef { return expandRepo(key, r) }

// expandRepo returns the stored locations key stands for.
func expandRepo(key string, repos Repositories) []RepoRef {
	var out []RepoRef
	var walk func(key string, seen map[string]bool)
	walk = func(key string, seen map[string]bool) {
		r, ok := repos[key]
		switch {
		case !ok:
			stored, cached := types.ResolveRepoKey(key)
			out = append(out, RepoRef{Key: stored, Cached: cached})
		case r.Class == RepoRemote:
			// Only the cache holds a remote's items; the uncached form is
			// listed too so an item stored there by other means still answers
			// to the remote's key.
			out = append(out, RepoRef{Key: key}, RepoRef{Key: key, Cached: true})
		case r.Class == RepoVirtual:
			if seen[key] {
				return
			}
			seen[key] = true
			for _, m := range r.Members {
				walk(m, seen)
			}
		default:
			out = append(out, RepoRef{Key: key})
		}
	}
	walk(key, map[string]bool{})
	return uniqueRefs(out)
}

func uniqueRefs(refs []RepoRef) []RepoRef {
	slices.SortFunc(refs, func(a, b RepoRef) int {
		if c := cmp.Compare(a.Key, b.Key); c != 0 {
			return c
		}
		switch {
		case a.Cached == b.Cached:
			return 0
		case a.Cached:
			return 1
		}
		return -1
	})
	return slices.Compact(refs)
}

func sortedKeys(repos Repositories) []string {
	keys := make([]string, 0, len(repos))
	for k := range repos {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
