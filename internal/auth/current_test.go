package auth

import (
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitCurrentPrincipalRechecksIdentity(t *testing.T) {
	ctx := t.Context()
	db := inmem.New()
	a, clock := newAuth(t, db, Options{Hash: HashParams{Time: 1, MemoryKiB: 8, Threads: 1}})
	addUser(t, db, types.User{Name: "user", Admin: true}, "pw")
	p, err := a.Basic(ctx, "user", "pw")
	if err != nil {
		t.Fatal(err)
	}
	u, err := db.Users().Get(ctx, "user")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Groups().Create(ctx, types.Group{Name: "new"}); err != nil {
		t.Fatal(err)
	}
	u.Admin = false
	u.Groups = []string{"new"}
	if err = db.Users().Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	fresh, err := a.Current(ctx, p, db.Tokens())
	if err != nil || fresh.Admin || len(fresh.Groups) != 1 {
		t.Fatalf("current %+v %v", fresh, err)
	}
	for _, change := range []string{"disabled", "hash", "password off", "basic off", "deleted"} {
		t.Run(change, func(t *testing.T) {
			changed := u
			switch change {
			case "disabled":
				changed.Status = types.UserDisabled
			case "hash":
				changed.PasswordHash = []byte("changed")
			case "password off":
				changed.InternalPasswordDisabled = true
			case "basic off":
				if err := db.Settings().Put(ctx, types.SettingBasicAuthEnabled, false); err != nil {
					t.Fatal(err)
				}
			case "deleted":
				if err := db.Users().Delete(ctx, u.Name); err != nil {
					t.Fatal(err)
				}
			}
			if change != "deleted" {
				if err := db.Users().Update(ctx, changed); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := a.Current(ctx, p, db.Tokens()); err == nil {
				t.Fatal("revoked password accepted")
			}
			if change == "deleted" {
				if err := db.Users().Create(ctx, u); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := db.Users().Update(ctx, u); err != nil {
					t.Fatal(err)
				}
			}
			if change == "basic off" {
				if err := db.Settings().Delete(ctx, types.SettingBasicAuthEnabled); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	for _, p := range []Principal{{Name: "user"}, {Registry: &types.RegistryAccess{}}} {
		if _, err := a.Current(ctx, p, db.Tokens()); err == nil {
			t.Fatal("unverified accepted")
		}
	}
	if _, err := a.Current(ctx, Anonymous, db.Tokens()); err == nil {
		t.Fatal("anonymous while off")
	}
	if err := db.Settings().Put(ctx, types.SettingAnonymousAccess, true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Current(ctx, Anonymous, db.Tokens()); err != nil {
		t.Fatal(err)
	}
	tok := types.Token{
		ID:        "current",
		Subject:   "jfac@test/users/user",
		Scope:     scope(t, "applied-permissions/user"),
		ExpiresAt: clock.now().Add(time.Hour),
	}
	if err := db.Tokens().Create(ctx, tok); err != nil {
		t.Fatal(err)
	}
	p = tokenPrincipal(u, tok.Scope)
	p.TokenID = tok.ID
	if fresh, err = a.Current(
		ctx,
		p,
		db.Tokens(),
	); err != nil || fresh.Subject().Scope == nil ||
		fresh.TokenID != tok.ID {
		t.Fatalf("token %+v %v", fresh, err)
	}
	clock.t = clock.now().Add(2 * time.Hour)
	if _, err = a.Current(ctx, p, db.Tokens()); err == nil {
		t.Fatal("expired accepted")
	}
	if err = db.Tokens().Delete(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Current(ctx, p, db.Tokens()); err == nil {
		t.Fatal("revoked accepted")
	}
}
