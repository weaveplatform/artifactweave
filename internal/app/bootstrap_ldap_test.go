package app

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
)

const (
	ldapDescriptorSecret = "ldap-bootstrap-private-fixture"
	ldapDescriptor       = "security:\n  ldapSettings:\n    corp:\n      ldapUrl: ldap://ldap.example.com/dc=example,dc=com\n" +
		"      userDnPattern: uid={0},ou=people\n      search:\n        managerDn: cn=manager,dc=example,dc=com\n" +
		"        managerPassword: ${LDAP_MANAGER_PASSWORD}\n"
)

// readLDAP opens cfg's store and reads the connection corp with its secret.
func readLDAP(t *testing.T, cfg config.Config) configdoc.ConfigDocument {
	t.Helper()
	keys, err := Keyring(cfg)
	if err != nil {
		t.Fatal(err)
	}
	st, err := OpenStore(t.Context(), cfg, appsql.Options{Keyring: keys})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	d, err := st.Configs().Get(t.Context(), configdoc.ConfigLDAP, "corp")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(d.Document), ldapDescriptorSecret) {
		t.Fatal("the manager password is in the public document")
	}
	return d
}

// config apply and the bootstrap descriptor write LDAP connections through
// the same access binding as PATCH, the password from the environment.
func TestAccApplyDescriptorCommandLDAP(t *testing.T) {
	t.Setenv("LDAP_MANAGER_PASSWORD", ldapDescriptorSecret)
	cfg := testConfig(t)
	cfg.MasterKey = keyOne
	file := writeFile(t, ldapDescriptor)
	var out bytes.Buffer
	if err := ApplyDescriptor(t.Context(), cfg, file, false, &out); err != nil ||
		out.String() != "Applied the configuration descriptor.\nCreated: security.ldapSettings.corp\n" {
		t.Fatalf("apply: %v\n%s", err, out.String())
	}
	first := readLDAP(t, cfg)
	if first.Secrets["manager_password"] != ldapDescriptorSecret || !strings.Contains(string(first.Document), `"user_dn_pattern":"uid={0},ou=people"`) {
		t.Fatalf("stored %s", first.Document)
	}
	out.Reset()
	if err := ApplyDescriptor(t.Context(), cfg, file, false, &out); err != nil ||
		out.String() != "Applied the configuration descriptor.\nUnchanged: security.ldapSettings.corp\n" {
		t.Fatalf("second apply: %v\n%s", err, out.String())
	}
	if !bytes.Equal(first.Document, readLDAP(t, cfg).Document) {
		t.Fatal("the second apply rewrote the connection")
	}
}

func TestAccBootstrapDescriptorLDAP(t *testing.T) {
	t.Setenv("LDAP_MANAGER_PASSWORD", ldapDescriptorSecret)
	cfg := testConfig(t)
	cfg.MasterKey = keyOne
	cfg.Bootstrap.Descriptor = writeFile(t, ldapDescriptor)
	var logs bytes.Buffer
	rt, err := OpenRuntime(t.Context(), cfg, slog.New(slog.NewTextHandler(&logs, nil)), RuntimeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if d := readLDAP(t, cfg); d.Secrets["manager_password"] != ldapDescriptorSecret || !d.Enabled {
		t.Fatal("the bootstrap descriptor did not store the connection")
	}
	if strings.Contains(logs.String(), ldapDescriptorSecret) {
		t.Fatal("the bootstrap logged the manager password")
	}
}
