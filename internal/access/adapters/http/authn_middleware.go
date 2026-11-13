package accesshttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/access/core/authz"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// accessPublicRoutes are Access's routes that answer without credentials
// even when anonymous access is off: its probes, which clients make before
// they authenticate, its version, token creation and the OIDC exchange.
var accessPublicRoutes = map[string]bool{
	"GET /access/api/v1/system/ping":      true,
	"GET /access/api/v1/system/version":   true,
	"GET /access/api/v1/system/readiness": true,
	"GET /access/api/v1/system/liveness":  true,
	// Token creation, because a refresh grant carries its own credential,
	// the refresh token; the handlers require a user for every other grant.
	"POST /access/api/v1/tokens":           true,
	"POST /artifactory/api/security/token": true,
	// The OIDC token exchange, whose subject token is the credential.
	oidcTokenRoute: true,
}

// refreshRoute is the one route an expired access token reaches.
const refreshRoute = "POST /access/api/v1/tokens"

// AuthnOptions are the values Authenticate takes from the modules, which
// the router composes. Credential parsing (credentials) is not among them:
// every credential form is accepted the same way on every route.
// RegistryPath and ChallengeAnonymous must be set, because Authenticate calls
// them without a nil check; the maps and lists may be empty.
type AuthnOptions struct {
	// Auth verifies credentials and answers whether anonymous access is on.
	Auth *authn.Authenticator
	// Store holds the permissions each principal is authorised with.
	Store Store
	// SelfAuthenticated holds "METHOD path" of the routes that verify their
	// own credential, which pass untouched.
	SelfAuthenticated map[string]bool
	// Skip reports the requests that pass untouched, credentials or not,
	// asked in order.
	Skip []func(*http.Request) bool
	// RegistryPath reports whether path is the registry's, the only place a
	// registry token is good.
	RegistryPath func(path string) bool
	// ChallengeAnonymous reports whether a request to path without
	// credentials is challenged even while anonymous access is on.
	ChallengeAnonymous func(path string) bool
	// CredentialKeepers each return the context an authenticated request
	// goes on with, in order: the hook for the operations that need the
	// credential itself, which no other request carries further than here.
	CredentialKeepers []func(context.Context, *http.Request, kernel.Principal) context.Context
	// PublicRoutes holds "METHOD path" of the routes that answer without
	// credentials while anonymous access is off.
	PublicRoutes map[string]bool
}

