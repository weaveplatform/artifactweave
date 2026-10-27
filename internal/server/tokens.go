package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	accessgen "github.com/weaveplatform/artifactweave/internal/server/gen/access"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The Access token API (#79) and Artifactory's legacy token endpoints, over
// the token service (internal/auth/token). Both surfaces share mint and the
// visibility rules, so a token issued through one is the same token in the
// other.

const (
	legacyTokenPath       = "/artifactory/api/security/token"
	legacyTokenRevokePath = "/artifactory/api/security/token/revoke"
	grantClient           = "client_credentials"
	grantRefresh          = "refresh_token"
)

// tokenAPI is what both surfaces need from the configuration.
type tokenAPI struct{ cfg Config }

func (t tokenAPI) service() (*token.Service, error) {
	if t.cfg.Tokens == nil {
		return nil, apierror.ErrNotImplemented
	}
	return t.cfg.Tokens, nil
}

func (t tokenAPI) internalError(ctx context.Context, op string, err error) error {
	return artifactoryService{cfg: t.cfg}.internalError(ctx, op, err)
}

// subject is the token subject of a user name on this instance.
func subjectOf(svc *token.Service, name string) string {
	return svc.Issuer() + "/users/" + name
}

// mintRequest is a create or refresh request from either surface.
type mintRequest struct {
	grant            string
	username         string
	scope            string
	expiresIn        *int
	refreshable      bool
	forceRevocable   bool
	includeReference bool
	audience         string
	description      string
	projectKey       string
	refreshToken     string
	// accessToken is the legacy refresh form's access_token, the token being
	// refreshed.
	accessToken string
	// legacy translates the legacy scope forms first, and makes a refresh
	// present the token it refreshes as accessToken.
	legacy bool
}

// mint issues or refreshes a token for the caller p.
func (t tokenAPI) mint(ctx context.Context, m mintRequest) (token.Issued, error) {
	svc, err := t.service()
	if err != nil {
		return token.Issued{}, err
	}
	switch m.grant {
	case "", grantClient:
	case grantRefresh:
		return t.refresh(ctx, svc, m)
	default:
		return token.Issued{}, badRequestf("unsupported grant_type %q", m.grant)
	}
	p, err := auth.RequireAuthenticated(ctx)
	if err != nil {
		return token.Issued{}, fmt.Errorf("tokens: %w", err)
	}
	// Only the Access route honours token.allow_basic_auth. The legacy route
	// is Artifactory's own, which in 7.x authenticates the caller itself and
	// asks Access for the token as a service: jf c add with a password swaps
	// it for a token there (jfrog-cli-core utils/config/tokenrefresh.go), and
	// jf rt atc with a password still works where jf atc cannot (jfrog-cli
	// issue 2547). Refusing it too would break every such configuration.
	if p.Password && !m.legacy && !t.cfg.AllowBasicAuthTokens {
		return token.Issued{}, errBasicTokenCreation
	}
	if narrowedToken(p) {
		return token.Issued{}, apierror.New(http.StatusForbidden,
			"A token with a narrowed scope cannot create tokens")
	}
	username := m.username
	if username == "" {
		username = p.Name
	}
	if username != p.Name && !p.Admin {
		return token.Issued{}, apierror.New(http.StatusForbidden,
			"Only an administrator can create tokens for other users")
	}
	req := token.Request{
		Username: username, Refreshable: m.refreshable, ForceRevocable: m.forceRevocable,
		Audience: m.audience, Description: m.description, ProjectKey: m.projectKey,
		IncludeReference: m.includeReference, ByAdmin: p.Admin,
	}
	if p.Admin {
		if req.Identity, err = t.identityOf(ctx, username); err != nil {
			return token.Issued{}, err
		}
	}
	if m.scope != "" {
		raw := m.scope
		if m.legacy {
			raw = legacyScope(raw)
		}
		if req.Scope, err = types.ParseScope(raw); err != nil {
			return token.Issued{}, badRequestf("%v", err)
		}
		if !p.Admin && !onlyUserScope(req.Scope) {
			return token.Issued{}, apierror.New(
				http.StatusForbidden,
				"Only an administrator can create tokens with a scope other than applied-permissions/user",
			)
		}
	}
	switch {
	case m.expiresIn != nil:
		if *m.expiresIn < 0 {
			return token.Issued{}, badRequestf("expires_in must not be negative")
		}
		d := time.Duration(*m.expiresIn) * time.Second
		req.ExpiresIn = &d
	case m.legacy:
		// The legacy route has its own default, so the year-long
		// token.default_expiry of the Access route does not reach it.
		d := types.LegacyTokenExpiry
		req.ExpiresIn = &d
	}
	if !p.Admin && p.Identity != types.IdentityStored {
		if err := t.asTransientCaller(ctx, p, &req); err != nil {
			return token.Issued{}, err
		}
	}
	issued, err := svc.Issue(ctx, req)
	switch {
	case errors.Is(err, token.ErrNeverExpiresAdminOnly):
		return token.Issued{}, errNeverExpires
	case errors.Is(err, token.ErrInvalidRequest):
		return token.Issued{}, badRequestf("%v", err)
	}
	if err != nil {
		return token.Issued{}, t.internalError(ctx, "issue token", err)
	}
	return issued, nil
}

// refresh exchanges m's refresh token. The refresh token is the credential:
// a random secret bound to one token, so the routes let a refresh through
// without others. Who else the request names narrows it:
//   - an authenticated caller must be the refreshed token's user, or 403
//     (createtoken.json: "The authenticated user must match the user of the
//     access token being refreshed");
//   - the token the request presents as being refreshed (the expired Bearer
//     token on the Access route, the form's access_token on the legacy one)
//     must be the refresh token's: another user's is 403 as above, and
//     another token of the same user answers as an invalid refresh token,
//     so the pair is never confirmed.
//
// An unknown, expired, revoked or malformed refresh token answers 400, the
// analogue of OAuth's invalid_grant; Access documents no status for it.
func (t tokenAPI) refresh(
	ctx context.Context,
	svc *token.Service,
	m mintRequest,
) (token.Issued, error) {
	if m.refreshToken == "" {
		return token.Issued{}, badRequestf(
			"refresh_token is required with grant_type refresh_token",
		)
	}
	var check token.RefreshCheck
	if p, ok := auth.FromContext(ctx); ok && !p.Anonymous {
		check.Username = p.Name
	}
	of, presented := auth.ExpiredTokenFrom(ctx)
	if m.legacy {
		// jfrog-client-go sends the legacy refresh with no credentials and the
		// token being refreshed, possibly expired, as access_token
		// (artifactory/services/security.go, buildRefreshTokenUrlValues).
		if m.accessToken == "" {
			return token.Issued{}, badRequestf(
				"access_token is required with grant_type refresh_token",
			)
		}
		if t.cfg.Auth != nil {
			of, presented = t.cfg.Auth.ExpiredToken(ctx, m.accessToken)
		}
		if !presented {
			return token.Issued{}, auth.ErrBadCredentials
		}
	}
	if presented {
		if check.Username != "" && check.Username != of.User {
			return token.Issued{}, errRefreshOtherUser
		}
		check = token.RefreshCheck{Username: of.User, TokenID: of.TokenID}
	}
	issued, err := svc.RefreshAs(ctx, m.refreshToken, check)
	switch {
	case errors.Is(err, token.ErrRefreshOtherUser):
		return token.Issued{}, errRefreshOtherUser
	case errors.Is(err, auth.ErrInvalidToken):
		return token.Issued{}, badRequestf("The refresh token is not valid")
	case err != nil:
		return token.Issued{}, t.internalError(ctx, "refresh token", err)
	}
	return issued, nil
}

var errRefreshOtherUser = apierror.New(http.StatusForbidden,
	"The authenticated user must match the user of the access token being refreshed")

// errNeverExpires answers a non-administrator's token that would never
// expire. createtoken documents 403, "The requested token details are
// forbidden", for an expires_in the caller may not have, and marks 0 as
// admin only. Whether 7.x refuses it at all while token.max-expiry is 0 is
// on #216.
var errNeverExpires = apierror.New(
	http.StatusForbidden,
	"The requested token details are forbidden: only an administrator can create a token that never expires",
)

// identityOf is how a token an administrator mints for username resolves
// to a user: stored when username is a stored user, otherwise transient.
// Access lets an administrator name a user that does not exist, and such a
// token authenticates as a transient user, held in memory for the request
// and never saved, with the permissions its scope gives (JFrog's
// access-tokens page, "Support Authentication for Non-existing Users").
// Only an administrator's token is marked: anyone else mints for
// themselves alone.
func (t tokenAPI) identityOf(ctx context.Context, username string) (types.TokenIdentity, error) {
	_, err := t.cfg.Store.Users().Get(ctx, username)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return types.IdentityTransient, nil
	case err != nil:
		return "", t.internalError(ctx, "token user", err)
	}
	return types.IdentityStored, nil
}

