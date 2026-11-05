package api

import "github.com/weaveplatform/artifactweave/migration/kernel"

// Subject is who a request acts as. Groups are the groups the subject
// belongs to; the token service (#74) may instead put a token's scoped
// groups here when it mints a token for a subject that is not a user.
type Subject struct {
	Name      string
	Groups    []string
	Admin     bool
	Anonymous bool
	// Scope is the scope of the token the request carries; nil for password
	// authentication, which acts with everything the subject holds.
	Scope *kernel.Scope
}

// Repo identifies a permission target: a repository's key and class (local,
// remote, virtual, federated, distribution), or the fixed build-info or
// release-bundle repository of those resources. "<remote>-cache" is
// accepted for a remote's cache and resolves to the remote.
type Repo struct {
	Key   string
	Class string
}

// Decider answers the permission questions other modules ask about one
// request's subject, from the snapshot of the permissions taken when it was
// built (decision 0006). The access module's evaluator implements it, and an
// implementation answers exactly as that evaluator does: a token scope only
// removes actions, except that applied-permissions/admin acts as an
// administrator. The methods only read that snapshot, so a Decider may be
// used from several goroutines. It holds only what other modules ask; the
// questions about permission documents and the subject's own grants stay on
// the evaluator, because only access's permission handlers ask them.
type Decider interface {
	// IsAdmin reports whether the subject acts as an administrator, which
	// the administrator-only endpoints require.
	IsAdmin() bool
	// Groups returns the groups the subject acts through: all of its groups,
	// or those a token scope names. The slice is the caller's to change.
	Groups() []string
	// IsRepoVisible reports whether the subject may see repo in a listing
	// and read its configuration.
	IsRepoVisible(repo Repo) bool
	// Allows reports whether the subject holds every action in want on the
	// file at path in repo, in the rt section of the permissions. Asking for
	// no action is refused.
	Allows(rt ResourceType, repo Repo, path string, want Actions) bool
	// AllowsFolder is Allows for the folder at path, which also counts as
	// permitted when a permitted path lies below it.
	AllowsFolder(rt ResourceType, repo Repo, path string, want Actions) bool
	// System reports whether the subject may use a system capability, an
	// area such as "info" or "metrics", with the actions in want: an
	// administrator may, anyone else only through a token's system scope
	// that covers the area and the actions.
	System(area string, want kernel.ScopeActions) bool
}
