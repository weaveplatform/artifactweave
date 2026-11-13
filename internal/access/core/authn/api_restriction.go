package authn

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

type apiRecoveryKey struct{}

// WithAPIRecovery permits credential verification for the narrowly mounted
// administrator recovery routes. Those routes must still call
// kernel.RequireAdmin; this context flag grants no identity or
// administrative authority.
func WithAPIRecovery(ctx context.Context) context.Context {
	return context.WithValue(ctx, apiRecoveryKey{}, true)
}

// CheckAPIRestriction checks current persisted policy without a permission
// cache. Missing policy means unrestricted; backend failures fail closed.
// Restrictions are stored under folded user names, and user is folded to
// match: a token issued for BOB is held to bob's restriction.
func CheckAPIRestriction(
	ctx context.Context,
	restrictions port.APIRestrictionStore,
	user string,
	now time.Time,
) error {
	if recovery, _ := ctx.Value(apiRecoveryKey{}).(bool); recovery {
		return nil
	}
	r, err := restrictions.Get(ctx, model.FoldUsername(user))
	if errors.Is(err, kernel.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("auth: API restriction: %w", err)
	}
	if r.Until.After(now) {
		return kernel.ErrAPIRestricted
	}
	return nil
}

func (a *Authenticator) apiAllowed(ctx context.Context, user string) error {
	if a.restrictions == nil {
		return nil
	}
	return CheckAPIRestriction(ctx, a.restrictions, user, a.now())
}
