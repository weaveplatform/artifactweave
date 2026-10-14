package auth

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// fakeVerifier resolves tokens from a map; anything else is invalid.
type fakeVerifier map[string]types.Token

func (f fakeVerifier) Verify(_ context.Context, raw string) (types.Token, error) {
	if raw == "broken" {
		return types.Token{}, errors.New("database down")
	}
	t, ok := f[raw]
	if !ok {
		return types.Token{}, ErrInvalidToken
	}
	return t, nil
}

func scope(t *testing.T, s string) types.Scope {
	t.Helper()
	sc, err := types.ParseScope(s)
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func TestTokenPrincipal(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	for _, g := range []types.Group{
		{Name: "ops"}, {Name: "devs"}, {Name: "readers"}, {Name: "admins", AdminPrivileges: true},
	} {
		if err := db.Groups().Create(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []types.User{
		{Name: "root", Admin: true, Groups: []string{"ops"}, Status: types.UserEnabled},
		{Name: "dev", Groups: []string{"devs", "readers"}},
		{Name: "gone", Status: types.UserDisabled},
	} {
		if err := db.Users().Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	const iss = "jfac@x/users/"
	v := fakeVerifier{
		"root-user":  {Subject: iss + "root", Scope: scope(t, "applied-permissions/user")},
		"root-admin": {Subject: iss + "root", Scope: scope(t, "applied-permissions/admin")},
		"root-groups": {
			Subject: iss + "root",
			Scope:   scope(t, "applied-permissions/groups:readers"),
		},
		"dev-user":  {Subject: iss + "dev", Scope: scope(t, "applied-permissions/user")},
		"dev-admin": {Subject: iss + "dev", Scope: scope(t, "applied-permissions/admin")},
		"dev-repo":  {Subject: iss + "dev", Scope: scope(t, "artifact:libs:r")},
		"dev-ops":   {Subject: iss + "dev", Scope: scope(t, "applied-permissions/groups:ops")},
		"dev-admins": {
			Subject: iss + "dev",
			Scope:   scope(t, "applied-permissions/groups:admins"),
		},
		"gone":      {Subject: iss + "gone", Scope: types.UserScope()},
		"missing":   {Subject: iss + "nobody", Scope: types.UserScope()},
		"node":      {Subject: "jfac@x/nodes/n1", Scope: types.UserScope()},
		"malformed": {Subject: "nonsense", Scope: types.UserScope()},
	}
	a, err := NewAuthenticator(db.Users(), db.Settings(), Options{Tokens: v})
	if err != nil {
		t.Fatal(err)
	}
	// Authorize applies the scope: /admin is admin only for an admin, /groups
	// acts through the named groups whether or not the user belongs to them,
	// an admin-privilege group makes its members administrators, and a
	// resource-only scope keeps the user's groups for the evaluator to narrow.
	src := authz.Sources{Permissions: db.Permissions(), Groups: db.Groups()}
	for raw, want := range map[string]Principal{
		"root-user":   {Name: "root", Admin: true, Groups: []string{"ops"}},
		"root-admin":  {Name: "root", Admin: true, Groups: []string{"ops"}},
		"root-groups": {Name: "root", Groups: []string{"readers"}},
		"dev-user":    {Name: "dev", Groups: []string{"devs", "readers"}},
		"dev-admin":   {Name: "dev", Groups: []string{"devs", "readers"}},
		"dev-repo":    {Name: "dev", Groups: []string{"devs", "readers"}},
		"dev-ops":     {Name: "dev", Groups: []string{"ops"}},
		"dev-admins":  {Name: "dev", Admin: true, Groups: []string{"admins"}},
	} {
		p, err := a.Token(ctx, raw, "")
		if err == nil {
			p, err = Authorize(ctx, src, p)
		}
		if err != nil || p.Name != want.Name || p.Admin != want.Admin ||
			!slices.Equal(p.Groups, want.Groups) || p.Scope.String() != v[raw].Scope.String() ||
			p.Access() == nil {
			t.Errorf("%s: %+v, %v; want %+v", raw, p, err, want)
		}
	}
	for _, raw := range []string{"gone", "missing", "node", "malformed", "unknown"} {
		if _, err := a.Token(ctx, raw, ""); !errors.Is(err, ErrBadCredentials) {
			t.Errorf("%s: %v", raw, err)
		}
	}
	// A Basic user name must be the token's subject.
	if _, err := a.Token(ctx, "dev-user", "dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Token(ctx, "dev-user", "root"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("another user's token: %v", err)
	}
	// A verifier failure is not a credential failure.
	if _, err := a.Token(ctx, "broken", ""); err == nil || errors.Is(err, ErrBadCredentials) {
		t.Fatalf("verifier failure: %v", err)
	}
	none, err := NewAuthenticator(db.Users(), db.Settings(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := none.Token(ctx, "dev-user", ""); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("no verifier: %v", err)
	}
}

func TestLooksLikeToken(t *testing.T) {
	for s, want := range map[string]bool{
		"eyJhbGciOi.eyJzdWIi.c2ln": true,
		// One character short of a reference token.
		"cmVmd0123456789012345678901234567890123456789012345678901234567": false,
		"password":    false,
		"eyJonly.two": false,
		"a.b.c":       false,
	} {
		if got := LooksLikeToken(s); got != want {
			t.Errorf("%q: %v", s, got)
		}
	}
	ref := "cmVmd" + "01234567890123456789012345678901234567890123456789012345678"
	if !LooksLikeToken(ref) {
		t.Errorf("reference %q", ref)
	}
}
