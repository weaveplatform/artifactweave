package api

import (
	"context"
	"slices"

	"github.com/weaveplatform/artifactweave/migration/kernel"
)

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

// Authorizer evaluates a principal's permissions (decision 0006), once for a
// request in the middleware and again for a principal the middleware did not
// see, such as the caller of queued work. Each call reads the permissions
// and groups through ctx, so a call inside a store transaction takes part in
// it. On error each call returns a nil Decider.
type Authorizer interface {
	// Authorize evaluates p's permissions, with the anonymous user's grants
	// applying only while anonymousEnabled holds. It returns p with Admin
	// and Groups set to what the evaluation decided (a token scope can
	// remove the admin flag and narrow the groups, and a group with admin
	// privileges makes a member an administrator) and the Decider that
	// answers every permission question for it; the two belong together in
	// the request's context (WithCaller). A store error is wrapped as "auth:
	// permissions: %w", and the principal returned with it is the zero
	// value.
	Authorize(
		ctx context.Context,
		p kernel.Principal,
		anonymousEnabled bool,
	) (kernel.Principal, Decider, error)
	// For evaluates p's permissions for a caller that holds no decider for
	// it, reading the anonymous access setting first; a setting that was
	// never written counts as off. It wraps an error reading the setting as
	// "anonymous access setting: %w" and an error evaluating as "evaluate
	// permissions: %w".
	For(ctx context.Context, p kernel.Principal) (Decider, error)
	// Decide evaluates s, a subject that no request authenticated, such as
	// the user a signed upload names, with the anonymous user's grants
	// applying only while anonymousEnabled holds. Its error is the
	// evaluator's, unwrapped, for the caller to wrap.
	Decide(ctx context.Context, s Subject, anonymousEnabled bool) (Decider, error)
}

// SubjectOf returns p as the permission evaluator starts from it: the user's
// own admin flag and groups, and the token's scope when a token
// authenticated p. It is here rather than in access core because other
// modules build the subject of a remote read from the request's principal
// and may import only this package. The groups never alias p's.
func SubjectOf(p kernel.Principal) Subject {
	s := Subject{
		Name: p.Name, Groups: slices.Clone(p.Groups), Admin: p.Admin, Anonymous: p.Anonymous,
	}
	if p.IsTokenAuthenticated {
		sc := p.Scope
		s.Scope = &sc
		// A groups scope acts through the groups it names whether or not
		// the user belongs to them (#79 decides who may mint such a token);
		// the evaluator keeps only named groups that the subject has, so
		// they are added here.
		for _, e := range sc.Entries {
			if e.Kind == kernel.ScopeGroups {
				s.Groups = append(s.Groups, e.Names...)
			}
		}
		slices.Sort(s.Groups)
		s.Groups = slices.Compact(s.Groups)
	}
	return s
}
