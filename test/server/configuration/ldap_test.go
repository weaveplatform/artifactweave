package configuration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

const (
	ldapPath   = "/access/api/v1/ldap/settings"
	ldapSecret = "ldap-manager-private-fixture"
	ldapYAML   = `security:
  ldapSettings:
    corp:
      key: corp
      ldapUrl: ldaps://ldap.example.com:636/dc=example,dc=com
      search:
        searchFilter: (uid={0})
        searchBase: ou=people
        managerDn: cn=manager,dc=example,dc=com
        managerPassword: ` + ldapSecret + `
`
)

func (w *repoWorld) patch(as, yaml string) (int, string) {
	w.t.Helper()
	rec := w.do(http.MethodPatch, configPath, as, yaml, "Content-Type", "application/yaml")
	return rec.Code, rec.Body.String()
}

func (w *repoWorld) ldap(key string) (int, map[string]any) {
	w.t.Helper()
	rec := w.do(http.MethodGet, ldapPath+"/"+key, "admin", "")
	if strings.Contains(rec.Body.String(), ldapSecret) {
		w.t.Fatal("the Access API returned the manager password")
	}
	var out map[string]any
	if rec.Code == http.StatusOK {
		storekit.Must(w.t, json.Unmarshal(rec.Body.Bytes(), &out))
	}
	return rec.Code, out
}

func (w *repoWorld) ldapDocument(key string) configdoc.ConfigDocument {
	w.t.Helper()
	d, err := w.db.Configs().Get(context.Background(), configdoc.ConfigLDAP, key)
	storekit.Must(w.t, err)
	if bytes.Contains(d.Document, []byte(ldapSecret)) {
		w.t.Fatal("the manager password is in the public document")
	}
	return d
}

