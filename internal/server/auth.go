package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/docker"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// publicRoutes answer without credentials even when anonymous access is off:
// the probes clients make before they authenticate, the Access version, and
// the telemetry sinks every jf command posts to (research 05).
//
// GET /artifactory/api/system/version is deliberately absent: Artifactory
// requires a valid user for it and has refused anonymous callers since
// 7.35.1 (RTFACT-26414), so it answers as any other route does, anonymous
// only while anonymous access is on (research 01). The JFrog CLI and the
// Terraform providers send their credentials with it; the CLI's usage
// report, which sends none when none are configured, only logs the 401.
var publicRoutes = map[string]bool{
	"GET /artifactory/api/system/ping":              true,
	"GET /artifactory/api/v1/system/readiness":      true,
	"GET /artifactory/api/v1/system/liveness":       true,
	"GET /access/api/v1/system/ping":                true,
	"GET /access/api/v1/system/version":             true,
	"GET /router/api/v1/system/health":              true,
	"GET /router/api/v1/system/readiness":           true,
	"GET /router/api/v1/system/liveness":            true,
	"GET /access/api/v1/system/readiness":           true,
	"GET /access/api/v1/system/liveness":            true,
	"POST /artifactory/api/system/usage":            true,
	"POST /jfconnect/api/v1/backoffice/metrics/log": true,
	// Token creation, because a refresh grant carries its own credential,
	// the refresh token; the handlers require a user for every other grant.
	"POST /access/api/v1/tokens":           true,
	"POST /artifactory/api/security/token": true,
	// The OIDC token exchange, whose subject token is the credential.
	oidcTokenRoute: true,
}

// selfAuthenticated routes verify their own credential, so the middleware
// passes them untouched: the multipart upload calls carry the upload token
// as a Bearer token, which only the multipart service can verify until
// access tokens arrive (P4), and a part PUT carries a signed URL and no
// credentials at all, as the CLI sends it.
var selfAuthenticated = map[string]bool{
	"POST " + uploadsURLPart:  true,
	"PUT " + uploadsPart:      true,
	"POST " + uploadsComplete: true,
	"POST " + uploadsStatus:   true,
	"POST " + uploadsAbort:    true,
}

// refreshRoute is the one route an expired access token reaches.
const refreshRoute = "POST /access/api/v1/tokens"