// asTransientCaller shapes the token a transient user, from an OIDC exchange
// or an administrator's token for a name that is no stored user, mints for
// itself (#456). Issued for a stored user, as anyone else's is, the token
// would answer 401 on every use, since no such user is stored. It carries
// the caller's identity marker and scope instead, so it resolves to the same
// user with the same groups and acts exactly as the caller does; the one
// scope a non-administrator may ask for, applied-permissions/user, means
// "as I am". It never gains more than the caller holds: it expires no later
// than the caller's token, and is refreshable only when that token is,
// since each refresh would otherwise extend an identity that ends with the
// caller's token.
func (t tokenAPI) asTransientCaller(
	ctx context.Context,
	p auth.Principal,
	req *token.Request,
) error {
	req.Identity, req.Scope, req.NotAfter = p.Identity, p.Scope, p.TokenExpires
	if !req.Refreshable {
		return nil
	}
	caller, err := t.cfg.Store.Tokens().Get(ctx, p.TokenID)
	if err != nil {
		return t.internalError(ctx, "the caller's token", err)
	}
	if !caller.Refreshable {
		return errTransientRefreshable
	}
	return nil
}

// errTransientRefreshable answers a transient user asking for a refreshable
// token while its own token is not refreshable, with the status and message
// createtoken gives token details the caller may not have.
var errTransientRefreshable = apierror.New(
	http.StatusForbidden,
	"The requested token details are forbidden: a transient user's token can be refreshable only when the token that creates it is",
)