// TestAccLDAPDescriptorSharesAccessConnections writes one connection
// through the descriptor and the Access API in turn: each reads back what
// the other wrote, field by field changes keep what they omit, the password
// included, and the XML export carries no ldapSettings, as 7.161.26's
// SecurityDescriptor has none.
func TestAccLDAPDescriptorSharesAccessConnections(t *testing.T) {
	w := newRepoWorld(t)
	if code, body := w.patch("admin", ldapYAML); code != http.StatusOK || body != "6 changes to config merged successfully" {
		t.Fatal(code, body)
	}
	// The first enabled connection turned internal password login off, as
	// in 7.x; the administrator, who has no token here, turns it back on
	// (TestAccInternalPasswordLogin covers the switch).
	var internal bool
	if err := w.db.Settings().Get(t.Context(), settings.SettingBasicAuthEnabled, &internal); err != nil || internal {
		t.Fatalf("internal password login after the first connection: %t %v", internal, err)
	}
	storekit.Must(t, w.db.Settings().Put(t.Context(), settings.SettingBasicAuthEnabled, true))
	code, got := w.ldap("corp")
	want := map[string]any{
		"key": "corp", "enabled": true, "ldap_url": "ldaps://ldap.example.com:636/dc=example,dc=com",
		"user_dn_pattern": "", "email_attribute": "mail", "auto_create_user": true,
		"ldap_poisoning_protection": true, "allow_user_to_access_profile": false, "paging_support_enabled": true,
		"search": map[string]any{
			"search_filter": "(uid={0})", "search_base": "ou=people", "search_sub_tree": true,
			"manager_dn": "cn=manager,dc=example,dc=com",
		},
	}
	if code != http.StatusOK || !sameJSON(t, got, want) {
		t.Fatalf("Access read %d %v", code, got)
	}
	revision := w.ldapDocument("corp").Document
	if w.ldapDocument("corp").Secrets["manager_password"] != ldapSecret {
		t.Fatal("the descriptor's password was not stored")
	}
	// The same descriptor again changes nothing, not even the revision.
	if code, body := w.patch("admin", ldapYAML); code != http.StatusOK || body != "6 changes to config merged successfully" {
		t.Fatal(code, body)
	}
	if !bytes.Equal(revision, w.ldapDocument("corp").Document) {
		t.Fatal("a second apply rewrote the connection")
	}
	// Access replaces without the password, which it keeps.
	got["enabled"] = false
	body, err := json.Marshal(got)
	storekit.Must(t, err)
	if rec := w.do(http.MethodPut, ldapPath, "admin", string(body)); rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body)
	}
	// The descriptor changes one field and keeps the rest.
	if code, body := w.patch("admin", "security:\n  ldapSettings:\n    corp:\n      emailAttribute: email\n"); code != http.StatusOK {
		t.Fatal(code, body)
	}
	_, got = w.ldap("corp")
	if got["enabled"] != false || got["email_attribute"] != "email" ||
		w.ldapDocument("corp").Secrets["manager_password"] != ldapSecret {
		t.Fatalf("a field-by-field change lost what it omitted: %v", got)
	}
	rec := w.do(http.MethodGet, configPath, "admin", "")
	xml := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Contains(xml, ldapSecret) || strings.Contains(xml, "managerPassword") ||
		!strings.Contains(xml, "<security><anonAccessEnabled>false</anonAccessEnabled>"+
			"<userLockPolicy><enabled>false</enabled><loginAttempts>5</loginAttempts></userLockPolicy>"+
			"<passwordSettings><encryptionPolicy>supported</encryptionPolicy><expirationPolicy><enabled>false</enabled>"+
			"<passwordMaxAge>60</passwordMaxAge><notifyByEmail>true</notifyByEmail></expirationPolicy></passwordSettings>"+
			"<ldapGroupSettings></ldapGroupSettings></security>") || strings.Contains(xml, "ldapSetting>") {
		t.Fatalf("export %d %s", rec.Code, xml)
	}
	// managerPassword null clears it; the entry null deletes the connection.
	if code, body := w.patch("admin", "security:\n  ldapSettings:\n    corp:\n      search:\n        managerPassword: null\n"); code != http.StatusOK {
		t.Fatal(code, body)
	}
	if _, ok := w.ldapDocument("corp").Secrets["manager_password"]; ok {
		t.Fatal("a null password did not clear it")
	}
	if code, body := w.patch("admin", "security:\n  ldapSettings:\n    corp: null\n"); code != http.StatusOK {
		t.Fatal(code, body)
	}
	if code, _ := w.ldap("corp"); code != http.StatusNotFound {
		t.Fatal("the descriptor's delete left the connection", code)
	}
}

func sameJSON(t *testing.T, a, b any) bool {
	t.Helper()
	ja, err := json.Marshal(a)
	storekit.Must(t, err)
	jb, err := json.Marshal(b)
	storekit.Must(t, err)
	return bytes.Equal(ja, jb)
}

