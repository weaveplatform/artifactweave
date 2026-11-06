package accessapitest

import (
	"context"
	"fmt"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/kernel"
)

var _ accessapi.Authorizer = (*Authorizer)(nil)

// Authorizer is an Authorizer whose evaluations DecideFunc answers: Authorize
// and For evaluate the subject that accessapi.SubjectOf builds from the
// principal, and Decide the subject it is given. The contract's error wraps
// apply, so a test reads the texts the access module gives. It holds no
// state, so it may be used from several goroutines as long as DecideFunc
// may.
type Authorizer struct {
	// DecideFunc evaluates s, with the anonymous user's grants applying only
	// while anonymousEnabled holds, and returns a Decider or an error. Nil
	// answers every subject with an empty *Decider, which holds nothing.
	DecideFunc func(
		ctx context.Context,
		s accessapi.Subject,
		anonymousEnabled bool,
	) (accessapi.Decider, error)
	// AnonymousEnabled is the anonymous access setting that For reads.
	AnonymousEnabled bool
}

// Authorize evaluates p and returns it with Admin and Groups set from the
// Decider, with the Decider, as the access module does. An evaluation error
// is wrapped as "auth: permissions: %w" and returned with the zero principal
// and a nil Decider.
func (a *Authorizer) Authorize(
	ctx context.Context,
	p kernel.Principal,
	anonymousEnabled bool,
) (kernel.Principal, accessapi.Decider, error) {
	d, err := a.decide(ctx, accessapi.SubjectOf(p), anonymousEnabled)
	if err != nil {
		return kernel.Principal{}, nil, fmt.Errorf("auth: permissions: %w", err)
	}
	p.Admin, p.Groups = d.IsAdmin(), d.Groups()
	return p, d, nil
}

// For evaluates p with AnonymousEnabled as the anonymous access setting. An
// evaluation error is wrapped as "evaluate permissions: %w" and returned with
// a nil Decider.
func (a *Authorizer) For(ctx context.Context, p kernel.Principal) (accessapi.Decider, error) {
	d, err := a.decide(ctx, accessapi.SubjectOf(p), a.AnonymousEnabled)
	if err != nil {
		return nil, fmt.Errorf("evaluate permissions: %w", err)
	}
	return d, nil
}

// Decide evaluates s. An evaluation error is returned unwrapped, for the
// caller to wrap, with a nil Decider.
func (a *Authorizer) Decide(
	ctx context.Context,
	s accessapi.Subject,
	anonymousEnabled bool,
) (accessapi.Decider, error) {
	return a.decide(ctx, s, anonymousEnabled)
}

// decide asks DecideFunc and drops the Decider that came with an error, so
// every method keeps the contract's nil Decider on error.
func (a *Authorizer) decide(
	ctx context.Context,
	s accessapi.Subject,
	anonymousEnabled bool,
) (accessapi.Decider, error) {
	if a.DecideFunc == nil {
		return &Decider{}, nil
	}
	d, err := a.DecideFunc(ctx, s, anonymousEnabled)
	if err != nil {
		return nil, err
	}
	return d, nil
}
