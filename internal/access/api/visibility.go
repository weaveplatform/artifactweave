package api

import "slices"

// RepoSet is a set of stored repositories named the way permission targets
// and token scopes name them: every repository, or those of the listed
// classes together with those of the listed keys. The zero RepoSet holds
// none.
type RepoSet struct {
	Every   bool
	Classes []string
	Keys    []string
}

// Has reports whether the set holds r.
func (s RepoSet) Has(r Repo) bool {
	return s.Every || slices.Contains(s.Classes, r.Class) || slices.Contains(s.Keys, r.Key)
}

// RepoVisibility is the set of repositories a subject sees in a listing, as
// the intersection of its sets: a repository is visible when every set holds
// it, so a RepoVisibility with no set shows every repository. It describes
// what Decider.IsRepoVisible answers in terms a store can apply as a filter,
// so a listing can count and page what the caller sees in its query rather
// than reading every repository and asking about each (decision 0043,
// "Paging classes and the next pointer").
type RepoVisibility []RepoSet

// Has reports whether every set holds r.
func (v RepoVisibility) Has(r Repo) bool {
	for _, s := range v {
		if !s.Has(r) {
			return false
		}
	}
	return true
}
