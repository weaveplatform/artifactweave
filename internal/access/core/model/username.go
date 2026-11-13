package model

import (
	"github.com/weaveplatform/artifactweave/internal/access/api"
)

// FoldUsername is the spelling a user name is stored and matched in: lower
// case, the access api's rule (api.FoldUsername), which gives its sources.
//
// Every entry point folds a user name it is given before it reads or writes
// a store, and the stores compare names exactly, so they hold only
// lower-case names and any spelling of a name finds its user.
func FoldUsername(name string) string { return api.FoldUsername(name) }

// SameUsername reports whether a and b name the same user.
func SameUsername(a, b string) bool { return api.SameUsername(a, b) }

// FoldUsernames returns names folded, in order; nil stays nil.
func FoldUsernames(names []string) []string {
	if names == nil {
		return nil
	}
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = FoldUsername(n)
	}
	return out
}

// FoldUserGrants returns grants keyed by folded user names. Two spellings of
// one name in a document, which no 7.x source describes, hold the union of
// their actions, so the result does not depend on map order.
func FoldUserGrants(grants map[string]api.Actions) map[string]api.Actions {
	if grants == nil {
		return nil
	}
	out := make(map[string]api.Actions, len(grants))
	for name, a := range grants {
		out[FoldUsername(name)] |= a
	}
	return out
}

// FoldGrants returns r with its user grants folded (FoldUserGrants); group
// grants keep their case.
func (r Resource) FoldGrants() Resource {
	r.Actions.Users = FoldUserGrants(r.Actions.Users)
	return r
}

// FoldGrants returns p with every section's user grants folded, leaving p's
// own sections untouched.
func (p Permission) FoldGrants() Permission {
	if p.Resources == nil {
		return p
	}
	out := make(map[api.ResourceType]Resource, len(p.Resources))
	for rt, r := range p.Resources {
		out[rt] = r.FoldGrants()
	}
	p.Resources = out
	return p
}
