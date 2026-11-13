package npmhttp

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
)

// The JFrog operations beside the npm registry that hand npm its .npmrc
// lines, GET /api/npm/auth and GET /api/npm/{repoKey}/auth/{scope}, and
// what the middleware does for npm's own login and logout (decision 0034).

const npmAPIPrefix = "/artifactory/api/npm/"

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
	rest, ok := strings.CutPrefix(escaped, npmAPIPrefix)
	if !ok {
		return false
	}
	key, after, ok := strings.Cut(rest, "/")
	return ok && key != "" && strings.HasPrefix(after, "-/user/")
}

// NPMUserRequest is npmUserPath of r's escaped path, the skip
// accesshttp.Authenticate asks first.
func NPMUserRequest(r *http.Request) bool { return npmUserPath(r.URL.EscapedPath()) }

// NPMOperation reports whether path is one of the JFrog operations that
// answer npm's configuration, which answer errors in the Artifactory
// envelope, {"errors":[...]}, as 7.x does (RTFACT-30181), where the
// registry beside them answers npm's.
func NPMOperation(path string) bool {
	rest, ok := strings.CutPrefix(path, npmAPIPrefix)
	if !ok {
		return false
	}
	segs := strings.Split(rest, "/")
	return rest == "auth" || len(segs) == 3 && segs[0] != "" && segs[1] == "auth" && segs[2] != ""
}

// NPMLoggedPath is path as the access log records it: the token a
// logout's path names is a credential, which must not reach the log. A
// token that cannot be revoked outlives the logout, and the log outlives
// both. Any other path comes back unchanged.
func NPMLoggedPath(path string) string {
	const marker = "/-/user/token/"
	if i := strings.Index(path, marker); i >= 0 && strings.HasPrefix(path, npmAPIPrefix) {
		return path[:i+len(marker)] + "[redacted]"
	}
	return path
}

type npmCredentialKey struct{}

// KeepNPMCredential keeps, for the npm configuration operations alone, the
// token a request authenticated with, which they write into .npmrc as it
// came. Nothing else needs the raw token, so no other request carries it
// further than the middleware. It reads Basic and Bearer as the middleware
// does (accessapi.BasicCredentials, accessapi.BearerToken), not with
// Request.BasicAuth: a lower-case "basic" value, a blank "Bearer" or
// another scheme authenticates nothing in 7.x, so beside a key that did,
// it must not be written into .npmrc in the key's place. Nor must Basic
// credentials naming another user, such as the anonymous name the
// middleware declines while anonymous access is off; an empty name is the
// token's subject (authn.Authenticator.Token).
func KeepNPMCredential(ctx context.Context, r *http.Request, p kernel.Principal) context.Context {
	if p.TokenID == "" || !NPMOperation(r.URL.Path) {
		return ctx
	}
	var c Credential
	h := r.Header.Get("Authorization")
	if user, password, ok := accessapi.BasicCredentials(h); ok &&
		(user == "" || accessapi.SameUsername(user, p.Name)) {
		c.User, c.Password = user, password
	} else if token, ok := accessapi.BearerToken(h); ok {
		c.Token = token
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
func (s NPMAPI) npmCredential(ctx context.Context) (Credential, error) {
	p, err := kernel.RequireAuthenticated(ctx)
	if err != nil {
		return Credential{}, err //nolint:wrapcheck // an apierror value
	}
	if p.TokenID != "" {
		c, ok := ctx.Value(npmCredentialKey{}).(Credential)
		if !ok {
			return Credential{}, kernel.ErrUnauthenticated
		}
		return c, nil
	}
	if !p.Password {
		return Credential{}, kernel.ErrUnauthenticated
	}
	if s.Access.Tokens == nil {
		return Credential{}, apierror.ErrNotImplemented
	}
	secret, err := s.Access.Tokens.EncryptedPassword(ctx, p.Name)
	if err != nil {
		return Credential{}, s.InternalError(ctx, "npm encrypted password", err)
	}
	return Credential{User: p.Name, Password: secret}, nil
}

// GetNpmAuth answers .npmrc lines that authenticate npm as the caller.
func (s NPMAPI) GetNpmAuth(
	ctx context.Context,
	_ artgen.GetNpmAuthRequestObject,
) (artgen.GetNpmAuthResponseObject, error) {
	c, err := s.npmCredential(ctx)
	if err != nil {
		return nil, err
	}
	return artgen.GetNpmAuth200TextResponse(AuthLines(c)), nil
}

// GetNpmScopeAuth answers .npmrc lines that point an npm scope at a
// repository and authenticate npm to it as the caller. Any npm repository
// may be named, whatever its class. The scope comes without its @ (R25 4);
// one sent with it is taken as meant.
func (s NPMAPI) GetNpmScopeAuth(
	ctx context.Context,
	req artgen.GetNpmScopeAuthRequestObject,
) (artgen.GetNpmScopeAuthResponseObject, error) {
	c, err := s.npmCredential(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.Content()
	if err != nil {
		return nil, err
	}
	rc, cached, err := itemkit.ResolveRepo(ctx, st, req.RepoKey)
	switch {
	case err != nil:
		return nil, s.ItemError(ctx, "resolve repository", err)
	case cached || rc.PackageType != repo.NPM:
		return nil, httpkit.BadRequestf("Repository '%s' is not an npm repository", req.RepoKey)
	}
	scope := strings.TrimPrefix(req.Scope, "@")
	if !ValidScope(scope) {
		return nil, httpkit.BadRequestf("'%s' is not an npm scope", req.Scope)
	}
	registry := baseurl.From(ctx) + npmAPIPrefix + url.PathEscape(rc.Key) + "/"
	return artgen.GetNpmScopeAuth200TextResponse(ScopeAuthLines(scope, registry, c)), nil
}
