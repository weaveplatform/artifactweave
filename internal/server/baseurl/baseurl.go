// Package baseurl resolves the public base URL a response must embed.
//
// npm tarball links, Helm chart URLs, the NuGet service index, Terraform
// download URLs, Docker upload locations and file-info URIs all carry absolute
// URLs. Behind a reverse proxy the request's own host is wrong, so the resolver
// follows Artifactory's precedence: the X-JFrog-Override-Base-Url header, then
// the X-Forwarded-* headers, then the configured base URL, then the request.
// Every handler reads the result from the context so there is exactly one place
// that implements the rule (decision 0002).
package baseurl

import (
	"context"
	"net/http"
	"strings"
)

type ctxKey struct{}

// OverrideHeader is the header Artifactory clients and proxies use to force the
// base URL for one request.
const OverrideHeader = "X-JFrog-Override-Base-Url"

// Resolve returns the base URL for r without a trailing slash. configured is
// the operator-supplied base URL and may be empty.
func Resolve(r *http.Request, configured string) string {
	if v := strings.TrimSpace(r.Header.Get(OverrideHeader)); v != "" {
		return strings.TrimRight(v, "/")
	}
	if host := r.Header.Get("X-Forwarded-Host"); host != "" {
		scheme := r.Header.Get("X-Forwarded-Proto")
		if scheme == "" {
			scheme = requestScheme(r)
		}
		// A forwarded port is only meaningful when the host carries none; proxies
		// that put the port in X-Forwarded-Host must not get it twice.
		if port := r.Header.Get(
			"X-Forwarded-Port",
		); port != "" && !strings.Contains(host, ":") &&
			!isDefaultPort(scheme, port) {
			host += ":" + port
		}
		return scheme + "://" + host
	}
	if configured != "" {
		return strings.TrimRight(configured, "/")
	}
	return requestScheme(r) + "://" + r.Host
}

// Middleware stores the resolved base URL in the request context.
func Middleware(configured string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), ctxKey{}, Resolve(r, configured))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// From returns the base URL stored by Middleware, or "" when the middleware
// did not run. Callers treat "" as a programming error, not a fallback.
func From(ctx context.Context) string {
	v, _ := ctx.Value(ctxKey{}).(string)
	return v
}

func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func isDefaultPort(scheme, port string) bool {
	return (scheme == "https" && port == "443") || (scheme == "http" && port == "80")
}
