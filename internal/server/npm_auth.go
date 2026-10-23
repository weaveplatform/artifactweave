package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/server/npm"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// The JFrog operations beside the npm registry that hand npm its .npmrc
// lines, GET /api/npm/auth and GET /api/npm/{repoKey}/auth/{scope}, and
// what the middleware does for npm's own login and logout (decision 0034).

const npmAPI = "/artifactory/api/npm/"

// npmUserPath reports whether escaped, a request's escaped path, is under
// a repository's /-/user/: npm's legacy login and its logout. The
// middleware reads no credentials there, and lets a request without any
// through while anonymous access is off, as 7.x lets anonymous requests
// reach .*npm/.*/-/user/.* (R25 4). npm sends a login whatever token
// .npmrc holds, which a stale one would otherwise refuse, and a logout the
// token it ends, which may have expired. Both routes check the password or
// token they carry themselves and act as no caller, so nothing is lost; and
// a stale password sent with them is not counted as a failed login, which
// could suspend the very login that replaces it.
func npmUserPath(escaped string) bool {
	rest, ok := strings.CutPrefix(escaped, npmAPI)
	if !ok {
		return false
	}
	key, after, ok := strings.Cut(rest, "/")
	return ok && key != "" && strings.HasPrefix(after, "-/user/")
}

// npmOperation reports whether path is one of the JFrog operations that
// answer npm's configuration, which answer errors in the Artifactory
// envelope, {"errors":[...]}, as 7.x does (RTFACT-30181), where the
// registry beside them answers npm's.
func npmOperation(path string) bool {
	rest, ok := strings.CutPrefix(path, npmAPI)
	if !ok {
		return false
	}
	segs := strings.Split(rest, "/")
	return rest == "auth" || len(segs) == 3 && segs[0] != "" && segs[1] == "auth" && segs[2] != ""
}

// loggedPath is path as the access log records it: the token a logout's
// path names is a credential, which must not reach the log. A token that
// cannot be revoked outlives the logout, and the log outlives both.
func loggedPath(path string) string {
	if rest, ok := strings.CutPrefix(path, "/artifactory/api/luarocks/"); ok {
		parts := strings.Split(rest, "/")
		if len(parts) >= 5 && parts[1] == "api" && parts[2] == "1" {
			parts[3] = "[redacted]"
			return "/artifactory/api/luarocks/" + strings.Join(parts, "/")
		}
	}
	const marker = "/-/user/token/"
	if i := strings.Index(path, marker); i >= 0 && strings.HasPrefix(path, npmAPI) {
		return path[:i+len(marker)] + "[redacted]"
	}
	return path
}

type npmCredentialKey struct{}

// keepNPMCredential keeps, for the npm configuration operations alone, the
// token a request authenticated with, which they write into .npmrc as it
// came. Nothing else needs the raw token, so no other request carries it
// further than the middleware.
func keepNPMCredential(ctx context.Context, r *http.Request, p auth.Principal) context.Context {
	if p.TokenID == "" || !npmOperation(r.URL.Path) {
		return ctx
	}
	var c npm.Credential
	if user, password, ok := r.BasicAuth(); ok {
		c.User, c.Password = user, password
	} else if scheme, raw, _ := strings.Cut(r.Header.Get("Authorization"), " "); strings.EqualFold(scheme, "Bearer") {
		c.Token = strings.TrimSpace(raw)
	} else {
		c.Token = r.Header.Get("X-JFrog-Art-Api")
	}
	return context.WithValue(ctx, npmCredentialKey{}, c)
}

// npmCredential is what the caller's .npmrc lines carry. A caller that
// sent its password gets its encrypted password in its place (DV-004), so
// the password itself is never written back: 7.x's documentation shows
// _auth decoding to user:<encrypted password>. A caller that sent a token
// gets that token back, as it sent it, Basic or Bearer; it is the
// credential the caller already holds, so nothing new is handed out.
// [I] What 7.x answers a token caller is undocumented (#216).
func (s artifactoryService) npmCredential(ctx context.Context) (npm.Credential, error) {
	p, err := auth.RequireAuthenticated(ctx)
	if err != nil {
		return npm.Credential{}, err //nolint:wrapcheck // an apierror value
	}
	if p.TokenID != "" {
		c, ok := ctx.Value(npmCredentialKey{}).(npm.Credential)
		if !ok {
			return npm.Credential{}, auth.ErrUnauthenticated
		}
		return c, nil
	}
	if !p.Password {
		return npm.Credential{}, auth.ErrUnauthenticated
	}
	if s.cfg.Tokens == nil {
		return npm.Credential{}, apierror.ErrNotImplemented
	}
	secret, err := s.cfg.Tokens.EncryptedPassword(ctx, p.Name)
	if err != nil {
		return npm.Credential{}, s.internalError(ctx, "npm encrypted password", err)
	}
	return npm.Credential{User: p.Name, Password: secret}, nil
}

// GetNpmAuth answers .npmrc lines that authenticate npm as the caller.
func (s artifactoryService) GetNpmAuth(
	ctx context.Context,
	_ artgen.GetNpmAuthRequestObject,
) (artgen.GetNpmAuthResponseObject, error) {
	c, err := s.npmCredential(ctx)
	if err != nil {
		return nil, err
	}
	return artgen.GetNpmAuth200TextResponse(npm.AuthLines(c)), nil
}

// GetNpmScopeAuth answers .npmrc lines that point an npm scope at a
// repository and authenticate npm to it as the caller. Any npm repository
// may be named, whatever its class. The scope comes without its @ (R25 4);
// one sent with it is taken as meant.
func (s artifactoryService) GetNpmScopeAuth(
	ctx context.Context,
	req artgen.GetNpmScopeAuthRequestObject,
) (artgen.GetNpmScopeAuthResponseObject, error) {
	c, err := s.npmCredential(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	rc, cached, err := resolveRepo(ctx, st, req.RepoKey)
	switch {
	case err != nil:
		return nil, s.itemError(ctx, "resolve repository", err)
	case cached || rc.PackageType != repo.NPM:
		return nil, badRequest("Repository '%s' is not an npm repository", req.RepoKey)
	}
	scope := strings.TrimPrefix(req.Scope, "@")
	if !npm.ValidScope(scope) {
		return nil, badRequest("'%s' is not an npm scope", req.Scope)
	}
	registry := baseurl.From(ctx) + npmAPI + url.PathEscape(rc.Key) + "/"
	return artgen.GetNpmScopeAuth200TextResponse(npm.ScopeAuthLines(scope, registry, c)), nil
}
