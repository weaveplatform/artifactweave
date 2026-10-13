package server

import (
	"context"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// seedTokens gives a fixture store an instance identity and a token service,
// and three token records with fixed ids the status table addresses: one of
// admin's, one of user's, and a short-lived non-revocable one of admin's.
func seedTokens(t *testing.T, db store.Store) *token.Service {
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
	return tokens
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
		{create, tokens, "admin", `{"username":"svc","scope":"applied-permissions/admin"}`, 200},
		{create, tokens, "user", `{"refreshable":true}`, 200},
		{create, tokens, "user", `{"username":"admin"}`, 403},
		{create, tokens, "user", `{"scope":"applied-permissions/admin"}`, 403},
		{create, tokens, "user", `{"expires_in":0}`, 400},
		{create, tokens, "admin", `{"scope":"nonsense"}`, 400},
		{create, tokens, "admin", `{"grant_type":"password"}`, 400},
		{create, tokens, "admin", `{"grant_type":"refresh_token"}`, 400},
		{create, tokens, "admin", `{"grant_type":"refresh_token","refresh_token":"unknown"}`, 400},
		{create, tokens, "anonymous", `{}`, 401},
		{create, tokens, "", `{}`, 401},

		{list, tokens, "admin", "", 200},
		{list, tokens, "user", "", 200},
		{list, tokens, "anonymous", "", 401},
		{list, tokens, "", "", 401},

		{get, tokens + "/admin-token", "admin", "", 200},
		{get, tokens + "/user-token", "user", "", 200},
		{get, tokens + "/admin-token", "user", "", 403},
		{get, tokens + "/missing", "admin", "", 404},
		{get, tokens + "/me", "admin", "", 404},
		{get, tokens + "/admin-token", "", "", 401},

		{revoke, tokens + "/short-token", "admin", "", 403},
		{revoke, tokens + "/admin-token", "user", "", 403},
		{revoke, tokens + "/missing", "admin", "", 404},
		{revoke, tokens + "/admin-token", "", "", 401},
		{revoke, tokens + "/admin-token", "admin", "", 200},

		{byValue, revokeAt, "admin", `{"token":"not-a-token"}`, 404},
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

		{lList, legacy, "admin", "", 200},
		{lList, legacy, "user", "", 200},
		{lList, legacy, "", "", 401},

		{lCreate, legacy, "admin", "form:username=svc&scope=member-of-groups:readers", 200},
		{lCreate, legacy, "user", "form:username=admin", 403},
		{lCreate, legacy, "admin", "form:expires_in=soon", 400},
		{lCreate, legacy, "admin", "form:refreshable=maybe", 400},
		{lCreate, legacy, "", "form:username=admin", 401},

		{lRevoke, legacy + "/revoke", "admin", "form:token_id=missing", 200},
		{lRevoke, legacy + "/revoke", "admin", "form:token=not-a-token", 200},
		{lRevoke, legacy + "/revoke", "admin", "form:", 400},
		{lRevoke, legacy + "/revoke", "admin", "form:token_id=a&token=b", 400},
		{lRevoke, legacy + "/revoke", "admin", "form:token_id=short-token", 400},
		{lRevoke, legacy + "/revoke", "user", "form:token_id=admin-token-2", 403},
		{lRevoke, legacy + "/revoke", "", "form:token_id=admin-token-2", 401},
		{lRevoke, legacy + "/revoke", "admin", "form:token_id=admin-token-2", 200},

		{enc, encPath, "admin", "", 200},
		{enc, encPath, "anonymous", "", 401},
		{enc, encPath, "", "", 401},
	}
}
