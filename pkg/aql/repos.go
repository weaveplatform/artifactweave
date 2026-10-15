package aql

import (
	"cmp"
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
func (q *Query) Resolve(repos Repositories) {
	if q.Criteria != nil {
		q.Criteria = q.resolve(q.Criteria, repos)
	}
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
