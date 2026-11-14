package dockerhttp

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
)

// authEntry is a docker config.json entry: base64 of user:secret, and the
// user's email. The endpoints answer it keyed by the registry's URL, as an
// object a user pastes into ~/.docker/config.json (additional-docker-
// information, "Set Your Docker Credentials Manually").
type authEntry struct {
	Auth  string `json:"auth"`
	Email string `json:"email"`
}

// registerAuth registers .../v2/auth, a registry path. The legacy
// .../v1/auth is routed in Register, being none.
func (g *Registry) registerAuth() {
	g.handle(KindAuth, http.MethodGet, func(w http.ResponseWriter, r *http.Request, _ Request) {
		g.writeAuth(w, r, apierror.ProfileOCI)
	})
}

// serveV1Auth answers GET /artifactory/api/docker/<repo>/v1/auth, in the
// Artifactory envelope as the path is no registry path.
func (g *Registry) serveV1Auth(w http.ResponseWriter, r *http.Request) {
	g.writeAuth(w, r, apierror.ProfileArtifactory)
}

// writeAuth answers the config.json entry for the credential the request
// authenticated with, its secret as presented: a password, an access or
// reference token, or a registry token (decision 0005). Anonymous gets
// 401: it has nothing to put in the entry.
func (g *Registry) writeAuth(w http.ResponseWriter, r *http.Request, s apierror.Profile) {
	p, ok := kernel.FromContext(r.Context())
	secret := presented(r, p.Name)
	if !ok || p.Anonymous || secret == "" {
		apierror.Write(w, s, Errorf(http.StatusUnauthorized, CodeUnauthorized,
			"authentication required"))
		return
	}
	var email string
	if g.identity != nil {
		u, err := g.identity.User(r.Context(), p.Name)
		switch {
		case err == nil:
			email = u.Email
		case !errors.Is(err, kernel.ErrNotFound):
			// A user an OIDC token names need not be stored; any other
			// failure is the store's.
			if g.fault != nil {
				g.fault(r, fmt.Errorf("get user %s: %w", p.Name, err))
			}
			apierror.Write(w, s, err)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]authEntry{registryURL(r): {
		Auth:  base64.StdEncoding.EncodeToString([]byte(p.Name + ":" + secret)),
		Email: email,
	}})
}

// registryURL is the key of the config.json entry: the scheme and host of
// the base URL the request resolved, which is the host a client logs in to
// on the repository-path method, with no path. A request the base-URL
// middleware never saw, as only tests send, is keyed by its own host.
func registryURL(r *http.Request) string {
	if u, err := url.Parse(baseurl.From(r.Context())); err == nil && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// presented is the secret of the credential the request authenticated as
// name with: an API key's reference token, a Basic password or a Bearer
// token, read as the authentication reads them. An API key comes first
// (accessapi.APIKey): it authenticates a request that sends one whatever
// its Authorization header says, as 7.x's props filter does, so a Basic
// or Bearer value beside it, which nothing verified, is not the one to
// hand back. Basic and Bearer are read with accessapi.BasicCredentials and
// accessapi.BearerToken: a lower-case "basic" value, a blank "Bearer" or
// another scheme authenticates nothing in 7.x. Nor is the password of
// Basic credentials naming another user, such as the anonymous name the
// authentication declines while anonymous access is off; an empty name is
// the token's subject (authn.Authenticator.Token).
func presented(r *http.Request, name string) string {
	if key, ok := accessapi.APIKey(r.Header); ok {
		return key
	}
	h := r.Header.Get("Authorization")
	if user, password, ok := accessapi.BasicCredentials(h); ok &&
		(user == "" || accessapi.SameUsername(user, name)) {
		return password
	}
	if token, ok := accessapi.BearerToken(h); ok {
		return token
	}
	return ""
}
