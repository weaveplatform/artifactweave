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
	// An API key authenticates a request that sends one whatever its
	// Authorization header says, as 7.x's props filter does, so a Basic
	// value beside it was never verified and is not exchanged. The key is
	// the token the caller supplied, and comes back as it is, as a token
	// sent as the Basic password does and as npm's and Docker's credential
	// answers hand it back. 7.x's Conan handler is not open source, so its
	// answer to a key is [I].
	if key, ok := accessapi.APIKey(r.Header); ok {
		writeToken(w, key)
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
	writeToken(w, secret)
}

// writeToken answers Conan's login with token, as plain text the client
// keeps as its Bearer token.
func writeToken(w http.ResponseWriter, token string) {
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Cache-Control", "no-store")
	//nolint:gosec // This is Conan's plain-text token exchange, not HTML.
	_, _ = w.Write(
		[]byte(token),
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