// TestAccLDAPDescriptorRefusals refuses a descriptor before anything in it
// is written: a non-administrator, a repeated key, an invalid connection
// beside valid entries, an invalid group setting beside valid entries, and
// spellings the section does not have.
func TestAccLDAPDescriptorRefusals(t *testing.T) {
	w := newRepoWorld(t)
	if code, _ := w.patch("reader", ldapYAML); code != http.StatusForbidden {
		t.Fatal("a reader patched LDAP connections", code)
	}
	mixed := "proxies:\n  corp-proxy:\n    host: proxy.example\n    port: 8080\n" +
		"localRepositories:\n  ldap-local:\n    type: generic\n"
	for name, tc := range map[string]struct{ yaml, message string }{
		"duplicate key": {ldapYAML + "    corp:\n      enabled: false\n", "already defined"},
		"invalid connection": {
			mixed + "security:\n  ldapSettings:\n    corp:\n      ldapUrl: http://ldap.example.com\n      userDnPattern: uid={0}\n",
			"security.ldapSettings.corp: ldapUrl must be an ldap:// or ldaps:// URL",
		},
		"no user lookup": {
			mixed + "security:\n  ldapSettings:\n    corp:\n      ldapUrl: ldap://h\n",
			"security.ldapSettings.corp: the LDAP connection needs userDnPattern or search.searchFilter",
		},
		"password without manager": {
			mixed + "security:\n  ldapSettings:\n    corp:\n      ldapUrl: ldap://h\n      userDnPattern: uid={0}\n      search:\n        managerPassword: " + ldapSecret + "\n",
			"search.managerPassword needs search.managerDn",
		},
		"mismatched key":   {"security:\n  ldapSettings:\n    corp:\n      key: other\n", "key other does not match the entry key"},
		"snake case field": {"security:\n  ldapSettings:\n    corp:\n      ldap_url: ldap://h\n", "unknown field ldap_url"},
		"unknown search":   {"security:\n  ldapSettings:\n    corp:\n      search:\n        filter: x\n", "unknown field filter"},
		"null flag":        {"security:\n  ldapSettings:\n    corp:\n      enabled: null\n", "cannot be null"},
		"null search":      {"security:\n  ldapSettings:\n    corp:\n      search: null\n", "cannot be null"},
		"number password":  {"security:\n  ldapSettings:\n    corp:\n      search:\n        managerPassword: 1234\n", "managerPassword must be a string"},
		"string flag":      {"security:\n  ldapSettings:\n    corp:\n      search:\n        searchSubTree: yes-please\n", "must be a boolean"},
		"section null":     {"security:\n  ldapSettings: null\n", "must be a mapping of keys to connections"},
		"entry scalar":     {"security:\n  ldapSettings:\n    corp: true\n", "must be a mapping or null"},
		// LDAP-03 (#1834) applies group settings; an invalid one beside valid
		// entries still refuses the whole descriptor.
		"invalid group setting": {
			mixed + "security:\n  ldapGroupSettings:\n    g:\n      enabledLdap: corp\n      strategy: NESTED\n",
			"security.ldapGroupSettings.g: strategy must be STATIC, DYNAMIC or HIERARCHICAL",
		},
	} {
		t.Run(name, func(t *testing.T) {
			code, body := w.patch("admin", tc.yaml)
			if code != http.StatusBadRequest || !strings.Contains(body, tc.message) || strings.Contains(body, ldapSecret) {
				t.Fatalf("%d %s", code, body)
			}
			all, err := w.db.Configs().List(context.Background(), configdoc.ConfigLDAP)
			storekit.Must(t, err)
			proxies, err := w.db.Configs().List(context.Background(), configdoc.ConfigProxy)
			storekit.Must(t, err)
			_, repoErr := w.db.Repos().Get(context.Background(), "ldap-local")
			if len(all) != 0 || len(proxies) != 0 || !errors.Is(repoErr, kernel.ErrNotFound) {
				t.Fatal("a refused descriptor wrote an entry")
			}
		})
	}
}

// failingLDAPWrites fails the write of an LDAP connection, the last write a
// descriptor makes, after its repositories and proxies.
type failingLDAPWrites struct {
	harness.Store
}

var errLDAPWrite = errors.New("ldap write failure")

func (s failingLDAPWrites) Configs() configdoc.ConfigStore {
	return failingLDAPConfigs{s.Store.Configs()}
}

type failingLDAPConfigs struct{ configdoc.ConfigStore }

func (c failingLDAPConfigs) Put(ctx context.Context, d configdoc.ConfigDocument) error {
	if d.Kind == configdoc.ConfigLDAP {
		return errLDAPWrite
	}
	return c.ConfigStore.Put(ctx, d) //nolint:wrapcheck // test double
}

