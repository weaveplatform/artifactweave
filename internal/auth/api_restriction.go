package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// ErrAPIRestricted is returned only after the credential has been verified.
var ErrAPIRestricted = apierror.New(http.StatusForbidden, "API access is temporarily restricted")

type apiRecoveryKey struct{}

// WithAPIRecovery permits credential verification for the narrowly mounted
// administrator recovery routes. Those routes must still call RequireAdmin;
// this context flag grants no identity or administrative authority.
func WithAPIRecovery(ctx context.Context) context.Context {
	return context.WithValue(ctx, apiRecoveryKey{}, true)
}

// CheckAPIRestriction checks current persisted policy without a permission
// cache. Missing policy means unrestricted; backend failures fail closed.
func CheckAPIRestriction(
	ctx context.Context,
	restrictions store.APIRestrictionStore,
	user string,
	now time.Time,
) error {
	if recovery, _ := ctx.Value(apiRecoveryKey{}).(bool); recovery {
		return nil
	}
	r, err := restrictions.Get(ctx, user)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("auth: API restriction: %w", err)
	}
	if r.Until.After(now) {
		return ErrAPIRestricted
	}
	return nil
}

func (a *Authenticator) apiAllowed(ctx context.Context, user string) error {
	if a.restrictions == nil {
		return nil
	}
	return CheckAPIRestriction(ctx, a.restrictions, user, a.now())
}
