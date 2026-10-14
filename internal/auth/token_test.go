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

// expiringVerifier is a fakeVerifier that also verifies expired tokens from
// a map of its own.
type expiringVerifier struct {
	fakeVerifier
	expired map[string]types.Token
}

func (e expiringVerifier) VerifyExpired(_ context.Context, raw string) (types.Token, error) {
	t, ok := e.expired[raw]
	if !ok {
		return types.Token{}, ErrInvalidToken
	}
	return t, nil
}

func TestExpiredToken(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	addUser(t, db, types.User{Name: "dev", Status: types.UserEnabled}, "")
	addUser(t, db, types.User{Name: "gone", Status: types.UserDisabled}, "")
	v := expiringVerifier{expired: map[string]types.Token{
		"old":      {ID: "t1", Subject: "jfac@x/users/dev"},
		"disabled": {ID: "t2", Subject: "jfac@x/users/gone"},
	}}
	a, _ := newAuth(t, db, Options{Tokens: v})
	want := Refresher{User: "dev", TokenID: "t1"}
	if r, ok := a.ExpiredToken(ctx, "old"); !ok || r != want {
		t.Fatalf("an expired token = %+v, %v", r, ok)
	}
	for _, raw := range []string{"disabled", "forged"} {
		if r, ok := a.ExpiredToken(ctx, raw); ok {
			t.Errorf("%s = %+v", raw, r)
		}
	}
	// A verifier without VerifyExpired accepts no expired token.
	plain, _ := newAuth(t, db, Options{Tokens: fakeVerifier{}})
	if _, ok := plain.ExpiredToken(ctx, "old"); ok {
		t.Fatal("a plain verifier verified an expired token")
	}

	if _, ok := ExpiredTokenFrom(ctx); ok {
		t.Fatal("an empty context carries an expired token")
	}
	if r, ok := ExpiredTokenFrom(WithExpiredToken(ctx, want)); !ok || r != want {
		t.Fatalf("ExpiredTokenFrom = %+v, %v", r, ok)
	}
}

// TestEncryptedPasswordToken: an encrypted password works exactly when the
// password it stands for would.
func TestEncryptedPasswordToken(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	a, clk := newAuth(t, db, Options{})
	addUser(t, db, types.User{Name: "dev", PasswordChanged: clk.t.AddDate(0, 0, -10)}, "pw")
	addUser(t, db, types.User{Name: "sso", InternalPasswordDisabled: true}, "pw")
	addUser(t, db, types.User{Name: "nopass"}, "")
	addUser(t, db, types.User{Name: "forced", PasswordExpired: true}, "pw")
	v := fakeVerifier{}
	for _, n := range []string{"dev", "sso", "nopass", "forced"} {
		v[n] = types.Token{
			ID: n, Subject: "jfac@x/users/" + n, Scope: types.UserScope(),
			Purpose: types.TokenPurposeEncryptedPassword,
		}
	}
	a.tokens = v
	if p, err := a.Token(ctx, "dev", "dev"); err != nil || p.Name != "dev" || p.TokenID != "dev" {
		t.Fatalf("dev = %+v, %v", p, err)
	}
	for raw, want := range map[string]error{
		"sso": ErrBadCredentials, "nopass": ErrBadCredentials, "forced": ErrPasswordExpired,
	} {
		if _, err := a.Token(ctx, raw, ""); !errors.Is(err, want) {
			t.Errorf("%s: %v, want %v", raw, err, want)
		}
	}
	// The age policy expires the password with no write that could revoke
	// its encrypted password.
	if err := db.Settings().Put(ctx, types.SettingPasswordExpirationPolicy,
		types.PasswordExpirationPolicy{Enabled: true, MaxAgeDays: 5}); err != nil {
		t.Fatal(err)
	}
	a.ForgetPolicies()
	if _, err := a.Token(ctx, "dev", ""); !errors.Is(err, ErrPasswordExpired) {
		t.Fatalf("a password past the policy's age: %v", err)
	}

	broken, err := NewAuthenticator(db.Users(), failingSettings{db.Settings()}, Options{Tokens: v})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broken.Token(ctx, "dev", ""); err == nil || errors.Is(err, ErrBadCredentials) {
		t.Fatalf("a policy that cannot be read: %v", err)
	}
}
