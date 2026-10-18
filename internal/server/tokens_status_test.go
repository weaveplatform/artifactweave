package server

import (
	"context"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// statusSecrets are the fixture's token values, which only its own signing
// key can produce, by the name a row writes as {name}. These tokens expired
// an hour ago, within the refresh grace:
//   - expired-access and expired-refresh: a refreshable token of admin's;
//   - forged-access: expired-access with its signature altered;
//   - other-refresh: the refresh token of another of admin's tokens;
//   - legacy-access and legacy-refresh: a third of admin's, for the legacy
//     route;
//   - user-refresh and user-2-refresh: refresh tokens of two of user's.
//
// These are admin's, live for an hour, below the revocable threshold:
//   - short-access: neither refreshable nor forced, so not revocable;
//   - refreshable-id and refreshable-2-access: two refreshable ones, which
//     are revocable all the same.
type statusSecrets map[string]string

// seedTokens gives a fixture store an instance identity and a token service,
// token records with fixed ids the status table addresses (two of admin's,
// one of user's, and a short-lived non-revocable one of admin's), and the
// statusSecrets.
func seedTokens(t *testing.T, db store.Store) (*token.Service, statusSecrets) {
	t.Helper()
	ctx := context.Background()
	if err := db.Instance().Init(ctx, types.InstanceIdentity{
		InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	tokens, err := token.New(ctx, db, token.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	for _, r := range []types.Token{
		{ID: "admin-token", Subject: subjectOf(tokens, "admin"), Revocable: true},
		{ID: "admin-token-2", Subject: subjectOf(tokens, "admin"), Revocable: true},
		{ID: "user-token", Subject: subjectOf(tokens, "user"), Revocable: true},
		{
			ID: "short-token", Subject: subjectOf(tokens, "admin"),
			ExpiresAt: now.Add(time.Hour),
		},
	} {
		r.Scope, r.IssuedAt, r.AccessDigest = types.UserScope(), now, []byte(r.ID)
		if err := db.Tokens().Create(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	// A service two hours behind, over the same key, issues a one-hour token
	// that has expired by now but is well within the refresh grace.
	past, err := token.New(ctx, db, token.Options{
		KeyBits: 1024, Now: func() time.Time { return time.Now().Add(-2 * time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	hour := time.Hour
	secrets := statusSecrets{}
	for _, r := range []struct{ name, user string }{
		{"expired", "admin"},
		{"other", "admin"},
		{"legacy", "admin"},
		{"user", "user"},
		{"user-2", "user"},
	} {
		issued, err := past.Issue(ctx, token.Request{
			Username: r.user, Refreshable: true, ExpiresIn: &hour, ByAdmin: true,
			Description: "refreshable " + r.name,
		})
		if err != nil {
			t.Fatal(err)
		}
		secrets[r.name+"-access"], secrets[r.name+"-refresh"] = issued.AccessToken, issued.RefreshToken
	}
	sig := []byte(secrets["expired-access"])
	sig[len(sig)-3] ^= 1
	secrets["forged-access"] = string(sig)
	for _, r := range []struct {
		name        string
		refreshable bool
	}{{"short", false}, {"refreshable", true}, {"refreshable-2", true}} {
		issued, err := tokens.Issue(ctx, token.Request{
			Username: "admin", Refreshable: r.refreshable, ExpiresIn: &hour, ByAdmin: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		secrets[r.name+"-access"], secrets[r.name+"-id"] = issued.AccessToken, issued.Record.ID
	}
	return tokens, secrets
}

// lRefresh is a legacy refresh form body.
func lRefresh(access, refresh string) string {
	return "form:grant_type=refresh_token&access_token=" + access + "&refresh_token=" + refresh
}

// tokenStatusRows covers the Access token API and the legacy token
// endpoints. Rows run in order on one fixture: a revocation removes its
// token for the rows after it.
func tokenStatusRows() []statusCase {
	const (
		create   = "POST /access/api/v1/tokens"
		list     = "GET /access/api/v1/tokens"
		get      = "GET /access/api/v1/tokens/{tokenId}"
		revoke   = "DELETE /access/api/v1/tokens/{tokenId}"
		byValue  = "DELETE /access/api/v1/tokens/revoke"
		expGet   = "GET /access/api/v1/config/token/default_expiry"
		expPut   = "PUT /access/api/v1/config/token/default_expiry"
		lList    = "GET /artifactory/api/security/token"
		lCreate  = "POST /artifactory/api/security/token"
		lRevoke  = "POST /artifactory/api/security/token/revoke"
		enc      = "GET /artifactory/api/security/encryptedPassword"
		tokens   = "/access/api/v1/tokens"
		expiry   = "/access/api/v1/config/token/default_expiry"
		legacy   = "/artifactory/api/security/token"
		encPath  = "/artifactory/api/security/encryptedPassword"
		revokeAt = "/access/api/v1/tokens/revoke"
	)
	return []statusCase{
		// A password creates no token here while token.allow_basic_auth is
		// off, its default (#385); a token does.
		{create, tokens, "admin", `{}`, 401},
		{create, tokens, "user", `{}`, 401},
		{
			create, tokens, "admin-token",
			`{"username":"svc","scope":"applied-permissions/admin"}`, 200,
		},
		{create, tokens, "user-token", `{"refreshable":true}`, 200},
		{create, tokens, "user-token", `{"username":"admin"}`, 403},
		{create, tokens, "user-token", `{"scope":"applied-permissions/admin"}`, 403},
		// A token that never expires is the administrator's alone, and a
		// refused expires_in is 403, as createtoken documents (#406).
		{create, tokens, "user-token", `{"expires_in":0}`, 403},
		{create, tokens, "admin-token", `{"scope":"nonsense"}`, 400},
		{create, tokens, "admin", `{"grant_type":"password"}`, 400},
		{create, tokens, "admin", `{"grant_type":"refresh_token"}`, 400},
		{create, tokens, "admin", `{"grant_type":"refresh_token","refresh_token":"unknown"}`, 400},
		{create, tokens, "anonymous", `{}`, 401},
		{create, tokens, "", `{}`, 401},
		// An expired access token reaches only a refresh of itself; revoked
		// and forged tokens are refused before the handler.
		{create, tokens, "expired", `{}`, 401},
		// The authenticated user, or the expired token's, must be the refresh
		// token's user (403); another token of the same user is 400.
		{
			create,
			tokens,
			"admin",
			`{"grant_type":"refresh_token","refresh_token":"{user-refresh}"}`,
			403,
		},
		{
			create,
			tokens,
			"expired",
			`{"grant_type":"refresh_token","refresh_token":"{user-refresh}"}`,
			403,
		},
		{
			create,
			tokens,
			"user",
			`{"grant_type":"refresh_token","refresh_token":"{user-2-refresh}"}`,
			200,
		},
		{
			create,
			tokens,
			"expired",
			`{"grant_type":"refresh_token","refresh_token":"{other-refresh}"}`,
			400,
		},
		{
			create,
			tokens,
			"forged",
			`{"grant_type":"refresh_token","refresh_token":"{expired-refresh}"}`,
			401,
		},
		{
			create,
			tokens,
			"expired",
			`{"grant_type":"refresh_token","refresh_token":"{expired-refresh}"}`,
			200,
		},
		{
			create,
			tokens,
			"expired",
			`{"grant_type":"refresh_token","refresh_token":"{other-refresh}"}`,
			401,
		},

		{list, tokens, "admin", "", 200},
		{list, tokens, "user", "", 200},
		{list, tokens, "anonymous", "", 401},
		{list, tokens, "", "", 401},
		{list, tokens, "expired", "", 401},

		{get, tokens + "/admin-token", "admin", "", 200},
		{get, tokens + "/user-token", "user", "", 200},
		{get, tokens + "/admin-token", "user", "", 403},
		{get, tokens + "/missing", "admin", "", 404},
		{get, tokens + "/me", "admin", "", 404},
		{get, tokens + "/admin-token", "", "", 401},

		// A token below the revocable threshold is revocable only when
		// refreshable or forced: by id the others are 400, by value 404
		// (#406).
		{revoke, tokens + "/short-token", "admin", "", 400},
		{revoke, tokens + "/{refreshable-id}", "admin", "", 200},
		{revoke, tokens + "/admin-token", "user", "", 403},
		{revoke, tokens + "/missing", "admin", "", 404},
		{revoke, tokens + "/admin-token", "", "", 401},
		{revoke, tokens + "/admin-token", "admin", "", 200},

		{byValue, revokeAt, "admin", `{"token":"not-a-token"}`, 404},
		{byValue, revokeAt, "admin", `{"token":"{short-access}"}`, 404},
		{byValue, revokeAt, "admin", `{"token":"{refreshable-2-access}"}`, 200},
		{byValue, revokeAt, "admin", `{}`, 400},
		{byValue, revokeAt, "user", `{"token":"x"}`, 403},
		{byValue, revokeAt, "", `{"token":"x"}`, 401},

		{expGet, expiry, "admin", "", 200},
		{expGet, expiry, "user", "", 403},
		{expGet, expiry, "", "", 401},
		{expPut, expiry, "admin", `{"default_token_expiration":-1}`, 400},
		{expPut, expiry, "user", `{"default_token_expiration":7200}`, 403},
		{expPut, expiry, "", `{"default_token_expiration":7200}`, 401},
		{expPut, expiry, "admin", `{"default_token_expiration":7200}`, 200},

		// The legacy list and revoke are admin only, even for the caller's
		// own tokens (#382).
		{lList, legacy, "admin", "", 200},
		{lList, legacy, "user", "", 403},
		{lList, legacy, "anonymous", "", 401},
		{lList, legacy, "", "", 401},

		// The legacy route takes a password whatever token.allow_basic_auth
		// says.
		{lCreate, legacy, "admin", "form:username=svc&scope=member-of-groups:readers", 200},
		{lCreate, legacy, "user", "form:username=admin", 403},
		{lCreate, legacy, "admin", "form:expires_in=soon", 400},
		{lCreate, legacy, "admin", "form:refreshable=maybe", 400},
		{lCreate, legacy, "", "form:username=admin", 401},
		// The legacy refresh, as jf sends it: no credentials, and the token
		// being refreshed, expired here, as access_token.
		{lCreate, legacy, "", "form:grant_type=refresh_token&refresh_token={legacy-refresh}", 400},
		{lCreate, legacy, "", lRefresh("{forged-access}", "{legacy-refresh}"), 401},
		{lCreate, legacy, "", lRefresh("{legacy-access}", "unknown"), 400},
		{lCreate, legacy, "", lRefresh("{legacy-access}", "{other-refresh}"), 400},
		{lCreate, legacy, "", lRefresh("{legacy-access}", "{user-refresh}"), 403},
		{lCreate, legacy, "user", lRefresh("{legacy-access}", "{legacy-refresh}"), 403},
		{lCreate, legacy, "", lRefresh("{legacy-access}", "{legacy-refresh}"), 200},
		{lCreate, legacy, "", lRefresh("{legacy-access}", "{legacy-refresh}"), 401},

		{lRevoke, legacy + "/revoke", "admin", "form:token_id=missing", 200},
		{lRevoke, legacy + "/revoke", "admin", "form:token=not-a-token", 200},
		{lRevoke, legacy + "/revoke", "admin", "form:", 400},
		{lRevoke, legacy + "/revoke", "admin", "form:token_id=a&token=b", 400},
		{lRevoke, legacy + "/revoke", "admin", "form:token_id=short-token", 400},
		{lRevoke, legacy + "/revoke", "user", "form:token_id=admin-token-2", 403},
		{lRevoke, legacy + "/revoke", "user", "form:token_id=user-token", 403},
		{lRevoke, legacy + "/revoke", "user", "form:", 403},
		{lRevoke, legacy + "/revoke", "", "form:token_id=admin-token-2", 401},
		{lRevoke, legacy + "/revoke", "admin", "form:token_id=admin-token-2", 200},

		{enc, encPath, "admin", "", 200},
		{enc, encPath, "user", "", 200},
		{enc, encPath, "anonymous", "", 401},
		{enc, encPath, "", "", 401},
		{enc, encPath, "expired", "", 401},
	}
}
