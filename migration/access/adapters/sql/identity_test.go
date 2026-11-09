package sql_test

import (
	"context"
	"errors"
	"testing"

	accsql "github.com/weaveplatform/artifactweave/migration/access/adapters/sql"
	"github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/core/model"
	"github.com/weaveplatform/artifactweave/migration/access/core/port"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/crypt"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/sqltest"
)

// identityStore opens a SQLite store, sealed with the test keyring, that
// holds the access module's tables alone and the fixtures every fault case
// starts from: alice in g1, a permission with a grant and a target, a token,
// a key pair, a default signing key and a certificate.
func identityStore(t *testing.T) *accsql.Store {
	t.Helper()
	ctx := context.Background()
	s := accsql.New(sqltest.OpenSQLite(t, accsql.Module))
	for _, err := range []error{
		s.Groups().Create(ctx, model.Group{Name: "g1"}),
		s.Users().Create(ctx, model.User{Name: "alice", Groups: []string{"g1"}}),
		s.Permissions().Create(ctx, permissionFixture("p")),
		s.Tokens().Create(ctx, model.Token{ID: "t1", Scope: kernel.UserScope()}),
		s.Keys().CreateKeypair(ctx, api.Keypair{Name: "kp", PrivateKey: "secret", Passphrase: "pass"}),
		s.Keys().PutSigningKey(ctx, model.SigningKey{Alias: "sk", PrivateKey: "secret", IsDefault: true}),
		s.Keys().PutCertificate(ctx, api.Certificate{Alias: "cert", PEM: "pem", PrivateKey: "secret"}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func permissionFixture(name string) model.Permission {
	return model.Permission{Name: name, Resources: map[api.ResourceType]model.Resource{
		api.ResourceArtifact: {
			Actions: model.Grants{Users: map[string]api.Actions{"alice": api.ActionRead}},
			Targets: map[string]model.Target{"libs-local": {Include: []string{"**"}}},
		},
	}}
}

// TestAccIdentityFaults checks that a failing statement anywhere inside an
// identity operation surfaces as an error, rather than being dropped and
// leaving a half-written record.
func TestAccIdentityFaults(t *testing.T) {
	ctx := context.Background()
	dropMembers := sqltest.DropTable("group_members")
	for _, c := range []struct {
		name     string
		sabotage []string
		call     func(port.Store) error
	}{
		{"user create group lookup", []string{dropMembers, sqltest.DropTable("user_groups")}, func(s port.Store) error {
			return s.Users().Create(ctx, model.User{Name: "bob", Groups: []string{"g1"}})
		}},
		{"user create membership", []string{sqltest.FailTrigger("INSERT", "group_members")}, func(s port.Store) error {
			return s.Users().Create(ctx, model.User{Name: "bob", Groups: []string{"g1"}})
		}},
		{"user get memberships", []string{dropMembers}, func(s port.Store) error {
			_, err := s.Users().Get(ctx, "alice")
			return err
		}},
		{"user get unreadable membership", []string{
			dropMembers,
			"CREATE TABLE group_members (group_name TEXT, user_name TEXT)",
			"INSERT INTO group_members VALUES (NULL, 'alice')",
		}, func(s port.Store) error {
			_, err := s.Users().Get(ctx, "alice")
			return err
		}},
		{"user update group lookup", []string{dropMembers, sqltest.DropTable("user_groups")}, func(s port.Store) error {
			return s.Users().Update(ctx, model.User{Name: "alice", Groups: []string{"g1"}})
		}},
		{"user update clear", []string{dropMembers}, func(s port.Store) error {
			return s.Users().Update(ctx, model.User{Name: "alice"})
		}},
		{"user update membership", []string{sqltest.FailTrigger("INSERT", "group_members")}, func(s port.Store) error {
			return s.Users().Update(ctx, model.User{Name: "alice", Groups: []string{"g1"}})
		}},
		{"user delete", []string{dropMembers}, func(s port.Store) error {
			return s.Users().Delete(ctx, "alice")
		}},
		{"user list", []string{dropMembers, sqltest.DropTable("users")}, func(s port.Store) error {
			_, _, err := s.Users().List(ctx, kernel.Page{})
			return err
		}},
		{"user list memberships", []string{dropMembers}, func(s port.Store) error {
			_, _, err := s.Users().List(ctx, kernel.Page{})
			return err
		}},
		{"group create user lookup", []string{dropMembers, sqltest.DropTable("users")}, func(s port.Store) error {
			return s.Groups().Create(ctx, model.Group{Name: "g2", Members: []string{"alice"}})
		}},
		{"group create membership", []string{sqltest.FailTrigger("INSERT", "group_members")}, func(s port.Store) error {
			return s.Groups().Create(ctx, model.Group{Name: "g2", Members: []string{"alice"}})
		}},
		{"group get memberships", []string{dropMembers}, func(s port.Store) error {
			_, err := s.Groups().Get(ctx, "g1")
			return err
		}},
		{"group update user lookup", []string{dropMembers, sqltest.DropTable("users")}, func(s port.Store) error {
			return s.Groups().Update(ctx, model.Group{Name: "g1", Members: []string{"alice"}})
		}},
		{"group update clear", []string{dropMembers}, func(s port.Store) error {
			return s.Groups().Update(ctx, model.Group{Name: "g1"})
		}},
		{"group update membership", []string{sqltest.FailTrigger("INSERT", "group_members")}, func(s port.Store) error {
			return s.Groups().Update(ctx, model.Group{Name: "g1", Members: []string{"alice"}})
		}},
		{"group delete", []string{dropMembers}, func(s port.Store) error {
			return s.Groups().Delete(ctx, "g1")
		}},
		{"group list", []string{dropMembers, sqltest.DropTable("user_groups")}, func(s port.Store) error {
			_, _, err := s.Groups().List(ctx, kernel.Page{})
			return err
		}},
		{"group list memberships", []string{dropMembers}, func(s port.Store) error {
			_, _, err := s.Groups().List(ctx, kernel.Page{})
			return err
		}},
		{"group auto-join", []string{dropMembers, sqltest.DropTable("user_groups")}, func(s port.Store) error {
			_, err := s.Groups().AutoJoin(ctx)
			return err
		}},
		{"remove members", []string{dropMembers}, func(s port.Store) error {
			return s.Groups().RemoveMembers(ctx, "g1", "alice")
		}},
		{"add members user lookup", []string{dropMembers, sqltest.DropTable("users")}, func(s port.Store) error {
			return s.Groups().AddMembers(ctx, "g1", "alice")
		}},
		{"permission mirror clear", []string{sqltest.DropTable("permission_grants")}, func(s port.Store) error {
			return s.Permissions().Create(ctx, permissionFixture("p2"))
		}},
		{"permission mirror grant", []string{sqltest.FailTrigger("INSERT", "permission_grants")}, func(s port.Store) error {
			return s.Permissions().Update(ctx, permissionFixture("p"))
		}},
		{"permission mirror target", []string{sqltest.FailTrigger("INSERT", "permission_targets")}, func(s port.Store) error {
			return s.Permissions().Create(ctx, permissionFixture("p2"))
		}},
		{"permission insert", []string{sqltest.FailTrigger("INSERT", "permissions")}, func(s port.Store) error {
			return s.Permissions().Create(ctx, permissionFixture("p2"))
		}},
		{"permission update", []string{sqltest.FailTrigger("UPDATE", "permissions")}, func(s port.Store) error {
			return s.Permissions().Update(ctx, permissionFixture("p"))
		}},
		{"permission delete", []string{sqltest.DropTable("permission_grants")}, func(s port.Store) error {
			return s.Permissions().Delete(ctx, "p")
		}},
		{"permission list", []string{
			sqltest.DropTable("permission_grants"),
			sqltest.DropTable("permission_targets"),
			sqltest.DropTable("permissions"),
		}, func(s port.Store) error {
			_, _, err := s.Permissions().List(ctx, kernel.Page{})
			return err
		}},
		{"token unreadable scope", []string{"UPDATE tokens SET scope = 'not a scope'"}, func(s port.Store) error {
			_, err := s.Tokens().Get(ctx, "t1")
			return err
		}},
		{"token list", []string{sqltest.DropTable("tokens")}, func(s port.Store) error {
			_, _, err := s.Tokens().List(ctx, port.TokenFilter{}, kernel.Page{})
			return err
		}},
		{"keypair moved to another row", []string{"UPDATE keypairs SET name = 'moved'"}, func(s port.Store) error {
			_, err := s.Keys().Keypair(ctx, "moved")
			return err
		}},
		{"keypair list unseal", []string{"UPDATE keypairs SET name = 'moved'"}, func(s port.Store) error {
			_, err := s.Keys().Keypairs(ctx)
			return err
		}},
		{"keypair passphrase moved", []string{
			"INSERT INTO keypairs (name, private_key, passphrase) SELECT 'other', NULL, passphrase FROM keypairs",
		}, func(s port.Store) error {
			_, err := s.Keys().Keypair(ctx, "other")
			return err
		}},
		{"signing key moved to another row", []string{"UPDATE signing_keys SET alias = 'moved'"}, func(s port.Store) error {
			_, err := s.Keys().SigningKey(ctx, "moved")
			return err
		}},
		{"signing key list unseal", []string{"UPDATE signing_keys SET alias = 'moved'"}, func(s port.Store) error {
			_, err := s.Keys().SigningKeys(ctx)
			return err
		}},
		{"signing key clear default", []string{sqltest.FailTrigger("UPDATE", "signing_keys")}, func(s port.Store) error {
			return s.Keys().PutSigningKey(ctx, model.SigningKey{Alias: "new", IsDefault: true})
		}},
		{"certificate moved to another row", []string{"UPDATE certificates SET alias = 'moved'"}, func(s port.Store) error {
			_, err := s.Keys().Certificate(ctx, "moved")
			return err
		}},
		{"certificate list unseal", []string{"UPDATE certificates SET alias = 'moved'"}, func(s port.Store) error {
			_, err := s.Keys().Certificates(ctx)
			return err
		}},
		{"certificate list", []string{sqltest.DropTable("certificates")}, func(s port.Store) error {
			_, err := s.Keys().Certificates(ctx)
			return err
		}},
		{"trusted key list", []string{sqltest.DropTable("trusted_keys")}, func(s port.Store) error {
			_, err := s.Keys().TrustedKeys(ctx)
			return err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := identityStore(t)
			sqltest.Exec(t, s.DB(), c.sabotage...)
			err := c.call(s)
			if err == nil {
				t.Fatal("the injected fault was not reported")
			}
			if errors.Is(err, kernel.ErrNotFound) || errors.Is(err, kernel.ErrConflict) {
				t.Fatalf("the fault surfaced as a store sentinel: %v", err)
			}
		})
	}
}

// TestAccSealedIdentityColumns checks that key material is sealed at rest
// and that a second store over the same database, without the keyring,
// cannot read it.
func TestAccSealedIdentityColumns(t *testing.T) {
	ctx := context.Background()
	s := identityStore(t)
	for _, q := range []string{
		"SELECT private_key FROM keypairs WHERE name = 'kp'",
		"SELECT passphrase FROM keypairs WHERE name = 'kp'",
		"SELECT private_key FROM signing_keys WHERE alias = 'sk'",
		"SELECT private_key FROM certificates WHERE alias = 'cert'",
	} {
		var raw []byte
		if err := s.DB().QueryRowContext(ctx, q).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if !crypt.IsSealed(raw) {
			t.Fatalf("%s: stored in plain text", q)
		}
	}
	bare := accsql.New(dialect.New(s.DB(), s.Dialect(), nil))
	if _, err := bare.Keys().Keypair(ctx, "kp"); !errors.Is(err, dialect.ErrNoKeyring) {
		t.Fatalf("read without a keyring: %v", err)
	}
}
