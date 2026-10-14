package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// publicRoutes answer without credentials even when anonymous access is off:
// the probes and version reads clients make before they authenticate, and the
// telemetry sinks every jf command posts to (research 05).
var publicRoutes = map[string]bool{
	"GET /artifactory/api/system/ping":              true,
	"GET /artifactory/api/system/version":           true,
	"GET /artifactory/api/v1/system/readiness":      true,
	"GET /artifactory/api/v1/system/liveness":       true,
	"GET /access/api/v1/system/ping":                true,
	"GET /access/api/v1/system/version":             true,
	"GET /router/api/v1/system/health":              true,
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
// anonymous access is on; otherwise only public routes answer. Every
// principal is then authorised once (auth.Authorize): its permissions are
// evaluated for the request, and handlers ask that evaluator, through
// access, every question.
func authenticate(a *auth.Authenticator, st store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			if selfAuthenticated[r.Method+" "+r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			if p, presented, err := credentials(ctx, a, r); presented {
				if errors.Is(err, auth.ErrPasswordExpired) && ownPasswordChange(r, p.Name) {
					err = nil
				}
				if err == nil {
					p, err = authorize(ctx, st, p, false)
				} else if who, ok := expiredRefresh(ctx, a, r, err); ok {
					next.ServeHTTP(w, r.WithContext(auth.WithExpiredToken(ctx, who)))
					return
				}
				if err != nil {
					writeErr(w, r, err)
					return
				}
				next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(ctx, p)))
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
				next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(ctx, p)))
			case publicRoutes[r.Method+" "+r.URL.Path]:
				next.ServeHTTP(w, r)
			default:
				apierror.Write(w, surfaceFor(r.URL.Path), auth.ErrUnauthenticated)
			}
		})
	}
}

// ownPasswordChange reports whether r sets user's own password through
// Access v2, the one request an expired password may still make: without
// it, a user whose password expired could not choose a new one.
func ownPasswordChange(r *http.Request, user string) bool {
	rest, ok := strings.CutPrefix(r.URL.Path, "/access/api/v2/users/")
	name, ok2 := strings.CutSuffix(rest, "/password")
	return r.Method == http.MethodPut && ok && ok2 && user != "" && name == user
}

// credentials verifies whatever credential the request presents, reporting
// presented false when there is none. Every form decision 0005 accepts:
// Basic with a password, or with an access or reference token as the
// password (the user name must be the token's subject); Bearer with either
// token; X-JFrog-Art-Api with a reference token (API keys are retired). A
// credential that does not verify, or a scheme nothing accepts, answers 401
// rather than quietly becoming anonymous.
func credentials(
	ctx context.Context,
	a *auth.Authenticator,
	r *http.Request,
) (p auth.Principal, presented bool, err error) {
	if user, password, ok := r.BasicAuth(); ok {
		if auth.LooksLikeToken(password) {
			p, err = a.Token(ctx, password, user)
		} else {
			p, err = a.Basic(ctx, user, password)
		}
		return p, true, err //nolint:wrapcheck // apierror values, rendered by status
	}
	if h := r.Header.Get("Authorization"); h != "" {
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
	return auth.Principal{}, false, nil
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
