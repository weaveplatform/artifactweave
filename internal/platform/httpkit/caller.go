package httpkit

import (
	"context"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// AdminOnly refuses a caller who is not an administrator
// (kernel.RequireAdmin) with the kernel's typed error wrapped as
// "repositories: %w". The wrap keeps the typed error, so the response
// carries its status: kernel.ErrUnauthenticated (401) without a user, and
// kernel.ErrForbidden (403) for a user who is not an administrator.
func AdminOnly(ctx context.Context) error {
	if _, err := kernel.RequireAdmin(ctx); err != nil {
		return fmt.Errorf("repositories: %w", err)
	}
	return nil
}

// Principal returns the caller of a read, or kernel.ErrUnauthenticated when
// ctx carries none. The middleware lets a request without one through only
// to public routes, so none here means a router built without
// authentication, which is refused as unauthenticated.
func Principal(ctx context.Context) (kernel.Principal, error) {
	p, ok := kernel.FromContext(ctx)
	if !ok {
		return p, kernel.ErrUnauthenticated
	}
	return p, nil
}

// Denied refuses a read: anonymous is told to authenticate
// (kernel.ErrUnauthenticated), a user that they lack the right
// (kernel.ErrForbidden).
func Denied(p kernel.Principal) error {
	if p.Anonymous {
		return kernel.ErrUnauthenticated
	}
	return kernel.ErrForbidden
}

// BadRequestf returns a 400 whose message is format applied to a.
func BadRequestf(format string, a ...any) error {
	return apierror.New(http.StatusBadRequest, fmt.Sprintf(format, a...))
}
