package docker

import (
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
)

// nullRealm is the repository key a realm names when the request addresses
// no repository: the bare probe and the catalog on the repository path
// (research 17).
const nullRealm = "null"

// Challenge gives every 401 from the registry the Bearer challenge Docker
// clients follow to the token endpoint (decision 0005), whoever wrote it:
// the authentication middleware, a handler's Require, or a later feature.
// A 401 written with apierror's default Basic challenge, or none, gets the
// registry's; one a handler challenged itself (a mount's second scope)
// keeps its own. It must run after the base-URL middleware.
func (g *Registry) Challenge(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsRegistryPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(&challenger{ResponseWriter: w, g: g, r: r}, r)
	})
}

// challenger sets the challenge on a 401 as its header is written.
type challenger struct {
	http.ResponseWriter

	g *Registry
	r *http.Request
}

func (c *challenger) WriteHeader(status int) {
	if status == http.StatusUnauthorized {
		h := c.Header()
		if v := h.Get("WWW-Authenticate"); v == "" || v == apierror.BasicChallenge {
			h.Set("WWW-Authenticate", c.g.challenge(c.r))
		}
	}
	c.ResponseWriter.WriteHeader(status)
}

// Unwrap lets http.ResponseController reach the writer underneath.
func (c *challenger) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// Flush and ReadFrom keep what the writer underneath offers: blob
// downloads stream through ReadFrom.
func (c *challenger) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (c *challenger) ReadFrom(src io.Reader) (int64, error) {
	return io.Copy(c.ResponseWriter, src) //nolint:wrapcheck // the writer's own error
}

// challenge is the WWW-Authenticate value for r: the realm on the base URL
// the client addressed, naming the repository the request addresses (null
// for none), the service, and the scope the request needs.
func (g *Registry) challenge(r *http.Request) string {
	base := baseurl.From(r.Context())
	realm, scope := g.challengeTarget(r)
	v := `Bearer realm="` + base + prefix + realm + `/v2/token",service="` + service(base) + `"`
	if scope != "" {
		v += `,scope="` + scope + `"`
	}
	return v
}

// challengeTarget is the repository key a request's realm names and the
// scope it asks for: repository:<name>:<actions> for an operation on an
// image, with <name> as the client wrote it, so that it is what the client
// asks the token endpoint for; registry:catalog:* for the catalog; none
// for the probe, the token and auth endpoints. A key or name that could
// not stand inside a quoted parameter is left out.
func (g *Registry) challengeTarget(r *http.Request) (realm, scope string) {
	rest, _ := registryRest(r.URL.Path)
	realm = nullRealm
	if after, ok := strings.CutPrefix(r.URL.Path, prefix); ok {
		realm, _, _ = strings.Cut(after, "/")
	} else if label, ok := g.subdomainLabel(r); ok {
		realm = label
	}
	if t, ok := parseTarget(rest); ok {
		switch {
		case t.kind.image():
			if realm == nullRealm {
				// The repository path: the name begins with the repository.
				realm, _, _ = strings.Cut(t.image, "/")
			}
			scope = "repository:" + t.image + ":" + scopeActions(r.Method)
		case t.kind == kindRepoCatalog:
			realm, scope = t.image, catalogScope
		case t.kind == KindCatalog:
			scope = catalogScope
		}
	}
	if !quotable(realm) || realm == "" {
		realm = nullRealm
	}
	if !quotable(scope) {
		scope = ""
	}
	return realm, scope
}

// subdomainLabel is the repository label of a host under the subdomain
// method, without looking the repository up: a challenge only names it.
func (g *Registry) subdomainLabel(r *http.Request) (string, bool) {
	if g.parent == "" {
		return "", false
	}
	label, ok := strings.CutSuffix(addressedHost(r), "."+g.parent)
	return label, ok && label != ""
}

// scopeActions are the actions a request's method needs, as Docker's
// registry asks for them: a push reads as well.
func scopeActions(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead:
		return actionPull
	case http.MethodDelete:
		return actionDelete
	default:
		return actionPull + "," + actionPush
	}
}

// quotable reports whether s can stand inside a quoted header parameter:
// a path is client input, and a quote or a control character in it must
// not end the parameter.
func quotable(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool {
		return r == '"' || r == '\\' || r < ' ' || r == 0x7f
	})
}

// service is the host of the base URL with any port that is not the
// scheme's default (research 17).
func service(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	if p := u.Port(); (u.Scheme == "https" && p == "443") || (u.Scheme == "http" && p == "80") {
		return u.Hostname()
	}
	return u.Host
}