// authenticate puts the request's principal in its context. Credentials
// that do not verify answer 401 at once, even on a public route, as
// Artifactory does: a client with a wrong password learns it on its first
// call. The exception is an expired Bearer access token on refreshRoute
// (expiredRefresh). Without credentials the request acts as anonymous when
// anonymous access is on, except on the registry (decision 0031); otherwise
// only public routes answer. Every
// principal is then authorised once (auth.Authorize): its permissions are
// evaluated for the request, and handlers ask that evaluator, through
// access, every question. The principal's name goes to the access log
// (noteUser). A request skip reports passes untouched, credentials or not: a
// protocol's absent path, such as Go's checksum database, which its router
// answers 404 whoever asks, a host route served to anyone, such as
// Terraform's discovery document, and a signed URL, such as Terraform's
// downloads, whose handler checks the signature
// (pkgroute.Protocol.SkipsAuthentication). So do npm's login and logout,
// which check the password and the token they carry themselves
// (npmUserPath).
func authenticate(
	a *auth.Authenticator,
	st store.Store,
	skip func(r *http.Request) bool,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			if selfAuthenticated[r.Method+" "+r.URL.Path] || npmUserPath(r.URL.EscapedPath()) ||
				skip != nil && skip(r) {
				next.ServeHTTP(w, r)
				return
			}
			if p, presented, err := credentials(ctx, a, r); presented {
				if errors.Is(err, auth.ErrPasswordExpired) && ownPasswordChange(r, p.Name) {
					err = nil
				}
				if err == nil && p.Registry != nil && !docker.IsRegistryPath(r.URL.Path) {
					// A registry token is good for the registry alone
					// (decision 0005).
					err = auth.ErrBadCredentials
				}
				if err == nil {
					p, err = authorize(ctx, st, p, p.Anonymous)
				} else if who, ok := expiredRefresh(ctx, a, r, err); ok {
					next.ServeHTTP(w, r.WithContext(auth.WithExpiredToken(ctx, who)))
					return
				}
				if err != nil {
					writeErr(w, r, err)
					return
				}
				noteUser(ctx, p.Name)
				next.ServeHTTP(
					w,
					r.WithContext(auth.WithPrincipal(keepNPMCredential(ctx, r, p), p)),
				)
				return
			}
			if docker.ChallengesAnonymous(r.URL.Path) {
				// The registry challenges a request without credentials even
				// while anonymous access is on: an anonymous client then
				// fetches an anonymous token and pulls with it, and one with
				// credentials learns at once to send them (decision 0031).
				apierror.Write(w, apierror.SurfaceOCI, auth.ErrUnauthenticated)
				return
			}
			anonymous, err := a.AnonymousAllowed(ctx)
			if err != nil {
				writeErr(w, r, err)
				return
			}
			switch {
			case anonymous:
				p, err := authorize(ctx, st, auth.Anonymous, true)
				if err != nil {
					writeErr(w, r, err)
					return
				}
				noteUser(ctx, p.Name)
				next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(ctx, p)))
			case publicRoutes[r.Method+" "+r.URL.Path]:
				next.ServeHTTP(w, r)
			default:
				apierror.Write(w, surfaceFor(r.URL.Path), auth.ErrUnauthenticated)
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
	return r.Method == http.MethodPut && ok && ok2 && user != "" && name == user
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
	a *auth.Authenticator,
	r *http.Request,
) (p auth.Principal, presented bool, err error) {
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
			auth.LooksLikeToken(h) {
			p, err = a.Token(ctx, h, "")
			return p, true, err //nolint:wrapcheck // Authentication errors are rendered by status.
		}
		scheme, raw, _ := strings.Cut(h, " ")
		if !strings.EqualFold(scheme, "Bearer") || raw == "" {
			return auth.Principal{}, true, auth.ErrBadCredentials
		}
		p, err = a.Token(ctx, strings.TrimSpace(raw), "")
		return p, true, err //nolint:wrapcheck // apierror values, rendered by status
	}
	if k := r.Header.Get("X-JFrog-Art-Api"); k != "" {
		if !types.IsReferenceToken(k) {
			return auth.Principal{}, true, auth.ErrBadCredentials
		}
		p, err = a.Token(ctx, k, "")
		return p, true, err //nolint:wrapcheck // apierror values, rendered by status
	}
	if user, secret, ok := nugetAPIKey(r); ok {
		if user == "" || secret == "" {
			return auth.Principal{}, true, auth.ErrBadCredentials
		}
		p, err = basicCredentials(ctx, a, user, secret)
		return p, true, err
	}
	return auth.Principal{}, false, nil
}

// basicCredentials verifies a user and the secret Basic carries with it: a
// password, or an access or reference token whose subject is the user.
func basicCredentials(
	ctx context.Context,
	a *auth.Authenticator,
	user, secret string,
) (auth.Principal, error) {
	if auth.LooksLikeToken(secret) {
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
	a *auth.Authenticator,
	r *http.Request,
	err error,
) (auth.Refresher, bool) {
	if !errors.Is(err, auth.ErrBadCredentials) || r.Method+" "+r.URL.Path != refreshRoute {
		return auth.Refresher{}, false
	}
	scheme, raw, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return auth.Refresher{}, false
	}
	return a.ExpiredToken(ctx, strings.TrimSpace(raw))
}

// authorize evaluates p's permissions for the request. A handler built
// without a store (tests of the system endpoints) has nothing to evaluate.
func authorize(
	ctx context.Context,
	st store.Store,
	p auth.Principal,
	anonymous bool,
) (auth.Principal, error) {
	if st == nil {
		return p, nil
	}
	return auth.Authorize(ctx, authz.Sources{ //nolint:wrapcheck // auth wraps its own errors
		Permissions: st.Permissions(), Groups: st.Groups(), AnonymousEnabled: anonymous,
	}, p)
}
