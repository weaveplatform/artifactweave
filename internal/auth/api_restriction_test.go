package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitAPIRestrictionCredentialsAndRecovery(t *testing.T) {
	ctx := t.Context()
	db := inmem.New()
	addUser(t, db, types.User{Name: "dev"}, "pw")
	v := fakeVerifier{"dev-token": {Subject: "jfac@x/users/dev", Scope: types.UserScope()}}
	a, clock := newAuth(t, db, Options{APIRestrictions: db.APIRestrictions(), Tokens: v})
	p, err := a.Basic(ctx, "dev", "pw")
	if err != nil {
		t.Fatal(err)
	}
	r := store.APIRestriction{
		User:    "dev",
		Actor:   "admin",
		Reason:  "Poll overload",
		Changed: clock.t,
		Until:   clock.t.Add(time.Minute),
	}
	if _, err := db.APIRestrictions().Put(ctx, r); err != nil {
		t.Fatal(err)
	}
	for name, check := range map[string]func() error{
		"cached password": func() error { _, err := a.Basic(ctx, "dev", "pw"); return err },
		"token":           func() error { _, err := a.Token(ctx, "dev-token", ""); return err },
		"current":         func() error { _, err := a.Current(ctx, p, db.Tokens()); return err },
	} {
		if err := check(); !errors.Is(err, ErrAPIRestricted) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := a.Basic(ctx, "dev", "wrong"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("restriction disclosed before verification: %v", err)
	}
	recovery := WithAPIRecovery(ctx)
	verified, err := a.Basic(recovery, "dev", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RequireAdmin(WithPrincipal(recovery, verified)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("recovery granted admin: %v", err)
	}
	if _, err := a.Token(recovery, "dev-token", ""); err != nil {
		t.Fatal(err)
	}
	clock.t = r.Until
	if _, err := a.Basic(ctx, "dev", "pw"); err != nil {
		t.Fatalf("expired restriction: %v", err)
	}
	if _, err := a.Token(ctx, "dev-token", ""); err != nil {
		t.Fatal(err)
	}
	if err := CheckAPIRestriction(
		ctx,
		brokenRestrictions{},
		"dev",
		clock.t,
	); !errors.Is(
		err,
		context.DeadlineExceeded,
	) {
		t.Fatalf("backend failure: %v", err)
	}
}

type brokenRestrictions struct{ store.APIRestrictionStore }

func (brokenRestrictions) Get(context.Context, string) (store.APIRestriction, error) {
	return store.APIRestriction{}, context.DeadlineExceeded
}
