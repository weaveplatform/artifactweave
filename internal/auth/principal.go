package auth

import (
	"context"
	"fmt"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// SubjectOf returns p as the permission evaluator starts from it: the
// user's own admin flag and groups, and the token's scope when a token
// authenticated the request. It is a function rather than a method so that
// Principal itself does not depend on the permission evaluator.
func SubjectOf(p Principal) authz.Subject {
	s := authz.Subject{
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
			if e.Kind == types.ScopeGroups {
				s.Groups = append(s.Groups, e.Names...)
			}
		}
		slices.Sort(s.Groups)
		s.Groups = slices.Compact(s.Groups)
	}
	return s
}

// Authorize evaluates p's permissions once for the request (decision 0006).
// It returns p with Admin and Groups set to what the evaluator decides (a
// token scope can remove the admin flag and narrow the groups, and an
// admin-privilege group makes a member an administrator) and the evaluator
// itself, which answers every permission question asked for p. A request
// stores the two with WithAuthorized, and its handlers reach the decider
// through DeciderFrom. On error it returns the zero Principal and a nil
// decider.
func Authorize(
	ctx context.Context,
	src authz.Sources,
	p Principal,
) (Principal, *authz.Decider, error) {
	d, err := authz.New(ctx, src, SubjectOf(p))
	if err != nil {
		return Principal{}, nil, fmt.Errorf("auth: permissions: %w", err)
	}
	p.Admin, p.Groups = d.IsAdmin(), d.Groups()
	return p, d, nil
}

type deciderKey struct{}

// boundDecider ties a decider to the WithPrincipal call that stored its
// principal, which kernel.PrincipalBinding identifies. It is kept apart from
// the principal so that whatever stores a decider need not be the code that
// stores the principal; the binding alone keeps a decider from answering for
// a principal stored by another call.
type boundDecider struct {
	binding any
	d       *authz.Decider
}

// WithAuthorized returns ctx carrying p and, for DeciderFrom, d, the
// decider Authorize returned with p. d is bound to this call: a context
// derived with a later WithPrincipal or WithAuthorized no longer sees it.
func WithAuthorized(ctx context.Context, p Principal, d *authz.Decider) context.Context {
	ctx = WithPrincipal(ctx, p)
	b := boundDecider{binding: kernel.PrincipalBinding(ctx), d: d}
	return context.WithValue(ctx, deciderKey{}, b)
}

// DeciderFrom returns the permission decider that WithAuthorized stored
// with ctx's principal, which handlers ask every permission question
// (decision 0006). It is nil when ctx carries no principal or WithPrincipal
// stored it. A context derived from ctx other than by WithPrincipal or
// WithAuthorized, such as one with a deadline, sees the same decider; one
// derived by either sees only what that call stored. The decider is bound
// to the call that stored the principal, not to the principal's value, so a
// decider can never answer for a principal another call stored.
func DeciderFrom(ctx context.Context) *authz.Decider {
	b, ok := ctx.Value(deciderKey{}).(boundDecider)
	if !ok || b.binding == nil || b.binding != kernel.PrincipalBinding(ctx) {
		return nil
	}
	return b.d
}
