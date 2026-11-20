package itemkit

import (
	"context"
	"errors"
	"fmt"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// CurrentAccess takes the shared authority guard before refreshing credentials
// and evaluating current policy. Publication callers retain the supplied shared
// store transaction through their mutation and call this before any source,
// work or item lock. Outside a transaction it is only a current read.
func (s *Kit) CurrentAccess(ctx context.Context) (kernel.Principal, Access, error) {
	p, err := httpkit.Principal(ctx)
	if err != nil {
		return kernel.Principal{}, Access{}, err
	}
	if s.Access.Authenticator == nil || s.Access.Authorizer == nil {
		return kernel.Principal{}, Access{}, apierror.ErrNotImplemented
	}
	if s.Access.Projects != nil {
		if err := s.Access.Projects.Guard(ctx); err != nil {
			return kernel.Principal{}, Access{}, fmt.Errorf("guard current publication authority: %w", err)
		}
	}
	if p.Registry != nil {
		p, err = s.Access.Authenticator.CurrentRegistry(ctx, p)
	} else {
		p, err = s.Access.Authenticator.Current(ctx, p)
	}
	if err != nil {
		if errors.Is(err, kernel.ErrNotFound) {
			// An ended user/token is a credential refusal, not an item miss.
			return kernel.Principal{}, Access{}, kernel.ErrBadCredentials
		}
		return kernel.Principal{}, Access{}, fmt.Errorf("current publication credential: %w", err)
	}
	// The request's cached Decider is deliberately not an input here: role,
	// group, permission and project ownership changes must affect publication.
	d, err := s.Access.Authorizer.For(ctx, p)
	if err != nil {
		return kernel.Principal{}, Access{}, fmt.Errorf("current publication permissions: %w", err)
	}
	if d == nil {
		return kernel.Principal{}, Access{}, kernel.ErrForbidden
	}
	p.Admin, p.Groups = d.IsAdmin(), d.Groups()
	return p, Access{Decider: d}, nil
}

// PublicationCheck checks the actual client target after streaming and format
// path admission, through CurrentAccess in the item's transaction. Repository
// classes are immutable; the authorizer resolves current canonical ownership
// and environments instead of trusting c's old project assignment.
func (s *Kit) PublicationCheck(c repo.Config) func(context.Context, string, bool, accessapi.Actions) error {
	return func(ctx context.Context, path string, folder bool, want accessapi.Actions) error {
		p, access, err := s.CurrentAccess(ctx)
		if err != nil {
			return err
		}
		if !access.Item(c, path, folder, want) {
			return fmt.Errorf("%w: %w", item.ErrPermissionDenied, httpkit.Denied(p))
		}
		return nil
	}
}
