package sqlcommon_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/crypt"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// failInsert and failUpdate make every INSERT or UPDATE on a table fail
// while its reads and deletes keep working, so a test can reach the write
// that follows a successful read.
func failInsert(table string) string {
	return "CREATE TRIGGER fail_" + table + " BEFORE INSERT ON " + table +
		" BEGIN SELECT RAISE(ABORT, 'injected'); END"
}

func failUpdate(table string) string {
	return "CREATE TRIGGER fail_update_" + table + " BEFORE UPDATE ON " + table +
		" BEGIN SELECT RAISE(ABORT, 'injected'); END"
}

// identityStore opens a SQLite store with a keyring and the fixtures every
// fault case starts from: alice in g1, a permission with a grant and a
// target, a token, a key pair and a default signing key.
func identityStore(t *testing.T) *sqlcommon.Store {
	t.Helper()
	ctx := context.Background()
	k, err := crypt.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "aw.db"),
		sqlite.Options{Options: sqlcommon.Options{Keyring: k}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, err := range []error{
		s.Groups().Create(ctx, types.Group{Name: "g1"}),
		s.Users().Create(ctx, types.User{Name: "alice", Groups: []string{"g1"}}),
		s.Permissions().Create(ctx, permissionFixture("p")),
		s.Tokens().Create(ctx, types.Token{ID: "t1", Scope: types.UserScope()}),
		s.Keys().CreateKeypair(ctx, types.Keypair{Name: "kp", PrivateKey: "secret", Passphrase: "pass"}),
		s.Keys().PutSigningKey(ctx, types.SigningKey{Alias: "sk", PrivateKey: "secret", IsDefault: true}),
		s.Keys().PutCertificate(ctx, types.Certificate{Alias: "cert", PEM: "pem", PrivateKey: "secret"}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func permissionFixture(name string) types.Permission {
	return types.Permission{Name: name, Resources: map[types.ResourceType]types.Resource{
		types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"alice": types.ActionRead}},
			Targets: map[string]types.Target{"libs-local": {Include: []string{"**"}}},
		},
	}}
}

