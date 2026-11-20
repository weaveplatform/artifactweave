package access

import (
	"context"
	"errors"
	"fmt"
	"slices"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/access/core/authz"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

var _ accessapi.Authorizer = authorizer{}

// authorizer evaluates permissions over the store's permissions and groups.
// Every Decider it returns goes through authz.AsAPI, so a nil evaluator is a
// nil interface, never a typed nil.
type authorizer struct {
	st           Store
	repositories artifactsapi.ProjectRepositoryReader
}

// sources is what an evaluation reads, read through the caller's context.
func (b authorizer) sources(anonymousEnabled bool) authz.Sources {
	src := authz.Sources{
		Permissions: b.st.Permissions(), Groups: b.st.Groups(), Users: b.st.Users(), AnonymousEnabled: anonymousEnabled,
	}
	if b.repositories != nil {
		src.Projects, src.Roles, src.Repositories = b.st.Projects(), project.Service{Store: b.st}, b.repositories
	}
	return src
}

// snapshot also protects ordinary request evaluation from mixing a new role
// definition with an old group or ownership read. Publication owners enter this
// same guard before their own locks and retain the enclosing transaction.
func (b authorizer) snapshot(ctx context.Context, fn func(context.Context) error) error {
	return b.st.Tx(ctx, func(ctx context.Context) error { //nolint:wrapcheck // preserve each public method's error envelope
		if err := b.st.Projects().Guard(ctx); err != nil {
			return fmt.Errorf("guard permission snapshot: %w", err)
		}
		return fn(ctx)
	})
}

// identity refreshes only verified principals. Decide's unsigned subjects and
// hand-built embedding principals remain their owner's responsibility. This
// refresh deliberately does not recheck password expiry: middleware admits an
// expired password only to its change-password route, which must remain usable.
func (b authorizer) identity(ctx context.Context, p kernel.Principal) (kernel.Principal, error) {
	if p.Anonymous || !p.Password && !p.IsTokenAuthenticated && p.Registry == nil {
		return p, nil
	}
	if p.Identity == kernel.IdentitySynthetic {
		p.Admin, p.Groups = false, nil
		return p, nil
	}
	u, err := b.st.Users().Get(ctx, model.FoldUsername(p.Name))
	if errors.Is(err, kernel.ErrNotFound) && p.Identity == kernel.IdentityTransient {
		groups, err := b.st.Groups().AutoJoin(ctx)
		if err != nil {
			return kernel.Principal{}, fmt.Errorf("current transient groups: %w", err)
		}
		p.Admin, p.Groups = false, slices.Clone(groups)
		return p, nil
	}
	if errors.Is(err, kernel.ErrNotFound) {
		return kernel.Principal{}, kernel.ErrBadCredentials
	}
	if err != nil {
		return kernel.Principal{}, fmt.Errorf("current permission identity: %w", err)
	}
	if u.Status != "" && u.Status != model.UserEnabled {
		return kernel.Principal{}, kernel.ErrBadCredentials
	}
	p.Admin, p.Groups = u.Admin, slices.Clone(u.Groups)
	return p, nil
}

func (b authorizer) Authorize(
	ctx context.Context,
	p kernel.Principal,
	anonymousEnabled bool,
) (kernel.Principal, accessapi.Decider, error) {
	var d *authz.Decider
	err := b.snapshot(ctx, func(ctx context.Context) error {
		var err error
		p, err = b.identity(ctx, p)
		if err != nil {
			return fmt.Errorf("auth: permissions: %w", err)
		}
		p, d, err = authn.Authorize(ctx, b.sources(anonymousEnabled), p)
		return err //nolint:wrapcheck // authn supplies the public auth: permissions envelope
	})
	if err != nil {
		return kernel.Principal{}, nil, err //nolint:wrapcheck // preserve the existing authorization error envelope
	}
	return p, authz.AsAPI(d), nil
}

// For is the evaluation that a handler without the request's decider made
// itself (the item kit's AccessFor), with its error texts.
func (b authorizer) For(ctx context.Context, p kernel.Principal) (accessapi.Decider, error) {
	var d *authz.Decider
	err := b.snapshot(ctx, func(ctx context.Context) error {
		var on bool
		err := b.st.Settings().Get(ctx, settings.SettingAnonymousAccess, &on)
		if err != nil && !errors.Is(err, kernel.ErrNotFound) {
			return fmt.Errorf("anonymous access setting: %w", err)
		}
		p, err = b.identity(ctx, p)
		if err != nil {
			return fmt.Errorf("evaluate permissions: %w", err)
		}
		d, err = authz.New(ctx, b.sources(on), accessapi.SubjectOf(p))
		if err != nil {
			return fmt.Errorf("evaluate permissions: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err //nolint:wrapcheck // the snapshot preserves the binding's existing envelopes
	}
	return authz.AsAPI(d), nil
}

func (b authorizer) Decide(
	ctx context.Context,
	s accessapi.Subject,
	anonymousEnabled bool,
) (accessapi.Decider, error) {
	var d *authz.Decider
	err := b.snapshot(ctx, func(ctx context.Context) error {
		var err error
		d, err = authz.New(ctx, b.sources(anonymousEnabled), s)
		return err //nolint:wrapcheck // Decide publishes the evaluator's error for its caller to wrap
	})
	if err != nil {
		return nil, err //nolint:wrapcheck // callers wrap, each with its own text
	}
	return authz.AsAPI(d), nil
}
