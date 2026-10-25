package conan

import (
	"net/http"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
)

// Conan exchanges Basic credentials for the Bearer token its client sends on
// subsequent calls. A caller that supplied an access token already receives
// that same token; a password receives a short-lived scoped token.
func (h handler) authenticate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.method(w, r)
		return
	}
	p, err := auth.RequireAuthenticated(r.Context())
	if err != nil {
		h.error(w, r, err)
		return
	}
	name, secret, ok := r.BasicAuth()
	if !ok || name != p.Name || secret == "" {
		h.error(w, r, auth.ErrUnauthenticated)
		return
	}
	if !auth.LooksLikeToken(secret) {
		if !p.Password || h.tokens == nil {
			h.error(w, r, apierror.ErrNotImplemented)
			return
		}
		expiry := time.Hour
		issued, err := h.tokens.Issue(r.Context(), token.Request{
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
	p, err := auth.RequireAuthenticated(r.Context())
	if err != nil {
		h.error(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(p.Name))
}