// TestAccIdentityFaults checks that a failing statement anywhere inside an
// identity operation surfaces as an error, rather than being dropped and
// leaving a half-written record.
func TestAccIdentityFaults(t *testing.T) {
	ctx := context.Background()
	dropMembers := "DROP TABLE group_members"
	for _, c := range []struct {
		name     string
		sabotage []string
		call     func(store.Store) error
	}{
		{"user create group lookup", []string{dropMembers, "DROP TABLE user_groups"}, func(s store.Store) error {
			return s.Users().Create(ctx, types.User{Name: "bob", Groups: []string{"g1"}})
		}},
		{"user create membership", []string{failInsert("group_members")}, func(s store.Store) error {
			return s.Users().Create(ctx, types.User{Name: "bob", Groups: []string{"g1"}})
		}},
		{"user get memberships", []string{dropMembers}, func(s store.Store) error {
			_, err := s.Users().Get(ctx, "alice")
			return err
		}},
		{"user get unreadable membership", []string{
			dropMembers,
			"CREATE TABLE group_members (group_name TEXT, user_name TEXT)",
			"INSERT INTO group_members VALUES (NULL, 'alice')",
		}, func(s store.Store) error {
			_, err := s.Users().Get(ctx, "alice")
			return err
		}},
		{"user update group lookup", []string{dropMembers, "DROP TABLE user_groups"}, func(s store.Store) error {
			return s.Users().Update(ctx, types.User{Name: "alice", Groups: []string{"g1"}})
		}},
		{"user update clear", []string{dropMembers}, func(s store.Store) error {
			return s.Users().Update(ctx, types.User{Name: "alice"})
		}},
		{"user update membership", []string{failInsert("group_members")}, func(s store.Store) error {
			return s.Users().Update(ctx, types.User{Name: "alice", Groups: []string{"g1"}})
		}},
		{"user delete", []string{dropMembers}, func(s store.Store) error {
			return s.Users().Delete(ctx, "alice")
		}},
		{"user list", []string{dropMembers, "DROP TABLE users"}, func(s store.Store) error {
			_, _, err := s.Users().List(ctx, store.Page{})
			return err
		}},
		{"user list memberships", []string{dropMembers}, func(s store.Store) error {
			_, _, err := s.Users().List(ctx, store.Page{})
			return err
		}},
		{"group create user lookup", []string{dropMembers, "DROP TABLE users"}, func(s store.Store) error {
			return s.Groups().Create(ctx, types.Group{Name: "g2", Members: []string{"alice"}})
		}},
		{"group create membership", []string{failInsert("group_members")}, func(s store.Store) error {
			return s.Groups().Create(ctx, types.Group{Name: "g2", Members: []string{"alice"}})
		}},
		{"group get memberships", []string{dropMembers}, func(s store.Store) error {
			_, err := s.Groups().Get(ctx, "g1")
			return err
		}},
		{"group update user lookup", []string{dropMembers, "DROP TABLE users"}, func(s store.Store) error {
			return s.Groups().Update(ctx, types.Group{Name: "g1", Members: []string{"alice"}})
		}},
		{"group update clear", []string{dropMembers}, func(s store.Store) error {
			return s.Groups().Update(ctx, types.Group{Name: "g1"})
		}},
		{"group update membership", []string{failInsert("group_members")}, func(s store.Store) error {
			return s.Groups().Update(ctx, types.Group{Name: "g1", Members: []string{"alice"}})
		}},
		{"group delete", []string{dropMembers}, func(s store.Store) error {
			return s.Groups().Delete(ctx, "g1")
		}},
		{"group list", []string{dropMembers, "DROP TABLE user_groups"}, func(s store.Store) error {
			_, _, err := s.Groups().List(ctx, store.Page{})
			return err
		}},
		{"group list memberships", []string{dropMembers}, func(s store.Store) error {
			_, _, err := s.Groups().List(ctx, store.Page{})
			return err
		}},
		{"group auto-join", []string{dropMembers, "DROP TABLE user_groups"}, func(s store.Store) error {
			_, err := s.Groups().AutoJoin(ctx)
			return err
		}},
		{"remove members", []string{dropMembers}, func(s store.Store) error {
			return s.Groups().RemoveMembers(ctx, "g1", "alice")
		}},
		{"add members user lookup", []string{dropMembers, "DROP TABLE users"}, func(s store.Store) error {
			return s.Groups().AddMembers(ctx, "g1", "alice")
		}},
		{"permission mirror clear", []string{"DROP TABLE permission_grants"}, func(s store.Store) error {
			return s.Permissions().Create(ctx, permissionFixture("p2"))
		}},
		{"permission mirror grant", []string{failInsert("permission_grants")}, func(s store.Store) error {
			return s.Permissions().Update(ctx, permissionFixture("p"))
		}},
		{"permission mirror target", []string{failInsert("permission_targets")}, func(s store.Store) error {
			return s.Permissions().Create(ctx, permissionFixture("p2"))
		}},
		{"permission insert", []string{failInsert("permissions")}, func(s store.Store) error {
			return s.Permissions().Create(ctx, permissionFixture("p2"))
		}},
		{"permission update", []string{failUpdate("permissions")}, func(s store.Store) error {
			return s.Permissions().Update(ctx, permissionFixture("p"))
		}},
		{"permission delete", []string{"DROP TABLE permission_grants"}, func(s store.Store) error {
			return s.Permissions().Delete(ctx, "p")
		}},
		{"permission list", []string{
			"DROP TABLE permission_grants", "DROP TABLE permission_targets", "DROP TABLE permissions",
		}, func(s store.Store) error {
			_, _, err := s.Permissions().List(ctx, store.Page{})
			return err
		}},
		{"token unreadable scope", []string{"UPDATE tokens SET scope = 'not a scope'"}, func(s store.Store) error {
			_, err := s.Tokens().Get(ctx, "t1")
			return err
		}},
		{"token list", []string{"DROP TABLE tokens"}, func(s store.Store) error {
			_, _, err := s.Tokens().List(ctx, store.TokenFilter{}, store.Page{})
			return err
		}},
		{"keypair moved to another row", []string{"UPDATE keypairs SET name = 'moved'"}, func(s store.Store) error {
			_, err := s.Keys().Keypair(ctx, "moved")
			return err
		}},
		{"keypair list unseal", []string{"UPDATE keypairs SET name = 'moved'"}, func(s store.Store) error {
			_, err := s.Keys().Keypairs(ctx)
			return err
		}},
		{"keypair passphrase moved", []string{
			"INSERT INTO keypairs (name, private_key, passphrase) SELECT 'other', NULL, passphrase FROM keypairs",
		}, func(s store.Store) error {
			_, err := s.Keys().Keypair(ctx, "other")
			return err
		}},
		{"signing key moved to another row", []string{"UPDATE signing_keys SET alias = 'moved'"}, func(s store.Store) error {
			_, err := s.Keys().SigningKey(ctx, "moved")
			return err
		}},
		{"signing key list unseal", []string{"UPDATE signing_keys SET alias = 'moved'"}, func(s store.Store) error {
			_, err := s.Keys().SigningKeys(ctx)
			return err
		}},
		{"signing key clear default", []string{failUpdate("signing_keys")}, func(s store.Store) error {
			return s.Keys().PutSigningKey(ctx, types.SigningKey{Alias: "new", IsDefault: true})
		}},
		{"certificate moved to another row", []string{"UPDATE certificates SET alias = 'moved'"}, func(s store.Store) error {
			_, err := s.Keys().Certificate(ctx, "moved")
			return err
		}},
		{"certificate list unseal", []string{"UPDATE certificates SET alias = 'moved'"}, func(s store.Store) error {
			_, err := s.Keys().Certificates(ctx)
			return err
		}},
		{"certificate list", []string{"DROP TABLE certificates"}, func(s store.Store) error {
			_, err := s.Keys().Certificates(ctx)
			return err
		}},
		{"trusted key list", []string{"DROP TABLE trusted_keys"}, func(s store.Store) error {
			_, err := s.Keys().TrustedKeys(ctx)
			return err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := identityStore(t)
			for _, stmt := range c.sabotage {
				if _, err := s.DB().ExecContext(ctx, stmt); err != nil {
					t.Fatalf("%s: %v", stmt, err)
				}
			}
			err := c.call(s)
			if err == nil {
				t.Fatal("the injected fault was not reported")
			}
			if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrConflict) {
				t.Fatalf("the fault surfaced as a store sentinel: %v", err)
			}
		})
	}
}

// TestAccSealedIdentityColumns checks that key material is sealed at rest and
// that a store without the keyring cannot read it.
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
	bare := sqlcommon.New(s.DB(), sqlite.Dialect, nil)
	if _, err := bare.Keys().Keypair(ctx, "kp"); !errors.Is(err, sqlcommon.ErrNoKeyring) {
		t.Fatalf("read without a keyring: %v", err)
	}
}