// errBasicTokenCreation answers a password sent to the Access token API
// while token.allow_basic_auth is off. The message is the one Access gives
// Basic credentials it does not accept, with the error code UNAUTHORIZED
// (JFrog KB, "What causes the error message 'Unsupported authentication
// method Basic' to appear when using a REST API?"); that this switch answers
// with it, and with 401, is inferred and listed on #216.
var errBasicTokenCreation = apierror.New(http.StatusUnauthorized,
	"Unsupported authentication method Basic")

// narrowedToken reports whether p presented a token whose scope is narrower
// than its user's identity. Such a token must not mint a broader credential,
// a token or a password: a resource- or group-scoped token doing so would
// escalate past its own scope.
func narrowedToken(p auth.Principal) bool { return p.TokenID != "" && !identityScope(p.Scope) }

// identityScope reports whether a scope acts as its user or as an
// administrator, rather than narrowing to groups or resources.
func identityScope(s types.Scope) bool {
	for _, e := range s.Entries {
		if e.Kind == types.ScopeUser || e.Kind == types.ScopeAdmin {
			return true
		}
	}
	return false
}

// onlyUserScope is the one scope a non-administrator may request.
func onlyUserScope(s types.Scope) bool {
	return len(s.Entries) == 1 && s.Entries[0].Kind == types.ScopeUser
}

// legacyScope translates the legacy token scope forms to Access ones:
// member-of-groups:<g,...> is applied-permissions/groups, api:* the
// caller's own identity and jfrt@<id>:admin applied-permissions/admin.
// member-of-groups:* is the user's own identity too: Artifactory
// authenticates such a token as a copy of the real user, admin flag and
// direct permissions included (OSS 6.20 AccessTokenAuthenticationProvider,
// populateGroups), and the JFrog CLI asks for exactly that scope when it
// swaps a configured user's password for a token (jfrog-cli-core
// utils/config/tokenrefresh.go). As groups only, an administrator's jf
// configuration could upload nothing.
func legacyScope(s string) string {
	parts := strings.Fields(s)
	for i, p := range parts {
		switch {
		case p == "member-of-groups:*":
			parts[i] = "applied-permissions/user"
		case strings.HasPrefix(p, "member-of-groups:"):
			parts[i] = "applied-permissions/groups:" + strings.TrimPrefix(p, "member-of-groups:")
		case p == "api:*":
			parts[i] = "applied-permissions/user"
		case strings.HasPrefix(p, "jfrt@") && strings.HasSuffix(p, ":admin"):
			parts[i] = "applied-permissions/admin"
		}
	}
	return strings.Join(parts, " ")
}

