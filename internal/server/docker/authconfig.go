package docker

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/pkg/store"
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
		g.writeAuth(w, r, apierror.OCI)
	})
}

// serveV1Auth answers GET /artifactory/api/docker/<repo>/v1/auth, in the
// Artifactory envelope as the path is no registry path.
func (g *Registry) serveV1Auth(w http.ResponseWriter, r *http.Request) {
	g.writeAuth(w, r, apierror.Artifactory)
}

// writeAuth answers the config.json entry for the credential the request
// authenticated with, its secret as presented: a password, an access or
// reference token, or a registry token (decision 0005). Anonymous gets
// 401: it has nothing to put in the entry.
func (g *Registry) writeAuth(w http.ResponseWriter, r *http.Request, s apierror.Surface) {
	p, ok := auth.FromContext(r.Context())
	secret := presented(r)
	if !ok || p.Anonymous || secret == "" {
		apierror.Write(w, s, Errorf(http.StatusUnauthorized, CodeUnauthorized,
			"authentication required"))
		return
	}
	var email string
	if g.store != nil {
		u, err := g.store.Users().Get(r.Context(), p.Name)
		switch {
		case err == nil:
			email = u.Email
		case !errors.Is(err, store.ErrNotFound):
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

// presented is the secret of the request's credential: a Basic password,
// a Bearer token or an X-JFrog-Art-Api reference token.
func presented(r *http.Request) string {
	if _, password, ok := r.BasicAuth(); ok {
		return password
	}
	if scheme, raw, ok := strings.Cut(r.Header.Get("Authorization"), " "); ok &&
		strings.EqualFold(scheme, "Bearer") {
		return strings.TrimSpace(raw)
	}
	return r.Header.Get("X-JFrog-Art-Api")
}
