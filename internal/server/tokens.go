package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
		return token.Issued{}, badRequest("unsupported grant_type %q", m.grant)
	}
	p, err := auth.RequireAuthenticated(ctx)
	if err != nil {
		return token.Issued{}, fmt.Errorf("tokens: %w", err)
	}
	// A token whose scope is narrower than its user's identity must not mint
	// a broader one: a resource- or group-scoped token minting an identity
	// token would escalate past its own scope.
	if p.TokenID != "" && !identityScope(p.Scope) {
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
	if m.scope != "" {
		raw := m.scope
		if m.legacy {
			raw = legacyScope(raw)
		}
		if req.Scope, err = types.ParseScope(raw); err != nil {
			return token.Issued{}, badRequest("%v", err)
		}
		if !p.Admin && !onlyUserScope(req.Scope) {
			return token.Issued{}, apierror.New(
				http.StatusForbidden,
				"Only an administrator can create tokens with a scope other than applied-permissions/user",
			)
		}
	}
	if m.expiresIn != nil {
		if *m.expiresIn < 0 {
			return token.Issued{}, badRequest("expires_in must not be negative")
		}
		d := time.Duration(*m.expiresIn) * time.Second
		req.ExpiresIn = &d
	}
	issued, err := svc.Issue(ctx, req)
	if errors.Is(err, token.ErrInvalidRequest) {
		return token.Issued{}, badRequest("%v", err)
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
		return token.Issued{}, badRequest("refresh_token is required with grant_type refresh_token")
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
			return token.Issued{}, badRequest(
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
		return token.Issued{}, badRequest("The refresh token is not valid")
	case err != nil:
		return token.Issued{}, t.internalError(ctx, "refresh token", err)
	}
	return issued, nil
}

var errRefreshOtherUser = apierror.New(http.StatusForbidden,
	"The authenticated user must match the user of the access token being refreshed")

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
func legacyScope(s string) string {
	parts := strings.Fields(s)
	for i, p := range parts {
		switch {
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
// non-revocable ones, which Artifactory keeps no record of (see #79).
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
		return nil, badRequest("a token request body is required")
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

// GetTokens lists the caller's tokens, or every token for an administrator.
func (s accessService) GetTokens(
	ctx context.Context,
	_ accessgen.GetTokensRequestObject,
) (accessgen.GetTokensResponseObject, error) {
	recs, svc, err := s.tokenAPI().visible(ctx)
	if err != nil {
		return nil, err
	}
	out := accessgen.GetTokens200JSONResponse{Tokens: make([]accessgen.TokenDetails, 0, len(recs))}
	for _, r := range recs {
		out.Tokens = append(out.Tokens, tokenDetails(svc, r))
	}
	return out, nil
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

// RevokeTokenById revokes a token; a non-revocable one is 403, as the spec
// documents for this route.
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
		return nil, apierror.New(http.StatusForbidden, "Token is not revocable")
	case errors.Is(err, store.ErrNotFound):
		return nil, tokenNotFound()
	case err != nil:
		return nil, t.internalError(ctx, "revoke token", err)
	}
	return accessgen.RevokeTokenById200Response{}, nil
}

// RevokeTokenByValue revokes the token whose JWT or reference token is
// given. Admin only; a non-revocable token is 400, as the spec documents.
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
		return nil, badRequest("token is required")
	}
	// Verify resolves either form and refuses expired and revoked tokens,
	// which the route answers 404.
	rec, err := svc.Verify(ctx, *req.Body.Token)
	if errors.Is(err, auth.ErrInvalidToken) {
		return nil, tokenNotFound()
	}
	if err != nil {
		return nil, t.internalError(ctx, "verify token", err)
	}
	switch err := svc.Revoke(ctx, rec.ID); {
	case errors.Is(err, token.ErrNotRevocable):
		return nil, badRequest("Token is not revocable")
	case errors.Is(err, store.ErrNotFound):
		return nil, tokenNotFound()
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
		return nil, badRequest("default_token_expiration must be zero or more seconds")
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

// GetLegacyTokens lists tokens in the legacy shape, with the same
// visibility as GET /access/api/v1/tokens.
func (s artifactoryService) GetLegacyTokens(
	ctx context.Context,
	_ artgen.GetLegacyTokensRequestObject,
) (artgen.GetLegacyTokensResponseObject, error) {
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
		writeErr(w, r, badRequest("malformed form: %v", err))
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
			writeErr(w, r, badRequest("refreshable must be true or false"))
			return
		}
	}
	if v := f.Get("expires_in"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeErr(w, r, badRequest("expires_in must be a number of seconds"))
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
// Artifactory does; another user's token is 403 for a non-administrator,
// and a non-revocable one 400.
func (s artifactoryService) revokeLegacyToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	t := tokenAPI{cfg: s.cfg}
	svc, err := t.service()
	if err == nil {
		_, err = auth.RequireAuthenticated(ctx)
	}
	if err == nil {
		err = r.ParseForm()
		if err != nil {
			err = badRequest("malformed form: %v", err)
		}
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	value, id := r.PostForm.Get("token"), r.PostForm.Get("token_id")
	if (value == "") == (id == "") {
		writeErr(w, r, badRequest("exactly one of token and token_id is required"))
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
		writeErr(w, r, badRequest("Token is not revocable"))
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
		return nil, badRequest("The encrypted password needs a password login, not an access token")
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
