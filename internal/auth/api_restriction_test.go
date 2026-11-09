package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/access/core/model"
	"github.com/weaveplatform/artifactweave/migration/access/core/port"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

func TestUnitAPIRestrictionCredentialsAndRecovery(t *testing.T) {
	ctx := t.Context()
	db := inmem.New()
	addUser(t, db, model.User{Name: "dev"}, "pw")
	v := fakeVerifier{"dev-token": {Subject: "jfac@x/users/dev", Scope: kernel.UserScope()}}
	a, clock := newAuth(t, db, Options{APIRestrictions: db.APIRestrictions(), Tokens: v})
	p, err := a.Basic(ctx, "dev", "pw")
	if err != nil {
		t.Fatal(err)
	}
	r := port.APIRestriction{
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
		if err := check(); !errors.Is(err, kernel.ErrAPIRestricted) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := a.Basic(ctx, "dev", "wrong"); !errors.Is(err, kernel.ErrBadCredentials) {
		t.Fatalf("restriction disclosed before verification: %v", err)
	}
	recovery := WithAPIRecovery(ctx)
	verified, err := a.Basic(recovery, "dev", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.RequireAdmin(
		kernel.WithPrincipal(recovery, verified),
	); !errors.Is(
		err,
		kernel.ErrForbidden,
	) {
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

type brokenRestrictions struct{ port.APIRestrictionStore }

func (brokenRestrictions) Get(context.Context, string) (port.APIRestriction, error) {
	return port.APIRestriction{}, context.DeadlineExceeded
}