// visible lists the tokens the caller may see: every token for an
// administrator, their own otherwise. Expired tokens are omitted, and so are
// non-revocable ones, which Artifactory keeps no record of (see #79); a
// refreshable token is revocable, and so listed.
func (t tokenAPI) visible(ctx context.Context) ([]types.Token, *token.Service, error) {
	svc, err := t.service()
	if err != nil {
		return nil, nil, err
	}
	p, err := auth.RequireAuthenticated(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("tokens: %w", err)
	}
	var filter store.TokenFilter
	if !p.Admin {
		filter.Subject = subjectOf(svc, p.Name)
	}
	now := time.Now()
	var out []types.Token
	page := store.Page{Limit: 500}
	for {
		recs, next, err := t.cfg.Store.Tokens().List(ctx, filter, page)
		if err != nil {
			return nil, nil, t.internalError(ctx, "list tokens", err)
		}
		for _, r := range recs {
			if r.Revocable && !r.Expired(now) {
				out = append(out, r)
			}
		}
		if next == "" {
			return out, svc, nil
		}
		page.After = next
	}
}

// own resolves a token id the caller may act on: me is the caller's own
// token; another user's token is 403 unless the caller is an administrator;
// a missing or expired one is 404.
func (t tokenAPI) own(ctx context.Context, id string) (types.Token, *token.Service, error) {
	svc, err := t.service()
	if err != nil {
		return types.Token{}, nil, err
	}
	p, err := auth.RequireAuthenticated(ctx)
	if err != nil {
		return types.Token{}, nil, fmt.Errorf("tokens: %w", err)
	}
	if id == "me" {
		id = p.TokenID
	}
	if id == "" {
		return types.Token{}, nil, tokenNotFound()
	}
	rec, err := t.cfg.Store.Tokens().Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && rec.Expired(time.Now())) {
		return types.Token{}, nil, tokenNotFound()
	}
	if err != nil {
		return types.Token{}, nil, t.internalError(ctx, "get token", err)
	}
	if rec.Subject != subjectOf(svc, p.Name) && !p.Admin {
		return types.Token{}, nil, apierror.New(http.StatusForbidden,
			"Only an administrator can manage other users' tokens")
	}
	return rec, svc, nil
}

func tokenNotFound() error {
	return apierror.New(http.StatusNotFound, "Token not found")
}

// errNotRevocable is revocation by value's one 404, in revoketokenbyvalue's
// words.
var errNotRevocable = apierror.New(http.StatusNotFound, "Token not found or not revocable")

func (s accessService) tokenAPI() tokenAPI { return tokenAPI{cfg: s.cfg} }

// CreateToken mints or refreshes a token. The body may be JSON (the
// Terraform provider) or a form (the JFrog CLI); both carry the same fields.
func (s accessService) CreateToken(
	ctx context.Context,
	req accessgen.CreateTokenRequestObject,
) (accessgen.CreateTokenResponseObject, error) {
	body := req.JSONBody
	if body == nil {
		body = req.FormdataBody
	}
	if body == nil {
		return nil, badRequestf("a token request body is required")
	}
	m := mintRequest{
		username: deref(body.Username), scope: deref(body.Scope), expiresIn: body.ExpiresIn,
		refreshable: deref(body.Refreshable), forceRevocable: deref(body.ForceRevocable),
		includeReference: deref(body.IncludeReferenceToken), audience: deref(body.Audience),
		description: deref(body.Description), projectKey: deref(body.ProjectKey),
		refreshToken: deref(body.RefreshToken),
	}
	if body.GrantType != nil {
		m.grant = string(*body.GrantType)
	}
	issued, err := s.tokenAPI().mint(ctx, m)
	if err != nil {
		return nil, err
	}
	out := accessgen.CreateToken200JSONResponse{
		TokenId: issued.Record.ID, AccessToken: issued.AccessToken,
		ExpiresIn: int(issued.ExpiresIn), Scope: issued.Record.Scope.String(), TokenType: "Bearer",
	}
	if issued.RefreshToken != "" {
		out.RefreshToken = &issued.RefreshToken
	}
	if issued.ReferenceToken != "" {
		out.ReferenceToken = &issued.ReferenceToken
	}
	return out, nil
}

