package access

import (
	"context"
	"errors"
	"fmt"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/core/authn"
	"github.com/weaveplatform/artifactweave/migration/access/core/authz"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var _ accessapi.Authorizer = authorizer{}

// authorizer evaluates permissions over the store's permissions and groups.
// Every Decider it returns goes through authz.AsAPI, so a nil evaluator is a
// nil interface, never a typed nil.
type authorizer struct{ st Store }

// sources is what an evaluation reads, read through the caller's context.
func (b authorizer) sources(anonymousEnabled bool) authz.Sources {
	return authz.Sources{
		Permissions: b.st.Permissions(), Groups: b.st.Groups(), AnonymousEnabled: anonymousEnabled,
	}
}

func (b authorizer) Authorize(
	ctx context.Context,
	p kernel.Principal,
	anonymousEnabled bool,
) (kernel.Principal, accessapi.Decider, error) {
	p, d, err := authn.Authorize(ctx, b.sources(anonymousEnabled), p)
	//nolint:wrapcheck // authn.Authorize wraps its own as "auth: permissions: %w"
	return p, authz.AsAPI(d), err
}

// For is the evaluation that a handler without the request's decider made
// itself (internal/server's accessFor), with its error texts.
func (b authorizer) For(ctx context.Context, p kernel.Principal) (accessapi.Decider, error) {
	var on bool
	err := b.st.Settings().Get(ctx, types.SettingAnonymousAccess, &on)
	if err != nil && !errors.Is(err, kernel.ErrNotFound) {
		return nil, fmt.Errorf("anonymous access setting: %w", err)
	}
	d, err := authz.New(ctx, b.sources(on), accessapi.SubjectOf(p))
	if err != nil {
		return nil, fmt.Errorf("evaluate permissions: %w", err)
	}
	return authz.AsAPI(d), nil
}

func (b authorizer) Decide(
	ctx context.Context,
	s accessapi.Subject,
	anonymousEnabled bool,
) (accessapi.Decider, error) {
	d, err := authz.New(ctx, b.sources(anonymousEnabled), s)
	if err != nil {
		return nil, err //nolint:wrapcheck // callers wrap, each with its own text
	}
	return authz.AsAPI(d), nil
}
