package conanhttp

import (
	"net/http"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// Conan exchanges Basic credentials for the Bearer token its client sends on
// subsequent calls. A caller that supplied an access token already receives
// that same token; a password receives a short-lived scoped token.
func (h handler) authenticate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.method(w, r)
		return
	}
	p, err := kernel.RequireAuthenticated(r.Context())
	if err != nil {
		h.error(w, r, err)
		return
	}
	// The Basic name must be the principal's, matched as the authenticator
	// matched it: without regard to case (accessapi.FoldUsername). The Conan
	// client sends the name as the user typed it (conan/internal/rest/
	// rest_client_v2.py, authenticate: HTTPBasicAuth(user, password)), so
	// BOB logs in as bob here as on every other route. The credential is
	// read as the authentication reads it (accessapi.BasicCredentials), not
	// with Request.BasicAuth: a lower-case "basic" value or "Basic Og=="
	// authenticates nothing in 7.x, so beside a key that did, it is no
	// credential to exchange.
	name, secret, ok := accessapi.BasicCredentials(r.Header.Get("Authorization"))
	if !ok || !accessapi.SameUsername(name, p.Name) || secret == "" {
		h.error(w, r, kernel.ErrUnauthenticated)
		return
	}
	if !accessapi.LooksLikeToken(secret) {
		if !p.Password || h.tokens == nil {
			h.error(w, r, apierror.ErrNotImplemented)
			return
		}
		expiry := time.Hour
		issued, err := h.tokens.Issue(r.Context(), accessapi.TokenRequest{
			Username: p.Name, Scope: p.Scope, Identity: p.Identity,
			ExpiresIn: &expiry, NotAfter: p.TokenExpires,
			Description: "Conan client login", ForceRevocable: true,
		})
		if err != nil {
			h.error(w, r, err)
			return
		}
		secret = issued.AccessToken
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Cache-Control", "no-store")
	//nolint:gosec // This is Conan's plain-text token exchange, not HTML.
	_, _ = w.Write(
		[]byte(secret),
	)
}

func (h handler) checkCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.method(w, r)
		return
	}
	p, err := kernel.RequireAuthenticated(r.Context())
	if err != nil {
		h.error(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(p.Name))
}