// GetTokens lists the caller's tokens, or every token for an administrator,
// keeping only those with the scope the scope parameter names.
func (s accessService) GetTokens(
	ctx context.Context,
	req accessgen.GetTokensRequestObject,
) (accessgen.GetTokensResponseObject, error) {
	recs, svc, err := s.tokenAPI().visible(ctx)
	if err != nil {
		return nil, err
	}
	out := accessgen.GetTokens200JSONResponse{Tokens: make([]accessgen.TokenDetails, 0, len(recs))}
	for _, r := range recs {
		if req.Params.Scope != nil && !hasScope(r.Scope, *req.Params.Scope) {
			continue
		}
		out.Tokens = append(out.Tokens, tokenDetails(svc, r))
	}
	return out, nil
}

// hasScope reports whether sc is want, whole or as one of its
// space-separated scopes. JFrog documents the filter (7.133.3) only as
// returning "token results for a specific scope, such as group"; matching
// either form exactly is the literal reading, recorded as [I] on #216.
func hasScope(sc types.Scope, want string) bool {
	return sc.String() == want ||
		slices.ContainsFunc(sc.Entries, func(e types.ScopeEntry) bool { return e.String() == want })
}

// GetTokenById returns one token's metadata. A non-revocable token is still
// found by its id: the artifactory_scoped_token resource reads its token
// back after creating it, and drops it from state on a 404.
func (s accessService) GetTokenById(
	ctx context.Context,
	req accessgen.GetTokenByIdRequestObject,
) (accessgen.GetTokenByIdResponseObject, error) {
	rec, svc, err := s.tokenAPI().own(ctx, req.TokenId)
	if err != nil {
		return nil, err
	}
	return accessgen.GetTokenById200JSONResponse(tokenDetails(svc, rec)), nil
}

// RevokeTokenById revokes a token. A non-revocable one is 400, as 7.x
// answers it (JFrog KB 000005622, which shows the UI's DELETE of such a
// token failing with 400); revoketokenbyid lists 403 only for a caller
// without the permission.
func (s accessService) RevokeTokenById(
	ctx context.Context,
	req accessgen.RevokeTokenByIdRequestObject,
) (accessgen.RevokeTokenByIdResponseObject, error) {
	t := s.tokenAPI()
	rec, svc, err := t.own(ctx, req.TokenId)
	if err != nil {
		return nil, err
	}
	switch err := svc.Revoke(ctx, rec.ID); {
	case errors.Is(err, token.ErrNotRevocable):
		return nil, badRequestf("Token is not revocable")
	case errors.Is(err, store.ErrNotFound):
		return nil, tokenNotFound()
	case err != nil:
		return nil, t.internalError(ctx, "revoke token", err)
	}
	return accessgen.RevokeTokenById200Response{}, nil
}

// RevokeTokenByValue revokes the token whose JWT or reference token is
// given. Admin only. An unknown token and a non-revocable one both answer
// 404, which revoketokenbyvalue documents as "Token not found or not
// revocable", so the answer does not tell which.
func (s accessService) RevokeTokenByValue(
	ctx context.Context,
	req accessgen.RevokeTokenByValueRequestObject,
) (accessgen.RevokeTokenByValueResponseObject, error) {
	t := s.tokenAPI()
	svc, err := t.service()
	if err != nil {
		return nil, err
	}
	if err := adminOnly(ctx); err != nil {
		return nil, err
	}
	if req.Body == nil || deref(req.Body.Token) == "" {
		return nil, badRequestf("token is required")
	}
	// Verify resolves either form and refuses expired and revoked tokens,
	// which the route answers 404.
	rec, err := svc.Verify(ctx, *req.Body.Token)
	if errors.Is(err, auth.ErrInvalidToken) {
		return nil, errNotRevocable
	}
	if err != nil {
		return nil, t.internalError(ctx, "verify token", err)
	}
	switch err := svc.Revoke(ctx, rec.ID); {
	case errors.Is(err, token.ErrNotRevocable), errors.Is(err, store.ErrNotFound):
		return nil, errNotRevocable
	case err != nil:
		return nil, t.internalError(ctx, "revoke token", err)
	}
	return accessgen.RevokeTokenByValue200JSONResponse{RevokedTokenId: rec.ID}, nil
}

