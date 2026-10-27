package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// tokenHandler is the router over a seeded store whose authenticator
// verifies access tokens, with a token service to issue them.
func tokenHandler(t *testing.T) (http.Handler, *token.Service) {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	cheapSeed(t, db)
	if err := db.Instance().Init(ctx, types.InstanceIdentity{
		InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
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
	return New(Config{CompatVersion: "7.155.0", Store: db, Auth: a}), tokens
}

func TestAccTokenAuthentication(t *testing.T) {
	ctx := context.Background()
	h, tokens := tokenHandler(t)
	issued, err := tokens.Issue(ctx, token.Request{
		Username: "admin", IncludeReference: true, ForceRevocable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	groups, err := types.ParseScope("applied-permissions/groups:readers")
	if err != nil {
		t.Fatal(err)
	}
	groupsOnly, err := tokens.Issue(ctx, token.Request{Username: "admin", Scope: groups})
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := tokens.Issue(
		ctx,
		token.Request{Username: "admin", ExpiresIn: ptr(8 * time.Hour)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := tokens.Revoke(ctx, revoked.Record.ID); err != nil {
		t.Fatal(err)
	}
	jwt, ref := issued.AccessToken, issued.ReferenceToken
	tampered := jwt[:len(jwt)-4] + "AAAA"
	const admin = "/artifactory/api/system/license"
	for name, tc := range map[string]struct {
		headers  map[string]string
		expected int
	}{
		"bearer jwt":            {map[string]string{"Authorization": "Bearer " + jwt}, http.StatusOK},
		"bearer reference":      {map[string]string{"Authorization": "Bearer " + ref}, http.StatusOK},
		"lower-case scheme":     {map[string]string{"Authorization": "bearer " + jwt}, http.StatusOK},
		"basic with jwt":        {basic("admin", jwt), http.StatusOK},
		"basic with reference":  {basic("admin", ref), http.StatusOK},
		"basic, another user":   {basic("someone", jwt), http.StatusUnauthorized},
		"api header, reference": {map[string]string{"X-JFrog-Art-Api": ref}, http.StatusOK},
		"api header, jwt":       {map[string]string{"X-JFrog-Art-Api": jwt}, http.StatusUnauthorized},
		"api header, api key":   {map[string]string{"X-JFrog-Art-Api": "AKCp8legacykey"}, http.StatusUnauthorized},
		"tampered":              {map[string]string{"Authorization": "Bearer " + tampered}, http.StatusUnauthorized},
		"revoked":               {map[string]string{"Authorization": "Bearer " + revoked.AccessToken}, http.StatusUnauthorized},
		"empty bearer":          {map[string]string{"Authorization": "Bearer"}, http.StatusUnauthorized},
		"other scheme":          {map[string]string{"Authorization": "Negotiate abc"}, http.StatusUnauthorized},
		"password still works":  {basic("admin", "pw"), http.StatusOK},
	} {
		rec := do(t, h, http.MethodGet, admin, tc.headers, "")
		if rec.Code != tc.expected {
			t.Errorf("%s: %d, want %d: %s", name, rec.Code, tc.expected, rec.Body)
		}
		if tc.expected == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s: 401 without a challenge", name)
		}
	}
	// A groups-only scope does not carry the user's administrator rights:
	// creating a repository needs them.
	const create = "/artifactory/api/repositories/token-made"
	body := `{"rclass":"local","packageType":"generic"}`
	bearer := func(raw string) map[string]string {
		return map[string]string{
			"Authorization": "Bearer " + raw,
			"Content-Type":  "application/json",
		}
	}
	if rec := do(
		t,
		h,
		http.MethodPut,
		create,
		bearer(groupsOnly.AccessToken),
		body,
	); rec.Code != http.StatusForbidden {
		t.Errorf("groups-only scope: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, http.MethodPut, create, bearer(jwt), body); rec.Code != http.StatusOK {
		t.Errorf("admin token: %d %s", rec.Code, rec.Body)
	}
}

func ptr[T any](v T) *T { return &v }