// Authenticate puts the request's principal in its context. It decides in
// this order, and the order matters:
//
//  1. A self-authenticated route passes untouched.
//  2. So does a request a skip reports, credentials or not, the skips asked
//     in order. npm's login and logout come first (npmUserPath): they check
//     the password and the token they carry themselves, and a stale
//     credential read there could suspend the very login that replaces it.
//     Then a protocol's absent path, such as Go's checksum database, which
//     its router answers 404 whoever asks, a host route served to anyone,
//     such as Terraform's discovery document, and a signed URL, such as
//     Terraform's downloads, whose handler checks the signature
//     (pkgroute.Protocol.SkipsAuthentication).
//  3. Credentials that do not verify answer 401 at once, even on a public
//     route, as Artifactory does: a client with a wrong password learns it
//     on its first call. An expired password may still change itself
//     (ownPasswordChange), and a registry token is good for the registry
//     alone (decision 0005).
//  4. The exception is an expired Bearer access token on refreshRoute
//     (expiredRefresh).
//  5. A principal that verified is authorised once (authn.Authorize): its
//     permissions are evaluated for the request, and the principal is
//     stored with that evaluator (authn.WithAuthorized), which handlers ask
//     every question through authn.DeciderFrom. The credential keepers add
//     to its context first, in order. The principal's name goes to the
//     access log (httpkit.NoteUser).
//  6. Without credentials, a path that challenges anonymous requests
//     answers 401 even while anonymous access is on (decision 0031).
//  7. Otherwise the request acts as anonymous, authorised the same way,
//     when anonymous access is on.
//  8. With anonymous access off, only public routes answer.
func Authenticate(o AuthnOptions) func(http.Handler) http.Handler {
	skipped := func(r *http.Request) bool {
		return slices.ContainsFunc(
			o.Skip,
			func(skip func(*http.Request) bool) bool { return skip(r) },
		)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			if o.SelfAuthenticated[r.Method+" "+r.URL.Path] || skipped(r) {
				next.ServeHTTP(w, r)
				return
			}
			if p, presented, err := credentials(ctx, o.Auth, r); presented {
				if errors.Is(err, kernel.ErrPasswordExpired) && ownPasswordChange(r, p.Name) {
					err = nil
				}
				if err == nil && p.Registry != nil && !o.RegistryPath(r.URL.Path) {
					// A registry token is good for the registry alone
					// (decision 0005).
					err = kernel.ErrBadCredentials
				}
				var d *authz.Decider
				if err == nil {
					p, d, err = authorize(ctx, o.Store, p, p.Anonymous)
				} else if who, ok := expiredRefresh(ctx, o.Auth, r, err); ok {
					next.ServeHTTP(w, r.WithContext(authn.WithExpiredToken(ctx, who)))
					return
				}
				if err != nil {
					httpkit.WriteErr(w, r, err)
					return
				}
				httpkit.NoteUser(ctx, p.Name)
				kept := ctx
				for _, keep := range o.CredentialKeepers {
					kept = keep(kept, r, p)
				}
				next.ServeHTTP(w, r.WithContext(authn.WithAuthorized(kept, p, d)))
				return
			}
			if o.ChallengeAnonymous(r.URL.Path) {
				// The registry challenges a request without credentials even
				// while anonymous access is on: an anonymous client then
				// fetches an anonymous token and pulls with it, and one with
				// credentials learns at once to send them (decision 0031).
				apierror.Write(w, apierror.ProfileOCI, kernel.ErrUnauthenticated)
				return
			}
			anonymous, err := o.Auth.AnonymousAllowed(ctx)
			if err != nil {
				httpkit.WriteErr(w, r, err)
				return
			}
			switch {
			case anonymous:
				p, d, err := authorize(ctx, o.Store, kernel.Anonymous, true)
				if err != nil {
					httpkit.WriteErr(w, r, err)
					return
				}
				httpkit.NoteUser(ctx, p.Name)
				next.ServeHTTP(w, r.WithContext(authn.WithAuthorized(ctx, p, d)))
			case o.PublicRoutes[r.Method+" "+r.URL.Path]:
				next.ServeHTTP(w, r)
			default:
				apierror.Write(w, httpkit.ErrorProfile(r), kernel.ErrUnauthenticated)
			}
		})
	}
}

// legacyPasswordChange is the legacy security API's password change. It
// names its user in the body, which the middleware does not read, so the
// handler holds an expired password to its own name (ChangePassword).
const legacyPasswordChange = "POST " + securityUsers + "authorization/changePassword"