// GetTokenDefaultExpiry answers the default lifetime in seconds. Admin only.
func (s accessService) GetTokenDefaultExpiry(
	ctx context.Context,
	_ accessgen.GetTokenDefaultExpiryRequestObject,
) (accessgen.GetTokenDefaultExpiryResponseObject, error) {
	t := s.tokenAPI()
	st, err := artifactoryService{cfg: s.cfg}.content()
	if err != nil {
		return nil, err
	}
	if err := adminOnly(ctx); err != nil {
		return nil, err
	}
	secs := int64(types.DefaultTokenExpiry / time.Second)
	if err := st.Settings().Get(ctx, types.SettingDefaultTokenExpiry, &secs); err != nil &&
		!errors.Is(err, store.ErrNotFound) {
		return nil, t.internalError(ctx, "default token expiry", err)
	}
	v := int(secs)
	return accessgen.GetTokenDefaultExpiry200JSONResponse{DefaultTokenExpiration: &v}, nil
}

// SetTokenDefaultExpiry sets the default lifetime, in seconds as the token
// service reads it. Admin only; it applies to tokens created afterwards.
func (s accessService) SetTokenDefaultExpiry(
	ctx context.Context,
	req accessgen.SetTokenDefaultExpiryRequestObject,
) (accessgen.SetTokenDefaultExpiryResponseObject, error) {
	t := s.tokenAPI()
	st, err := artifactoryService{cfg: s.cfg}.content()
	if err != nil {
		return nil, err
	}
	if err := adminOnly(ctx); err != nil {
		return nil, err
	}
	if req.Body == nil || req.Body.DefaultTokenExpiration < 0 {
		return nil, badRequestf("default_token_expiration must be zero or more seconds")
	}
	if err := st.Settings().Put(ctx, types.SettingDefaultTokenExpiry,
		int64(req.Body.DefaultTokenExpiration)); err != nil {
		return nil, t.internalError(ctx, "default token expiry", err)
	}
	return accessgen.SetTokenDefaultExpiry200Response{}, nil
}

func tokenDetails(svc *token.Service, r types.Token) accessgen.TokenDetails {
	d := accessgen.TokenDetails{
		TokenId: r.ID, Subject: r.Subject, Issuer: svc.Issuer(), Scope: r.Scope.String(),
		IssuedAt: int(r.IssuedAt.Unix()), Refreshable: r.Refreshable,
	}
	if r.Description != "" {
		d.Description = &r.Description
	}
	if !r.ExpiresAt.IsZero() {
		d.Expiry = ptrTo(int(r.ExpiresAt.Unix()))
	}
	if !r.LastUsed.IsZero() {
		d.LastUsed = ptrTo(int(r.LastUsed.Unix()))
	}
	return d
}

func ptrTo[T any](v T) *T { return &v }

// Legacy endpoints: /api/security/token. The create and revoke bodies are
// form-encoded, so those two routes are hand-written.

func (s artifactoryService) registerTokenRoutes(r chi.Router) {
	r.With(bodyLimit(managementBodyLimit)).Post(legacyTokenPath, s.createLegacyToken)
	r.With(bodyLimit(managementBodyLimit)).Post(legacyTokenRevokePath, s.revokeLegacyToken)
}

// GetLegacyTokens lists every token in the legacy shape. Unlike
// GET /access/api/v1/tokens, which shows a user their own, it is admin only,
// as the deprecated legacy API documents (#382).
func (s artifactoryService) GetLegacyTokens(
	ctx context.Context,
	_ artgen.GetLegacyTokensRequestObject,
) (artgen.GetLegacyTokensResponseObject, error) {
	if _, err := auth.RequireAdmin(ctx); err != nil {
		return nil, fmt.Errorf("tokens: %w", err)
	}
	recs, svc, err := tokenAPI{cfg: s.cfg}.visible(ctx)
	if err != nil {
		return nil, err
	}
	list := make([]artgen.LegacyTokenDetails, 0, len(recs))
	for _, r := range recs {
		d := tokenDetails(svc, r)
		list = append(list, artgen.LegacyTokenDetails{
			TokenId: &d.TokenId, Subject: &d.Subject, Issuer: &d.Issuer,
			IssuedAt: &d.IssuedAt, Expiry: d.Expiry, Refreshable: &d.Refreshable,
		})
	}
	return artgen.GetLegacyTokens200JSONResponse{Tokens: &list}, nil
}