// TestAccLDAPDescriptorFailedWriteRollsBack fails the connection's write
// after the proxy and repository of the same descriptor were written:
// nothing of the descriptor remains.
func TestAccLDAPDescriptorFailedWriteRollsBack(t *testing.T) {
	w := newRepoWorld(t)
	api := configurationAPI(t, failingLDAPWrites{w.db})
	plan, err := api.Descriptor.Parse([]byte("proxies:\n  corp-proxy:\n    host: proxy.example\n    port: 8080\n"+
		"localRepositories:\n  ldap-local:\n    type: generic\n"+ldapYAML), nil)
	storekit.Must(t, err)
	if _, err := api.Descriptor.Apply(t.Context(), plan, time.Now(), false); !errors.Is(err, errLDAPWrite) {
		t.Fatal("the failed write was not reported", err)
	}
	if _, err := w.db.Configs().Get(t.Context(), configdoc.ConfigProxy, "corp-proxy"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("the proxy outlived the failed descriptor")
	}
	if _, err := w.db.Repos().Get(t.Context(), "ldap-local"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("the repository outlived the failed descriptor")
	}
	// The internal password switch the connection would have turned off
	// is written in the same transaction, so it was never written either.
	var internal bool
	if err := w.db.Settings().Get(t.Context(), settings.SettingBasicAuthEnabled, &internal); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("the internal password switch outlived the failed descriptor", internal, err)
	}
	// A dry run plans the connection and writes nothing.
	result, err := api.Descriptor.Apply(t.Context(), plan, time.Now(), true)
	if err != nil || !strings.Contains(strings.Join(result.Created, ","), "security.ldapSettings.corp") {
		t.Fatal(result, err)
	}
}

// TestAccLDAPSealedSQLAndRestart stores the connection on SQLite and
// PostgreSQL: the password only in the sealed secrets, bound to its row,
// unreadable without the keyring, and the connection read back whole by a
// store opened again, as after a restart.
func TestAccLDAPSealedSQLAndRestart(t *testing.T) {
	keys := storekit.Get[*crypt.Keyring](t)(crypt.New(bytes.Repeat([]byte{9}, 32)))
	sqltest.RunModule(t, appsql.Modules(), func(base *dialect.Store) *appsql.Store {
		return appsql.New(base.DB(), base.Dialect(), keys)
	}, func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
		db := newStore(t)
		ctx := t.Context()
		applyConfiguration(t, configurationAPI(t, db), ctx, ldapYAML)
		var sealed []byte
		storekit.Must(t, db.DB().QueryRowContext(ctx, db.Dialect().Rebind(
			"SELECT value FROM config_secrets WHERE kind = ? AND key = ? AND name = ?"),
			"ldap", "corp", "manager_password").Scan(&sealed))
		if !crypt.IsSealed(sealed) || bytes.Contains(sealed, []byte(ldapSecret)) {
			t.Fatal("the manager password is not sealed")
		}
		plain, err := keys.Open("config_secrets.value", "ldap/corp/manager_password", sealed)
		if err != nil || string(plain) != ldapSecret {
			t.Fatal("the sealed password is not bound to its row", err)
		}
		rows, err := db.DB().QueryContext(ctx, "SELECT kind, key, document FROM config_documents")
		storekit.Must(t, err)
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var kind, key, document string
			storekit.Must(t, rows.Scan(&kind, &key, &document))
			if strings.Contains(document, ldapSecret) {
				t.Fatalf("document %s/%s holds the password", kind, key)
			}
		}
		storekit.Must(t, rows.Err())

		reopened := appsql.New(db.DB(), db.Dialect(), keys)
		conns, err := accesstest.Over(t, reopened).API().LDAP.Connections(ctx)
		if err != nil || len(conns) != 1 || conns[0].SearchFilter != "(uid={0})" || !conns[0].Enabled {
			t.Fatal("the connection did not survive reopening the store", conns, err)
		}
		d, err := reopened.Configs().Get(ctx, configdoc.ConfigLDAP, "corp")
		if err != nil || d.Secrets["manager_password"] != ldapSecret {
			t.Fatal("the password did not survive reopening the store", err)
		}
		if _, err := appsql.New(db.DB(), db.Dialect(), nil).Configs().Get(ctx, configdoc.ConfigLDAP, "corp"); !errors.Is(err, dialect.ErrNoKeyring) {
			t.Fatal("the password was readable without the keyring", err)
		}
	}, sqltest.WithPostgres(), sqltest.WithKeyring(keys))
}
