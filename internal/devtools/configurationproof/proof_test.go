package configurationproof

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqlite"
	platformsql "github.com/weaveplatform/artifactweave/internal/platform/store/sql"
)

func TestAccConfigurationProofReadsActualSealedSecretLifecycle(t *testing.T) {
	const master = "configuration-proof-master-key-32-bytes"
	const first, second = "first-private-fixture", "second-private-fixture"
	path := filepath.Join(t.TempDir(), "fixture.db")
	db, err := sqlite.OpenDB(path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := dialect.Migrate(t.Context(), db, sqlite.Dialect, []dialect.Module{platformsql.Module}, ""); err != nil {
		t.Fatal(err)
	}
	material, err := crypt.ParseVersionedKey(master)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := crypt.NewKeyring(material)
	if err != nil {
		t.Fatal(err)
	}
	store := platformsql.New(dialect.New(db, sqlite.Dialect, keys))
	doc := configdoc.ConfigDocument{Kind: configdoc.ConfigProxy, Key: "owned", Document: []byte(`{"host":"proxy.invalid","port":3128}`)}
	write := func(secret string) {
		t.Helper()
		doc.Secrets = map[string]string{"password": secret}
		if err := store.Configs().Put(t.Context(), doc); err != nil {
			t.Fatal(err)
		}
	}
	evidence := filepath.Join(t.TempDir(), "proof.json")
	check := func(phase, expected string, wantOK bool) {
		t.Helper()
		err := Run(t.Context(), path, "proxy/owned", phase, evidence, master, expected)
		if (err == nil) != wantOK {
			t.Fatalf("phase %s proof success=%t, expected %t", phase, err == nil, wantOK)
		}
		if err != nil && (strings.Contains(err.Error(), first) || strings.Contains(err.Error(), second)) {
			t.Fatal("proof error exposed a credential")
		}
	}
	write(first)
	check("created", first, true)
	check("updated", second, false)
	write(second)
	check("updated", second, true)
	check("refreshed", second, true)
	doc.Document = []byte(`{"host":"proxy.invalid","port":3128,"username":"retained"}`)
	write(second)
	check("retained", second, true)
	// A provider's empty state/refresh can hide this defect; an actual
	// sealed row with the old value must make the clear proof fail.
	check("cleared", "", false)
	write("")
	check("cleared", "", true)
	if err := store.Configs().Delete(t.Context(), doc.Kind, doc.Key); err != nil {
		t.Fatal(err)
	}
	check("deleted", "", true)
	write(first)
	if _, err := db.ExecContext(t.Context(), "UPDATE config_secrets SET value = ? WHERE key = ?", []byte(first), doc.Key); err != nil {
		t.Fatal(err)
	}
	check("created", first, false)
	data, err := os.ReadFile(evidence)
	if err != nil || bytes.Contains(data, []byte(first)) || bytes.Contains(data, []byte(second)) || bytes.Contains(data, []byte(master)) {
		t.Fatal("private proof evidence exposed credentials or could not be read")
	}
}

func TestUnitConfigurationProofCannotCreateMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.db")
	err := Run(context.Background(), path, "proxy/owned", "created", filepath.Join(t.TempDir(), "proof.json"),
		"configuration-proof-master-key-32-bytes", "private-fixture")
	if err == nil {
		t.Fatal("checker opened a missing application database")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("checker created an application database")
	}
}

func TestAccConfigurationProofReadsTheMailServerPassword(t *testing.T) {
	f := newProofFixture(t)
	material, err := crypt.ParseVersionedKey(fixtureMaster)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := crypt.NewKeyring(material)
	if err != nil {
		t.Fatal(err)
	}
	store := platformsql.New(dialect.New(f.db, sqlite.Dialect, keys))
	mail := configdoc.ConfigDocument{
		Kind: configdoc.ConfigMailServer, Key: "mailserver", Enabled: true,
		Document: []byte(`{"host":"smtp.invalid"}`), Secrets: map[string]string{"password": "mail-proof-fixture"},
	}
	if err := store.Configs().Put(t.Context(), mail); err != nil {
		t.Fatal(err)
	}
	if err := Run(t.Context(), f.database, "mail_server/mailserver", "created", f.evidence, fixtureMaster, "mail-proof-fixture"); err != nil {
		t.Fatal(err)
	}
	// The proxy's password must not satisfy the mail target.
	if err := Run(t.Context(), f.database, "mail_server/mailserver", "created", f.evidence, fixtureMaster, fixturePassword); err == nil {
		t.Fatal("mail proof read another section's secret")
	}
	if err := store.Configs().Delete(t.Context(), mail.Kind, mail.Key); err != nil {
		t.Fatal(err)
	}
	if err := Run(t.Context(), f.database, "mail_server/mailserver", "deleted", f.evidence, fixtureMaster, ""); err != nil {
		t.Fatal(err)
	}
}

func TestAccConfigurationProofReadsTheLDAPManagerPassword(t *testing.T) {
	f := newProofFixture(t)
	material, err := crypt.ParseVersionedKey(fixtureMaster)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := crypt.NewKeyring(material)
	if err != nil {
		t.Fatal(err)
	}
	store := platformsql.New(dialect.New(f.db, sqlite.Dialect, keys))
	ldap := configdoc.ConfigDocument{
		Kind: configdoc.ConfigLDAP, Key: "directory",
		Document: []byte(`{"ldap_url":"ldap://127.0.0.1:1389/dc=example,dc=org","search":{"manager_dn":"cn=reader"}}`),
		Secrets:  map[string]string{"manager_password": "ldap-proof-fixture"},
	}
	if err := store.Configs().Put(t.Context(), ldap); err != nil {
		t.Fatal(err)
	}
	if err := Run(t.Context(), f.database, "ldap/directory", "created", f.evidence, fixtureMaster, "ldap-proof-fixture"); err != nil {
		t.Fatal(err)
	}
	// A clear must look at manager_password: a row under "password" would
	// let a retained manager password pass as cleared.
	if err := Run(t.Context(), f.database, "ldap/directory", "cleared", f.evidence, fixtureMaster, ""); err == nil {
		t.Fatal("LDAP clear proof accepted a retained manager password")
	}
	ldap.Secrets = nil
	if err := store.Configs().Put(t.Context(), ldap); err != nil {
		t.Fatal(err)
	}
	if err := Run(t.Context(), f.database, "ldap/directory", "cleared", f.evidence, fixtureMaster, ""); err != nil {
		t.Fatal(err)
	}
	ldap.Document = []byte(`{"ldap_url":"ldap://127.0.0.1:1389","manager_password":"ldap-proof-fixture"}`)
	ldap.Secrets = map[string]string{"manager_password": "ldap-proof-fixture"}
	if err := store.Configs().Put(t.Context(), ldap); err != nil {
		t.Fatal(err)
	}
	if err := Run(t.Context(), f.database, "ldap/directory", "updated", f.evidence, fixtureMaster, "ldap-proof-fixture"); err == nil {
		t.Fatal("LDAP proof accepted a public document naming the manager password")
	}
	if err := store.Configs().Delete(t.Context(), ldap.Kind, ldap.Key); err != nil {
		t.Fatal(err)
	}
	if err := Run(t.Context(), f.database, "ldap/directory", "deleted", f.evidence, fixtureMaster, ""); err != nil {
		t.Fatal(err)
	}
}