// createLegacyToken is POST /api/security/token: the form-encoded create or
// refresh the JFrog CLI still implements. A refresh carries the token being
// refreshed as access_token, which must pair with refresh_token (refresh).
func (s artifactoryService) createLegacyToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeErr(w, r, badRequestf("malformed form: %v", err))
		return
	}
	f := r.PostForm
	m := mintRequest{
		grant: f.Get("grant_type"), username: f.Get("username"), scope: f.Get("scope"),
		audience: f.Get("audience"), refreshToken: f.Get("refresh_token"),
		accessToken: f.Get("access_token"), legacy: true,
	}
	var err error
	if v := f.Get("refreshable"); v != "" {
		if m.refreshable, err = strconv.ParseBool(v); err != nil {
			writeErr(w, r, badRequestf("refreshable must be true or false"))
			return
		}
	}
	if v := f.Get("expires_in"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeErr(w, r, badRequestf("expires_in must be a number of seconds"))
			return
		}
		m.expiresIn = &n
	}
	issued, err := tokenAPI{cfg: s.cfg}.mint(r.Context(), m)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	expires := int(issued.ExpiresIn)
	scope := issued.Record.Scope.String()
	out := artgen.LegacyTokenResponse{
		AccessToken: &issued.AccessToken, ExpiresIn: &expires, Scope: &scope,
		TokenType: ptrTo("Bearer"),
	}
	if issued.RefreshToken != "" {
		out.RefreshToken = &issued.RefreshToken
	}
	writeJSON(w, http.StatusOK, out)
}

// revokeLegacyToken is POST /api/security/token/revoke, by value or by id.
// It answers 200 when the token was already revoked or never existed, as
// Artifactory does, and 400 for a non-revocable one. It is admin only, even
// for the caller's own token (#382): DELETE /access/api/v1/tokens/{id} is
// where a user revokes their own.
func (s artifactoryService) revokeLegacyToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	t := tokenAPI{cfg: s.cfg}
	svc, err := t.service()
	if err == nil {
		_, err = auth.RequireAdmin(ctx)
	}
	if err == nil {
		err = r.ParseForm()
		if err != nil {
			err = badRequestf("malformed form: %v", err)
		}
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	value, id := r.PostForm.Get("token"), r.PostForm.Get("token_id")
	if (value == "") == (id == "") {
		writeErr(w, r, badRequestf("exactly one of token and token_id is required"))
		return
	}
	if value != "" {
		rec, err := svc.Verify(ctx, value)
		if errors.Is(err, auth.ErrInvalidToken) {
			writeText(w, "Token not found or already revoked")
			return
		}
		if err != nil {
			writeErr(w, r, t.internalError(ctx, "verify token", err))
			return
		}
		id = rec.ID
	}
	rec, _, err := t.own(ctx, id)
	var e *apierror.Error
	if errors.As(err, &e) && e.Status == http.StatusNotFound {
		writeText(w, "Token not found or already revoked")
		return
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	switch err := svc.Revoke(ctx, rec.ID); {
	case errors.Is(err, token.ErrNotRevocable):
		writeErr(w, r, badRequestf("Token is not revocable"))
	case errors.Is(err, store.ErrNotFound):
		writeText(w, "Token not found or already revoked")
	case err != nil:
		writeErr(w, r, t.internalError(ctx, "revoke token", err))
	default:
		writeText(w, "Token revoked")
	}
}

// GetUserEncryptedPassword returns, in place of Artifactory's encrypted
// password, a reference token bound to the caller: every client that took
// the encrypted password accepts it as a password. Like the encrypted
// password it is the same on every call and lasts until the password is set
// or expired, which revokes it; it is also revocable through the token API.
// A token-authenticated caller is refused: the encrypted password stands
// for a password, which only a password login proves.
func (s artifactoryService) GetUserEncryptedPassword(
	ctx context.Context,
	_ artgen.GetUserEncryptedPasswordRequestObject,
) (artgen.GetUserEncryptedPasswordResponseObject, error) {
	t := tokenAPI{cfg: s.cfg}
	svc, err := t.service()
	if err != nil {
		return nil, err
	}
	p, err := auth.RequireAuthenticated(ctx)
	if err != nil {
		return nil, fmt.Errorf("tokens: %w", err)
	}
	if p.TokenID != "" {
		return nil, badRequestf(
			"The encrypted password needs a password login, not an access token",
		)
	}
	secret, err := svc.EncryptedPassword(ctx, p.Name)
	if err != nil {
		return nil, t.internalError(ctx, "encrypted password", err)
	}
	return artgen.GetUserEncryptedPassword200TextResponse(secret), nil
}

// writeText answers 200 with a plain-text confirmation, as the legacy
// security endpoints do.
func writeText(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(msg))
}
