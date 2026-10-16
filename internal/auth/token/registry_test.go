package token

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// A registry token verifies with what it grants and nothing stores it, so
// it is never listed, touched or refreshed, and it ends with its expiry.
func TestIssueRegistry(t *testing.T) {
	s, db, c := world(t)
	ctx := context.Background()
	access := types.RegistryAccess{Realm: "docker-local", Grants: []types.RegistryGrant{
		{Type: "repository", Name: "app", Actions: []string{"pull", "push"}},
	}}
	out, err := s.IssueRegistry(ctx, RegistryRequest{
		Username: "alice", ExpiresIn: 10 * time.Minute, Access: access,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ExpiresIn != 600 || out.RefreshToken != "" || out.ReferenceToken != "" {
		t.Fatalf("issued %+v", out)
	}
	if n, _, err := db.Tokens().List(ctx, store.TokenFilter{}, store.Page{}); err != nil ||
		len(n) != 0 {
		t.Fatalf("stored %v, %v", n, err)
	}
	var claims map[string]any
	if err := json.Unmarshal(decodePayload(t, out.AccessToken), &claims); err != nil {
		t.Fatal(err)
	}
	if claims["scp"] != "applied-permissions/user" || claims["registry"] == nil {
		t.Fatalf("claims %v", claims)
	}
	rec, err := s.Verify(ctx, out.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(&access, rec.Registry); diff != "" {
		t.Fatal(diff)
	}
	if rec.Subject != s.Issuer()+"/users/alice" ||
		!rec.ExpiresAt.Equal(c.now().Add(10*time.Minute)) ||
		rec.ID != out.Record.ID {
		t.Fatalf("record %+v", rec)
	}
	if _, err := s.VerifyExpired(ctx, out.AccessToken); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("refresh with a registry token: %v", err)
	}
	c.add(10 * time.Minute)
	invalid(t, s, out.AccessToken, "expired registry token")

	// No grants still marks a registry token, with an empty list.
	none, err := s.IssueRegistry(ctx, RegistryRequest{Username: "alice", ExpiresIn: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if rec, err := s.Verify(ctx, none.AccessToken); err != nil || rec.Registry == nil ||
		rec.Registry.Grants == nil || len(rec.Registry.Grants) != 0 {
		t.Fatalf("no grants: %+v, %v", rec.Registry, err)
	}
}

func TestIssueRegistryRefuses(t *testing.T) {
	s, _, _ := world(t)
	for name, r := range map[string]RegistryRequest{
		"no user":      {ExpiresIn: time.Hour},
		"slash":        {Username: "a/b", ExpiresIn: time.Hour},
		"no lifetime":  {Username: "alice"},
		"negative":     {Username: "alice", ExpiresIn: -time.Second},
		"over an hour": {Username: "alice", ExpiresIn: time.Hour + time.Second},
	} {
		if _, err := s.IssueRegistry(context.Background(), r); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A registry claim on a token without an expiry, or with a scope that does
// not parse, was not issued here.
func TestRegistryRecordRefuses(t *testing.T) {
	ok := claims{Scp: "applied-permissions/user", Exp: 1, Registry: &types.RegistryAccess{}}
	if _, err := registryRecord(ok, false); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]claims{
		"no expiry": {Scp: ok.Scp, Registry: ok.Registry},
		"bad scope": {Scp: "nonsense:", Exp: 1, Registry: ok.Registry},
	} {
		if _, err := registryRecord(c, false); !errors.Is(err, auth.ErrInvalidToken) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
