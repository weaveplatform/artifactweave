package authn

import (
	"context"
	"fmt"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/authz"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Authorize evaluates p's permissions once for the request (decision 0006).
// It returns p with Admin and Groups set to what the evaluator decides (a
// token scope can remove the admin flag and narrow the groups, and an
// admin-privilege group makes a member an administrator) and the evaluator
// itself, which answers every permission question asked for p. A request
// stores the two with WithAuthorized, and its handlers reach the decider
// through DeciderFrom. On error it returns the zero kernel.Principal and a
// nil decider.
func Authorize(
	ctx context.Context,
	src authz.Sources,
	p kernel.Principal,
) (kernel.Principal, *authz.Decider, error) {
	d, err := authz.New(ctx, src, accessapi.SubjectOf(p))
	if err != nil {
		return kernel.Principal{}, nil, fmt.Errorf("auth: permissions: %w", err)
	}
	p.Admin, p.Groups = d.IsAdmin(), d.Groups()
	return p, d, nil
}

// WithAuthorized returns ctx carrying p and, for DeciderFrom, d, the
// decider Authorize returned with p. d is bound to this call: a context
// derived with a later kernel.WithPrincipal or WithAuthorized no longer
// sees it.
// It stores them with accessapi.WithCaller, so one context key serves both
// packages and accessapi.DeciderFrom sees d too; AsAPI keeps a nil d from
// becoming a typed nil there.
func WithAuthorized(ctx context.Context, p kernel.Principal, d *authz.Decider) context.Context {
	return accessapi.WithCaller(ctx, p, authz.AsAPI(d))
}

// DeciderFrom returns the permission decider stored with ctx's principal,
// by WithAuthorized or accessapi.WithCaller, which handlers ask every
// permission question (decision 0006). It is nil when ctx carries no
// principal or kernel.WithPrincipal stored it, and when the stored decider
// is not the evaluator, such as a test's fake, so a caller evaluates the
// principal afresh. A context derived from ctx other than by
// kernel.WithPrincipal or WithAuthorized, such as one with a deadline, sees
// the same decider; one derived by either sees only what that call stored.
// The decider is bound to the call that stored the principal, not to the
// principal's value, so a decider can never answer for a principal another
// call stored.
func DeciderFrom(ctx context.Context) *authz.Decider {
	d, _ := accessapi.DeciderFrom(ctx)
	evaluator, _ := authz.FromAPI(d)
	return evaluator
}