// ownPasswordChange reports whether r sets user's own password, through
// Access v2 or the legacy change, the requests an expired password may still
// make: without them, a user whose password expired could not choose a new
// one.
func ownPasswordChange(r *http.Request, user string) bool {
	if r.Method+" "+r.URL.Path == legacyPasswordChange {
		return user != ""
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/access/api/v2/users/")
	name, ok2 := strings.CutSuffix(rest, "/password")
	return r.Method == http.MethodPut && ok && ok2 && user != "" && model.SameUsername(name, user)
}

// credentials verifies whatever credential the request presents, reporting
// presented false when there is none. Every form decision 0005 accepts:
// Basic with a password, or with an access or reference token as the
// password (the user name must be the token's subject); Bearer with either
// token; X-JFrog-Art-Api with a reference token (API keys are retired); and
// on the NuGet routes alone, X-NuGet-ApiKey (nugetAPIKey). A credential
// that does not verify, or a scheme nothing accepts, answers 401 rather
// than quietly becoming anonymous.
func credentials(
	ctx context.Context,
	a *authn.Authenticator,
	r *http.Request,
) (p kernel.Principal, presented bool, err error) {
	if user, password, ok := r.BasicAuth(); ok {
		p, err = basicCredentials(ctx, a, user, password)
		return p, true, err
	}
	if h := r.Header.Get("Authorization"); h != "" {
		// Cargo sends the configured registry token as a bare Authorization
		// value on sparse registry and web API requests.
		if strings.HasPrefix(r.URL.Path, "/artifactory/api/cargo/") &&
			!strings.ContainsAny(h, " \t") {
			p, err = a.Token(ctx, h, "")
			if err != nil {
				return p, true, fmt.Errorf("cargo authorization: %w", err)
			}
			return p, true, nil
		}
		if strings.HasPrefix(r.URL.Path, "/artifactory/api/gems/") && !strings.Contains(h, " ") &&
			accessapi.LooksLikeToken(h) {
			p, err = a.Token(ctx, h, "")
			return p, true, err //nolint:wrapcheck // Authentication errors are rendered by status.
		}
		scheme, raw, _ := strings.Cut(h, " ")
		if !strings.EqualFold(scheme, "Bearer") || raw == "" {
			return kernel.Principal{}, true, kernel.ErrBadCredentials
		}
		p, err = a.Token(ctx, strings.TrimSpace(raw), "")
		return p, true, err //nolint:wrapcheck // apierror values, rendered by status
	}
	if k := r.Header.Get("X-JFrog-Art-Api"); k != "" {
		if !accessapi.IsReferenceToken(k) {
			return kernel.Principal{}, true, kernel.ErrBadCredentials
		}
		p, err = a.Token(ctx, k, "")
		return p, true, err //nolint:wrapcheck // apierror values, rendered by status
	}
	if user, secret, ok := nugetAPIKey(r); ok {
		if user == "" || secret == "" {
			return kernel.Principal{}, true, kernel.ErrBadCredentials
		}
		p, err = basicCredentials(ctx, a, user, secret)
		return p, true, err
	}
	return kernel.Principal{}, false, nil
}

// basicCredentials verifies a user and the secret Basic carries with it: a
// password, or an access or reference token whose subject is the user.
func basicCredentials(
	ctx context.Context,
	a *authn.Authenticator,
	user, secret string,
) (kernel.Principal, error) {
	if accessapi.LooksLikeToken(secret) {
		return a.Token(ctx, secret, user) //nolint:wrapcheck // apierror values, rendered by status
	}
	return a.Basic(ctx, user, secret) //nolint:wrapcheck // apierror values, rendered by status
}

// nugetAPI prefixes the NuGet routes, the only ones that read
// X-NuGet-ApiKey.
const nugetAPI = "/artifactory/api/nuget/"

// nugetAPIKey returns the user and secret of the request's X-NuGet-ApiKey,
// which 7.x reads as Basic credentials "<user>:<password or token>"
// (decision 0038), reporting whether the request sends one this route
// reads. Only the NuGet routes read it, and only without an Authorization
// header: the NuGet client sends its key with a push or a delete and its
// stored credentials only after a 401, so a stale key must not hide
// credentials that would succeed. A value without a ":" has no secret, and
// credentials answers it 401, as JFrog supports no key alone
// (RTFACT-20340).
func nugetAPIKey(r *http.Request) (string, string, bool) {
	v := r.Header.Get("X-NuGet-ApiKey")
	if v == "" || r.Header.Get("Authorization") != "" || !strings.HasPrefix(r.URL.Path, nugetAPI) {
		return "", "", false
	}
	user, secret, _ := strings.Cut(v, ":")
	return user, secret, true
}

// expiredRefresh reports the Bearer access token a request to refreshRoute
// presented when it failed only for having expired. The JFrog CLI refreshes
// with the token it is replacing as its Bearer credential (jfrog-client-go's
// handleUnauthenticated), and it refreshes because that token is expiring or
// has expired. The request then reaches the handler with no principal, which
// accepts only a refresh grant whose refresh token belongs to that token;
// revoked and forged tokens still answer 401.
func expiredRefresh(
	ctx context.Context,
	a *authn.Authenticator,
	r *http.Request,
	err error,
) (authn.Refresher, bool) {
	if !errors.Is(err, kernel.ErrBadCredentials) || r.Method+" "+r.URL.Path != refreshRoute {
		return authn.Refresher{}, false
	}
	scheme, raw, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return authn.Refresher{}, false
	}
	return a.ExpiredToken(ctx, strings.TrimSpace(raw))
}

// authorize evaluates p's permissions for the request, returning p and its
// decider as authn.Authorize does. A handler built without a store (tests of
// the system endpoints) has nothing to evaluate, and p passes through
// without a decider.
func authorize(
	ctx context.Context,
	st Store,
	p kernel.Principal,
	anonymous bool,
) (kernel.Principal, *authz.Decider, error) {
	if st == nil {
		return p, nil, nil
	}
	return authn.Authorize(ctx, authz.Sources{ //nolint:wrapcheck // auth wraps its own errors
		Permissions: st.Permissions(), Groups: st.Groups(), AnonymousEnabled: anonymous,
	}, p)
}
