package auth

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// AnonymousUser is the name of the built-in anonymous user.
const AnonymousUser = "anonymous"

// Principal is who a request acts as.
type Principal struct {
	Name      string
	Admin     bool
	Anonymous bool
	Groups    []string
	// Scope is the scope of the access token the request presented; empty
	// for a password or anonymous request, which act with the user's full
	// permissions. Scopes only narrow (decision 0005); the permission
	// evaluator applies them.
	Scope types.Scope
	// TokenID is the id of the access or reference token the request
	// presented; empty for a password or anonymous request. The token API's
	// "me" resolves to it, and encryptedPassword refuses token callers.
	TokenID string
	// TokenExpires is when that token expires; zero for a password, an
	// anonymous request or a token that never expires. A registry token
	// obtained with a token lives no longer (decision 0005).
	TokenExpires time.Time
	// Registry is what a registry token grants, nil for any other
	// credential. Such a principal reaches the registry's routes alone.
	Registry *types.RegistryAccess
	// PasswordExpired marks a principal whose correct password has expired.
	// The middleware lets it through only to change its own password.
	PasswordExpired bool
	// token marks a principal authenticated by an access token, whose Scope
	// applies even when it is empty.
	token bool
	// access is the request's permission evaluator, set by Authorize.
	access *authz.Decider
}

// Subject is p as the permission evaluator starts from it: the user's own
// admin flag and groups, and the token's scope when a token authenticated
// the request.
func (p Principal) Subject() authz.Subject {
	s := authz.Subject{
		Name: p.Name, Groups: slices.Clone(p.Groups), Admin: p.Admin, Anonymous: p.Anonymous,
	}
	if p.token {
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

// Access returns the request's permission evaluator, or nil before
// Authorize has run.
func (p Principal) Access() *authz.Decider { return p.access }

// Authorize evaluates p's permissions once for the request (decision 0006)
// and returns p with Admin and Groups set to what the evaluator decides: a
// token scope can remove the admin flag and narrow the groups, and an
// admin-privilege group makes a member an administrator. Handlers then ask
// Access() every permission question.
func Authorize(ctx context.Context, src authz.Sources, p Principal) (Principal, error) {
	d, err := authz.New(ctx, src, p.Subject())
	if err != nil {
		return Principal{}, fmt.Errorf("auth: permissions: %w", err)
	}
	p.Admin, p.Groups, p.access = d.Admin(), d.Groups(), d
	return p, nil
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
