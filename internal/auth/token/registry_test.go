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
func TestUnitIssueRegistry(t *testing.T) {
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

func TestUnitIssueRegistryRefuses(t *testing.T) {
	s, _, _ := world(t)
	for name, r := range map[string]RegistryRequest{
		"no user":      {ExpiresIn: time.Hour},
		"slash":        {Username: "a/b", ExpiresIn: time.Hour},
		"no lifetime":  {Username: "alice"},
		"negative":     {Username: "alice", ExpiresIn: -time.Second},
		"over an hour": {Username: "alice", ExpiresIn: time.Hour + time.Second},
		"identity":     {Username: "alice", ExpiresIn: time.Hour, Identity: "borrowed"},
	} {
		if _, err := s.IssueRegistry(context.Background(), r); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A registry claim on a token without an expiry, or with a scope that does
// not parse, was not issued here.
func TestUnitRegistryRecordRefuses(t *testing.T) {
	ok := claims{Scp: "applied-permissions/user", Exp: 1, Registry: &types.RegistryAccess{}}
	if _, err := registryClaims(ok, false); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]claims{
		"no expiry": {Scp: ok.Scp, Registry: ok.Registry},
		"bad scope": {Scp: "nonsense:", Exp: 1, Registry: ok.Registry},
		"identity":  {Scp: ok.Scp, Exp: 1, Registry: ok.Registry, Idn: "borrowed"},
	} {
		if _, err := registryClaims(c, false); !errors.Is(err, auth.ErrInvalidToken) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A registry token keeps the scope and the OIDC identity of the credential
// that obtained it, so it resolves to the same transient or synthetic user
// and never acts more broadly; a password's scope is the default.
func TestUnitIssueRegistryKeepsScopeAndIdentity(t *testing.T) {
	s, _, _ := world(t)
	ctx := context.Background()
	groups, err := types.ParseScope("applied-permissions/groups:readers")
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		scope       types.Scope
		identity    types.TokenIdentity
		expectedScp string
	}{
		"password":  {types.Scope{}, types.IdentityStored, "applied-permissions/user"},
		"transient": {types.UserScope(), types.IdentityTransient, "applied-permissions/user"},
		"synthetic": {groups, types.IdentitySynthetic, "applied-permissions/groups:readers"},
	} {
		out, err := s.IssueRegistry(ctx, RegistryRequest{
			Username: "oidc-ci", ExpiresIn: time.Hour, Scope: c.scope, Identity: c.identity,
		})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var claims map[string]any
		if err := json.Unmarshal(decodePayload(t, out.AccessToken), &claims); err != nil {
			t.Fatal(err)
		}
		if claims["scp"] != c.expectedScp || (claims["idn"] != nil) != (c.identity != "") {
			t.Errorf("%s: claims %v", name, claims)
		}
		rec, err := s.Verify(ctx, out.AccessToken)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if rec.Identity != c.identity || rec.Scope.String() != c.expectedScp {
			t.Errorf("%s: record identity %q scope %q", name, rec.Identity, rec.Scope)
		}
	}
}

// A registry token obtained with a token is bound to it: nothing stores the
// registry token, so revoking the parent is what ends it early (decision
// 0005). One obtained with a password has no parent and ends with its
// expiry alone.
func TestUnitRegistryTokenEndsWithItsParent(t *testing.T) {
	s, _, _ := world(t)
	ctx := context.Background()
	parent := issue(t, s, Request{ForceRevocable: true})
	child, err := s.IssueRegistry(ctx, RegistryRequest{
		Username: "alice", ExpiresIn: time.Hour, Parent: parent.Record.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(decodePayload(t, child.AccessToken), &claims); err != nil {
		t.Fatal(err)
	}
	if claims["ptk"] != parent.Record.ID {
		t.Fatalf("claims %v", claims)
	}
	if rec, err := s.Verify(ctx, child.AccessToken); err != nil || rec.Parent != parent.Record.ID {
		t.Fatalf("before revoking: %+v, %v", rec, err)
	}
	if err := s.Revoke(ctx, parent.Record.ID); err != nil {
		t.Fatal(err)
	}
	invalid(t, s, child.AccessToken, "registry token of a revoked parent")
}

func TestUnitRegistryTokenParentUnreadable(t *testing.T) {
	s, fs, _ := faulty(t)
	ctx := context.Background()
	parent := issue(t, s, Request{ForceRevocable: true})
	child, err := s.IssueRegistry(ctx, RegistryRequest{
		Username: "alice", ExpiresIn: time.Hour, Parent: parent.Record.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	fs.fail = map[string]bool{"tokens.get": true}
	if _, err := s.Verify(ctx, child.AccessToken); !errors.Is(err, errBoom) ||
		errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("an unreadable parent is not a bad credential: %v", err)
	}
}
