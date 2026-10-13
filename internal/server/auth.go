package server

import (
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
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

// authenticate puts the request's principal in its context. Credentials
// that do not verify answer 401 at once, even on a public route, as
// Artifactory does: a client with a wrong password learns it on its first
// call. Without credentials the request acts as anonymous when anonymous
// access is on; otherwise only public routes answer, and handlers decide the
// rest from the principal (auth.RequireAdmin and friends).
func authenticate(a *auth.Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			if selfAuthenticated[r.Method+" "+r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			if user, password, ok := r.BasicAuth(); ok {
				p, err := a.Basic(ctx, user, password)
				if err != nil {
					apierror.Write(w, surfaceFor(r.URL.Path), err)
					return
				}
				next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(ctx, p)))
				return
			}
			// Bearer and API-key credentials arrive with tokens (P4). Until
			// then they cannot verify, and a credential that cannot verify
			// must not quietly become anonymous.
			if r.Header.Get("Authorization") != "" || r.Header.Get("X-JFrog-Art-Api") != "" {
				apierror.Write(w, surfaceFor(r.URL.Path), auth.ErrBadCredentials)
				return
			}
			anonymous, err := a.AnonymousAllowed(ctx)
			if err != nil {
				apierror.Write(w, surfaceFor(r.URL.Path), err)
				return
			}
			switch {
			case anonymous:
				next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(ctx, auth.Anonymous)))
			case publicRoutes[r.Method+" "+r.URL.Path]:
				next.ServeHTTP(w, r)
			default:
				apierror.Write(w, surfaceFor(r.URL.Path), auth.ErrUnauthenticated)
			}
		})
	}
}
