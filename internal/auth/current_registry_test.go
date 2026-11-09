package auth

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/access/core/model"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/settings"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

func TestUnitCurrentRegistryKeepsGrantAndRechecksCredential(t *testing.T) {
	for _, change := range []string{"unchanged", "user groups", "user disabled", "user deleted", "password changed", "expired", "parent revoked", "parent expired", "parent scope", "parent identity", "anonymous disabled"} {
		t.Run(change, func(t *testing.T) {
			ctx := t.Context()
			db := inmem.New()
			v := fakeVerifier{}
			a, clock := newAuth(
				t,
				db,
				Options{Tokens: v, Hash: HashParams{Time: 1, MemoryKiB: 8, Threads: 1}},
			)
			name := "user"
			if change == "anonymous disabled" {
				name = kernel.AnonymousUser
				if err := db.Settings().
					Put(ctx, settings.SettingAnonymousAccess, true); err != nil {
					t.Fatal(err)
				}
			}
			addUser(t, db, model.User{Name: name, Admin: true}, "pw")
			u, err := db.Users().Get(ctx, name)
			if err != nil {
				t.Fatal(err)
			}
			rec := model.Token{
				ID:        "registry",
				Subject:   "jfac@test/users/" + name,
				Scope:     kernel.UserScope(),
				ExpiresAt: clock.now().Add(time.Hour),
				Registry: &kernel.RegistryAccess{
					Realm: "remote",
					Grants: []kernel.RegistryGrant{
						{Type: "repository", Name: "image", Actions: []string{"pull"}},
					},
				},
			}
			parent := model.Token{
				ID:        "parent",
				Subject:   rec.Subject,
				Scope:     rec.Scope,
				ExpiresAt: rec.ExpiresAt,
			}
			if len(change) >= 6 && change[:6] == "parent" {
				rec.Parent = parent.ID
				if err := db.Tokens().Create(ctx, parent); err != nil {
					t.Fatal(err)
				}
			}
			v["raw"] = rec
			p, err := a.Token(ctx, "raw", "")
			if err != nil {
				t.Fatal(err)
			}
			// Durable work must retain the originally verified narrowing grant,
			// while rechecking all live authority after serialization/restart.
			encoded, err := json.Marshal(p.CredentialReference())
			if err != nil {
				t.Fatal(err)
			}
			var reference kernel.CredentialReference
			if err = json.Unmarshal(encoded, &reference); err != nil {
				t.Fatal(err)
			}
			p = reference.Principal()
			if _, err = a.Current(ctx, p, db.Tokens()); err == nil {
				t.Fatal("registry reference became a general credential")
			}
			switch change {
			case "user groups":
				u.Admin = false
				if err := db.Groups().Create(ctx, model.Group{Name: "new"}); err != nil {
					t.Fatal(err)
				}
				u.Groups = []string{"new"}
			case "user disabled":
				u.Status = model.UserDisabled
			case "password changed":
				u.PasswordHash = []byte("different")
			case "expired":
				clock.t = rec.ExpiresAt
			case "parent expired":
				parent.ExpiresAt = clock.now()
			case "parent scope":
				parent.Scope = scope(t, "artifact:other:r")
			case "parent identity":
				parent.Identity = kernel.IdentitySynthetic
			case "anonymous disabled":
				if err := db.Settings().
					Put(ctx, settings.SettingAnonymousAccess, false); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Users().Update(ctx, u); err != nil {
				t.Fatal(err)
			}
			if change == "user deleted" {
				if err := db.Users().Delete(ctx, name); err != nil {
					t.Fatal(err)
				}
			}
			if rec.Parent != "" {
				if err := db.Tokens().Delete(ctx, parent.ID); err != nil {
					t.Fatal(err)
				}
				if change != "parent revoked" {
					if err := db.Tokens().Create(ctx, parent); err != nil {
						t.Fatal(err)
					}
				}
			}
			fresh, err := a.CurrentRegistry(ctx, p, db.Tokens())
			if change != "unchanged" && change != "user groups" {
				if err == nil {
					t.Fatal("changed credential accepted")
				}
				return
			}
			if err != nil || !reflect.DeepEqual(fresh.Registry, p.Registry) ||
				fresh.TokenExpires != p.TokenExpires ||
				fresh.Scope.String() != p.Scope.String() {
				t.Fatalf("current grant: %+v %v", fresh, err)
			}
			if change == "user groups" &&
				(fresh.Admin || !reflect.DeepEqual(fresh.Groups, []string{"new"})) {
				t.Fatalf("stale user: %+v", fresh)
			}
			if _, err := a.Current(ctx, fresh, db.Tokens()); err == nil {
				t.Fatal("registry token became generic credential")
			}
			for _, unverified := range []kernel.Principal{{Registry: p.Registry}, {Name: p.Name, TokenID: p.TokenID, TokenExpires: p.TokenExpires, Registry: p.Registry}} {
				if _, err := a.CurrentRegistry(ctx, unverified, db.Tokens()); err == nil {
					t.Fatal("unverified principal accepted")
				}
			}
		})
	}
}
