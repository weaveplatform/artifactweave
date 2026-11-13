package authn

import (
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

// TestUnitUsernamesFold: a password or token login names its user in any
// case and acts as the stored user (#1432). A transient or synthetic subject,
// which no stored row names, keeps its spelling.
func TestUnitUsernamesFold(t *testing.T) {
	ctx := t.Context()
	db := inmem.New()
	addUser(t, db, model.User{Name: "dev", Status: model.UserEnabled}, "pw")
	const iss = "jfac@x/users/"
	v := fakeVerifier{
		"dev":   {Subject: iss + "dev", Scope: kernel.UserScope()},
		"upper": {Subject: iss + "DEV", Scope: kernel.UserScope()},
		"transient": {
			Subject: iss + "Robot", Scope: kernel.UserScope(), Identity: kernel.IdentityTransient,
		},
		"synthetic": {
			Subject: iss + "Oidc-CI", Scope: kernel.UserScope(), Identity: kernel.IdentitySynthetic,
		},
	}
	a, clock := newAuth(t, db, Options{Tokens: v, APIRestrictions: db.APIRestrictions()})
	for _, tc := range []struct {
		what string
		got  func() (kernel.Principal, error)
		want string
	}{
		{"Basic DEV", func() (kernel.Principal, error) { return a.Basic(ctx, "DEV", "pw") }, "dev"},
		{"Basic Dev", func() (kernel.Principal, error) { return a.Basic(ctx, "Dev", "pw") }, "dev"},
		{"token as DEV", func() (kernel.Principal, error) { return a.Token(ctx, "dev", "DEV") }, "dev"},
		{"subject DEV", func() (kernel.Principal, error) { return a.Token(ctx, "upper", "dev") }, "dev"},
		{"transient", func() (kernel.Principal, error) { return a.Token(ctx, "transient", "ROBOT") }, "Robot"},
		{"synthetic", func() (kernel.Principal, error) { return a.Token(ctx, "synthetic", "oidc-ci") }, "Oidc-CI"},
		{"current DEV", func() (kernel.Principal, error) {
			p, err := a.Basic(ctx, "dev", "pw")
			if err != nil {
				return p, err
			}
			p.Name = "DEV"
			return a.Current(ctx, p, db.Tokens())
		}, "dev"},
	} {
		if p, err := tc.got(); err != nil || p.Name != tc.want {
			t.Errorf("%s: %+v, %v; want %s", tc.what, p, err, tc.want)
		}
	}
	if _, err := a.Token(ctx, "dev", "devs"); !errors.Is(err, kernel.ErrBadCredentials) {
		t.Errorf("another user's name: %v", err)
	}

	// dev's restriction holds DEV.
	if _, err := db.APIRestrictions().Put(ctx, port.APIRestriction{
		User: "dev", Actor: "admin", Reason: "Poll overload", Changed: clock.t,
		Until: clock.t.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if err := CheckAPIRestriction(ctx, db.APIRestrictions(), "DEV", clock.t); !errors.Is(
		err, kernel.ErrAPIRestricted) {
		t.Errorf("DEV under dev's restriction: %v", err)
	}
}

// TestUnitUsernameLockFolds: failed logins as DEV and Dev count toward dev's
// lock. RTFACT-29660 reports 7.55.13 counting them apart, against Access's
// own rule that user names are case-insensitive; that is not followed.
func TestUnitUsernameLockFolds(t *testing.T) {
	ctx := t.Context()
	db := inmem.New()
	addUser(t, db, model.User{Name: "dev", Status: model.UserEnabled}, "pw")
	noSuspension(t, db)
	if err := db.Settings().Put(ctx, settings.SettingUserLockPolicy,
		model.UserLockPolicy{Enabled: true, LoginAttempts: 2}); err != nil {
		t.Fatal(err)
	}
	a, _ := newAuth(t, db, Options{})
	for _, name := range []string{"DEV", "Dev"} {
		if _, err := a.Basic(ctx, name, "wrong"); !errors.Is(err, kernel.ErrBadCredentials) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := a.Basic(ctx, "dev", "pw"); !errors.Is(err, kernel.ErrAccountLocked) {
		t.Fatalf("dev after two failures as DEV and Dev: %v", err)
	}
}
