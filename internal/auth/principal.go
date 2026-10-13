package auth

import (
	"context"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
)

// AnonymousUser is the name of the built-in anonymous user.
const AnonymousUser = "anonymous"

// Principal is who a request acts as.
type Principal struct {
	Name      string
	Admin     bool
	Anonymous bool
	Groups    []string
}

// Anonymous is the principal of a request without credentials when
// anonymous access is on.
var Anonymous = Principal{Name: AnonymousUser, Anonymous: true}

// The messages JFrog clients print. No public source documents Artifactory's
// exact wording; jfrog-client-go shows errors[].message as it comes.
var (
	// ErrBadCredentials answers credentials that do not verify.
	ErrBadCredentials = apierror.New(http.StatusUnauthorized, "Bad credentials")
	// ErrUnauthenticated answers a request that needs a user and has none.
	ErrUnauthenticated = apierror.New(http.StatusUnauthorized, "Authentication is required")
	// ErrForbidden answers an authenticated user without the right.
	ErrForbidden = apierror.New(http.StatusForbidden, "Forbidden")
)

type ctxKey struct{}

// WithPrincipal returns ctx carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the request's principal; ok is false when the request
// is unauthenticated (no credentials and anonymous access off).
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// RequireAuthenticated returns ErrUnauthenticated unless the request carries
// a user other than anonymous.
func RequireAuthenticated(ctx context.Context) (Principal, error) {
	p, ok := FromContext(ctx)
	if !ok || p.Anonymous {
		return Principal{}, ErrUnauthenticated
	}
	return p, nil
}

// RequireAdmin returns ErrUnauthenticated without a user and ErrForbidden for
// a user who is not an administrator, the split Artifactory makes between
// 401 and 403.
func RequireAdmin(ctx context.Context) (Principal, error) {
	p, err := RequireAuthenticated(ctx)
	if err != nil {
		return p, err
	}
	if !p.Admin {
		return Principal{}, ErrForbidden
	}
	return p, nil
}
