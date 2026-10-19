package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const serviceIDPath = "/artifactory/api/system/service_id"

// serviceIDHandler serves a store whose instance id differs from the other
// fixtures', so a body that matched them by accident would fail here.
func serviceIDHandler(t *testing.T) (http.Handler, *token.Service) {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	cheapSeed(t, db)
	if err := db.Instance().Init(ctx, types.InstanceIdentity{
		InstanceID: "jfrt@0f1e2d3c-4b5a-4687-9a0b-1c2d3e4f5a6b", ProtocolVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	tokens, err := token.New(ctx, db, token.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{
		Hash: testHash, Tokens: tokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	return New(Config{
		CompatVersion: "7.155.0", Store: db, Auth: a, Tokens: tokens, PasswordHash: testHash,
	}), tokens
}

// TestServiceID: an administrator reads the stored instance id as bare
// text, the id whose UUID the token issuer carries after jfac@.
func TestServiceID(t *testing.T) {
	h, tokens := serviceIDHandler(t)
	rec := do(t, h, http.MethodGet, serviceIDPath, basic("admin", "pw"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
		t.Errorf("Content-Type %q, want text/plain", ct)
	}
	const want = "jfrt@0f1e2d3c-4b5a-4687-9a0b-1c2d3e4f5a6b"
	if got := rec.Body.String(); got != want {
		t.Errorf("body %q, want %q", got, want)
	}
	if got := "jfac@" + strings.TrimPrefix(want, "jfrt@"); got != tokens.Issuer() {
		t.Errorf("issuer %q, want %q", tokens.Issuer(), got)
	}
}

// TestServiceIDGrantAdmin follows jf rt atc --grant-admin: read the service
// id, ask the legacy token API for the scope <id>:admin, and use the token
// as an administrator.
func TestServiceIDGrantAdmin(t *testing.T) {
	h, _ := serviceIDHandler(t)
	id := do(t, h, http.MethodGet, serviceIDPath, basic("admin", "pw"), "").Body.String()
	rec := do(
		t,
		h,
		http.MethodPost,
		"/artifactory/api/security/token",
		formAs(basic("admin", "pw")),
		url.Values{
			"username":   {"admin"},
			"scope":      {id + ":admin"},
			"expires_in": {"3600"},
		}.Encode(),
	)
	created := decodeObject(t, rec.Body.String())
	if rec.Code != http.StatusOK || created["scope"] != "applied-permissions/admin" {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	access, _ := created["access_token"].(string)
	rec = do(t, h, http.MethodGet, serviceIDPath, bearerAuth(access), "")
	if rec.Code != http.StatusOK || rec.Body.String() != id {
		t.Fatalf("service id with the granted token: %d %s", rec.Code, rec.Body)
	}
}

// TestServiceIDWithoutIdentity: a store with no instance identity is a
// server fault, never an empty id a client would build a scope from.
func TestServiceIDWithoutIdentity(t *testing.T) {
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Name: "admin", Admin: true})
	_, err := artifactoryService{cfg: Config{Store: inmem.New()}}.
		GetServiceId(ctx, artgen.GetServiceIdRequestObject{})
	var e *apierror.Error
	if !errors.As(err, &e) || e.Status != http.StatusInternalServerError {
		t.Fatalf("err %v, want a 500", err)
	}
}
