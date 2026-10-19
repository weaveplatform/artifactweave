package npm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Accounts is what npm's legacy login and logout need from the server: the
// authenticator that checks every other password login, and the token
// service (decision 0005).
type Accounts struct {
	// Passwords verifies the user name and password a login sends. Nil
	// answers login 501.
	Passwords Passwords
	// Tokens issues a login's token and revokes the one a logout names.
	// Nil answers login 501, and logout revokes nothing.
	Tokens Tokens
	// TokenExpiry is how long a login's token lasts
	// (npm.login_token_expiry); zero means DefaultLoginTokenExpiry.
	TokenExpiry time.Duration
}

// Passwords verifies credentials as the server's middleware does
// (*auth.Authenticator).
type Passwords interface {
	Basic(ctx context.Context, name, password string) (auth.Principal, error)
	Token(ctx context.Context, raw, user string) (auth.Principal, error)
}

// Tokens issues and revokes access tokens (*token.Service).
type Tokens interface {
	Issue(ctx context.Context, r token.Request) (token.Issued, error)
	RevokeValue(ctx context.Context, raw string) error
}

// DefaultLoginTokenExpiry is decision 0034's default npm.login_token_expiry:
// the month a token from npm login lasts on 7.x (KB 000006713).
const DefaultLoginTokenExpiry = 30 * 24 * time.Hour

// couchUser starts the last segment of a legacy login's path,
// /-/user/org.couchdb.user:<name>, as npm-profile's couchEndpoint builds it.
const couchUser = "org.couchdb.user:"

// loginBodyMax bounds a login body. npm sends a user name, a password and a
// date; nothing larger is a login.
const loginBodyMax = 64 << 10

// loginBody is the part of npm-profile's loginCouch body the server reads;
// it also sends _id, type, roles and date.
type loginBody struct {
	Name     *string `json:"name"`
	Password string  `json:"password"`
}

// loginAnswer is what a legacy login answers. npm reads token alone.
type loginAnswer struct {
	OK    bool   `json:"ok"`
	ID    string `json:"id"`
	Token string `json:"token"`
}

// login is npm's legacy login (decision 0034): the password is checked as
// every password login checks it, and the answer carries a new access token
// with the user's own permissions. 201, as 7.x answers it (KB 000004147).
//
// A refused password is 401 or 403, never 400 or 409: npm reports a 400 as
// "no such user", which would tell which names exist, and reads a 409 as an
// account to update, which nothing here serves. The middleware reads no
// credentials on this route, since npm sends whatever stale token .npmrc
// holds.
func (h handler) login(w http.ResponseWriter, r *http.Request) {
	a := h.accounts
	if a.Passwords == nil || a.Tokens == nil {
		h.k.Error(w, r, apierror.ErrNotImplemented)
		return
	}
	// chi matched the escaped path, whose escapes the server checked when it
	// parsed the request, so unescaping cannot fail; nor is the name empty,
	// which no route matches.
	name, _ := url.PathUnescape(chi.URLParam(r, "user"))
	var b loginBody
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, loginBodyMax))
	if err := dec.Decode(&b); err != nil {
		h.k.Error(w, r, loginBodyError(err))
		return
	}
	if b.Name == nil || *b.Name != name {
		h.k.Error(w, r, badRequest("the body's name must be the user the path names"))
		return
	}
	p, err := a.verify(r.Context(), name, b.Password)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	expiry := a.TokenExpiry
	issued, err := a.Tokens.Issue(r.Context(), token.Request{
		Username: p.Name, ExpiresIn: &expiry, Description: "npm login",
		// Logout must be able to end it, whatever the expiry: a token
		// shorter than the revocable threshold would otherwise outlive
		// npm logout, which reports success either way.
		ForceRevocable: true,
	})
	if err != nil {
		h.k.Error(w, r, fmt.Errorf("npm: login token for %s: %w", p.Name, err))
		return
	}
	writeJSON(w, http.StatusCreated, jsonType,
		loginAnswer{OK: true, ID: couchUser + p.Name, Token: issued.AccessToken})
}

// verify checks a login's password with the server's own checks: the basic
// authentication switch, the lock, the temporary suspension, the password
// expiry and the failed-login count all apply as to a Basic login.
// token.allow_basic_auth does not: it governs the Access token API only,
// and the legacy token route that takes a password stays open the same way
// (decision 0005). [I] That 7.x does not hold npm login to it is on #216;
// holding it would break npm login on every default instance. A token sent
// as the password is checked as a Basic
// password that is a token is, and logs in only as the stored user it acts
// as: a token narrowed to groups or resources, a registry token or a
// transient user's would otherwise obtain a broader credential.
func (a Accounts) verify(ctx context.Context, name, password string) (auth.Principal, error) {
	if !auth.LooksLikeToken(password) {
		return a.Passwords.Basic(ctx, name, password) //nolint:wrapcheck // apierror values
	}
	p, err := a.Passwords.Token(ctx, password, name)
	if err != nil {
		return auth.Principal{}, err //nolint:wrapcheck // apierror values
	}
	if p.Registry != nil || p.Identity != types.IdentityStored || !wholeIdentity(p.Scope) {
		return auth.Principal{}, auth.ErrBadCredentials
	}
	return p, nil
}

// wholeIdentity reports whether a token's scope acts as its user or as an
// administrator, rather than narrowing to groups or resources.
func wholeIdentity(s types.Scope) bool {
	for _, e := range s.Entries {
		if e.Kind == types.ScopeUser || e.Kind == types.ScopeAdmin {
			return true
		}
	}
	return false
}

// loginBodyError is a body that could not be read: 413 past loginBodyMax,
// 400 otherwise.
func loginBodyError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return apierror.New(http.StatusRequestEntityTooLarge,
			fmt.Sprintf("a login body is at most %d bytes", tooLarge.Limit))
	}
	return badRequest(fmt.Sprintf("the login body is not a JSON object: %v", err))
}

// logout is npm logout (decision 0034): it revokes the token the path names,
// since holding it proves the right to end it, and answers 200 whatever
// it finds. npm keeps a token in .npmrc unless logout succeeds, and a token
// already gone, or one that cannot be revoked, leaves nothing for it to
// do. The request needs no credentials of its own: npm sends the token
// being ended, which may have expired.
func (h handler) logout(w http.ResponseWriter, r *http.Request) {
	// As for login, the escapes are checked already and the token is never
	// empty.
	raw, _ := url.PathUnescape(chi.URLParam(r, "token"))
	if h.accounts.Tokens != nil {
		err := h.accounts.Tokens.RevokeValue(r.Context(), raw)
		switch {
		case err == nil, errors.Is(err, store.ErrNotFound), errors.Is(err, token.ErrNotRevocable):
		default:
			h.k.Error(w, r, fmt.Errorf("npm: logout: %w", err))
			return
		}
	}
	writeJSON(w, http.StatusOK, jsonType, map[string]bool{"ok": true})
}
