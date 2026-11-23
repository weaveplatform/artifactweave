package access_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	platformsettings "github.com/weaveplatform/artifactweave/internal/platform/settings"
)

const (
	ldapSettingsPath = "/access/api/v1/ldap/settings"
	ldapSecret       = "ldap-manager-private-fixture"
)

// providerLDAP is the body artifactory_ldap_setting_v2 (provider 12.11.14)
// sends on create and update, its API model: every field, the search
// strings and the password left out when empty (omitempty).
type providerLDAP struct {
	Key                      string         `json:"key"`
	Enabled                  bool           `json:"enabled"`
	LdapURL                  string         `json:"ldap_url"`
	UserDnPattern            string         `json:"user_dn_pattern"`
	Search                   providerSearch `json:"search"`
	AutoCreateUser           bool           `json:"auto_create_user"`
	EmailAttribute           string         `json:"email_attribute"`
	LdapPoisoningProtection  bool           `json:"ldap_poisoning_protection"`
	AllowUserToAccessProfile bool           `json:"allow_user_to_access_profile"`
	PagingSupportEnabled     bool           `json:"paging_support_enabled"`
}

type providerSearch struct {
	SearchFilter    string `json:"search_filter,omitempty"`
	SearchBase      string `json:"search_base,omitempty"`
	SearchSubTree   bool   `json:"search_sub_tree"`
	ManagerDn       string `json:"manager_dn,omitempty"`
	ManagerPassword string `json:"manager_password,omitempty"`
}

func (p providerLDAP) body(t *testing.T) string {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// providerPlan is the plan of a resource that sets the search attributes
// together, as the provider's validators require, and the documented
// defaults for the rest.
func providerPlan() providerLDAP {
	return providerLDAP{
		Key: "corp", Enabled: true, LdapURL: "ldap://ldap.example.com:389/dc=example,dc=com",
		Search: providerSearch{
			SearchFilter: "(uid={0})", SearchBase: "ou=people", SearchSubTree: true,
			ManagerDn: "cn=manager,dc=example,dc=com", ManagerPassword: ldapSecret,
		},
		AutoCreateUser: true, EmailAttribute: "mail", LdapPoisoningProtection: true, PagingSupportEnabled: true,
	}
}

func ldapWorld(t *testing.T) identityWorld {
	t.Helper()
	w := newIdentityWorld(t)
	if err := w.db.Users().Create(t.Context(), accesstest.User{
		Name: "reader", PasswordHash: cheapHash, Groups: []string{authn.ReadersGroup},
	}); err != nil {
		t.Fatal(err)
	}
	// These tests manage connections with passwords: internal password login
	// set on explicitly stays on when a connection is enabled
	// (TestAccInternalPasswordLogin covers the default turning it off).
	if err := w.db.Settings().Put(t.Context(), platformsettings.SettingBasicAuthEnabled, true); err != nil {
		t.Fatal(err)
	}
	return w
}

func ldapSecretRow(t *testing.T, w identityWorld, key string) string {
	t.Helper()
	d, err := w.db.Configs().Get(t.Context(), configdoc.ConfigLDAP, key)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(d.Document), ldapSecret) {
		t.Fatal("the manager password is in the public document")
	}
	return d.Secrets["manager_password"]
}

// TestAccLDAPSettingsProviderShape replays artifactory_ldap_setting_v2's
// create, read, update, import and delete: every read returns what the plan
// holds, so a refreshed plan is empty, and the password is never returned
// yet kept when an update leaves it out.
func TestAccLDAPSettingsProviderShape(t *testing.T) {
	w := ldapWorld(t)
	plan := providerPlan()
	var created providerLDAP
	w.call(t, http.MethodPost, ldapSettingsPath+"/", admin, plan.body(t), http.StatusCreated, &created)
	sent := plan
	sent.Search.ManagerPassword = ""
	if created != sent {
		t.Fatalf("created %+v, want %+v", created, sent)
	}
	var read providerLDAP
	w.call(t, http.MethodGet, ldapSettingsPath+"/corp", admin, "", http.StatusOK, &read)
	if read != sent {
		t.Fatalf("read %+v, want %+v", read, sent)
	}
	// The provider sends no password once its configuration drops it.
	plan.Search.ManagerPassword = ""
	plan.Enabled, plan.AllowUserToAccessProfile = false, true
	var updated providerLDAP
	w.call(t, http.MethodPut, ldapSettingsPath+"/", admin, plan.body(t), http.StatusOK, &updated)
	if updated != plan || ldapSecretRow(t, w, "corp") != ldapSecret {
		t.Fatalf("update %+v lost the password or a field", updated)
	}
	var all []providerLDAP
	w.call(t, http.MethodGet, ldapSettingsPath, admin, "", http.StatusOK, &all)
	if len(all) != 1 || all[0] != plan {
		t.Fatalf("listed %+v", all)
	}
	w.call(t, http.MethodDelete, ldapSettingsPath+"/corp", admin, "", http.StatusNoContent, nil)
	// Read and delete of a gone connection answer 404, which the provider
	// reads as removed.
	w.call(t, http.MethodGet, ldapSettingsPath+"/corp", admin, "", http.StatusNotFound, nil)
	w.call(t, http.MethodDelete, ldapSettingsPath+"/corp", admin, "", http.StatusNotFound, nil)
	w.call(t, http.MethodPut, ldapSettingsPath, admin, plan.body(t), http.StatusNotFound, nil)
	w.call(t, http.MethodGet, ldapSettingsPath, admin, "", http.StatusOK, &all)
	if all == nil || len(all) != 0 {
		t.Fatal("an empty list is not []")
	}
}

func TestAccLDAPSettingsDefaultsAndPasswords(t *testing.T) {
	w := ldapWorld(t)
	var created map[string]any
	w.call(t, http.MethodPost, ldapSettingsPath, admin,
		`{"key":"min","ldap_url":"ldaps://ldap.example.com","user_dn_pattern":"uid={0},ou=people"}`,
		http.StatusCreated, &created)
	want := `{"allow_user_to_access_profile":false,"auto_create_user":true,"email_attribute":"mail","enabled":true,` +
		`"key":"min","ldap_poisoning_protection":true,"ldap_url":"ldaps://ldap.example.com","paging_support_enabled":true,` +
		`"search":{"manager_dn":"","search_base":"","search_filter":"","search_sub_tree":true},"user_dn_pattern":"uid={0},ou=people"}`
	if got, _ := json.Marshal(created); string(got) != want {
		t.Fatalf("created %s", got)
	}
	body := `{"key":"min","ldap_url":"ldaps://ldap.example.com","user_dn_pattern":"uid={0}",` +
		`"search":{"manager_dn":"cn=m","manager_password":"` + ldapSecret + `"}}`
	rec := do(t, w.h, http.MethodPut, ldapSettingsPath, jsonAs(admin), body)
	mustStatus(t, rec, http.StatusOK, "replace with a password")
	if strings.Contains(rec.Body.String(), ldapSecret) || strings.Contains(rec.Body.String(), "manager_password") {
		t.Fatal("the password was returned")
	}
	for _, path := range []string{ldapSettingsPath, ldapSettingsPath + "/min"} {
		if rec := do(t, w.h, http.MethodGet, path, admin, ""); strings.Contains(rec.Body.String(), ldapSecret) ||
			strings.Contains(rec.Body.String(), "manager_password") {
			t.Fatal("a read returned the password")
		}
	}
	if ldapSecretRow(t, w, "min") != ldapSecret {
		t.Fatal("the password was not stored sealed apart")
	}
	w.call(t, http.MethodPut, ldapSettingsPath, admin, strings.Replace(body, ldapSecret, "", 1), http.StatusOK, nil)
	if ldapSecretRow(t, w, "min") != "" {
		t.Fatal("an empty password did not clear it")
	}
}

func TestAccLDAPSettingsRefusals(t *testing.T) {
	w := ldapWorld(t)
	valid := `{"key":"corp","ldap_url":"ldap://ldap.example.com","user_dn_pattern":"uid={0}"}`
	w.call(t, http.MethodPost, ldapSettingsPath, admin, valid, http.StatusCreated, nil)
	for name, tc := range map[string]struct{ method, body, message string }{
		"duplicate key":    {http.MethodPost, valid, "an LDAP connection with that key already exists"},
		"http url":         {http.MethodPost, `{"key":"x","ldap_url":"http://h","user_dn_pattern":"uid={0}"}`, "ldap_url must be an ldap:// or ldaps:// URL"},
		"no user lookup":   {http.MethodPost, `{"key":"x","ldap_url":"ldap://h"}`, "needs user_dn_pattern or search.search_filter"},
		"password, no dn":  {http.MethodPost, `{"key":"x","ldap_url":"ldap://h","user_dn_pattern":"uid={0}","search":{"manager_password":"` + ldapSecret + `"}}`, "search.manager_password needs search.manager_dn"},
		"filter":           {http.MethodPost, `{"key":"x","ldap_url":"ldap://h","search":{"search_filter":"(uid=x)"}}`, "search.search_filter must contain {0}"},
		"replace, no key":  {http.MethodPut, `{"ldap_url":"ldap://h"}`, "key is required"},
		"replace, invalid": {http.MethodPut, `{"key":"corp","ldap_url":"ldap://h","user_dn_pattern":"x"}`, "user_dn_pattern must contain {0}"},
		"no body":          {http.MethodPost, "", "body"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := do(t, w.h, tc.method, ldapSettingsPath, jsonAs(admin), tc.body)
			mustStatus(t, rec, http.StatusBadRequest, name)
			if !strings.Contains(rec.Body.String(), tc.message) || strings.Contains(rec.Body.String(), ldapSecret) {
				t.Fatalf("%s: %s", name, rec.Body)
			}
		})
	}
	var read map[string]any
	w.call(t, http.MethodGet, ldapSettingsPath+"/corp", admin, "", http.StatusOK, &read)
	if read["user_dn_pattern"] != "uid={0}" {
		t.Fatal("a refused replace changed the connection")
	}
}

// TestAccLDAPSettingsAuthority refuses every operation to callers who are
// not administrators, before the store is read or written.
func TestAccLDAPSettingsAuthority(t *testing.T) {
	w := ldapWorld(t)
	valid := `{"key":"corp","ldap_url":"ldap://ldap.example.com","user_dn_pattern":"uid={0}","search":{"manager_dn":"cn=m","manager_password":"` + ldapSecret + `"}}`
	w.call(t, http.MethodPost, ldapSettingsPath, admin, valid, http.StatusCreated, nil)
	other := strings.Replace(valid, `"corp"`, `"other"`, 1)
	reader := basic("reader", "pw")
	scoped := w.bearer(t, "reader", "applied-permissions/user")
	for _, op := range []struct{ method, path, body string }{
		{http.MethodGet, ldapSettingsPath, ""},
		{http.MethodGet, ldapSettingsPath + "/corp", ""},
		{http.MethodPost, ldapSettingsPath, other},
		{http.MethodPut, ldapSettingsPath, valid},
		{http.MethodDelete, ldapSettingsPath + "/corp", ""},
	} {
		for who, creds := range map[string]map[string]string{
			"anonymous": nil, "reader": reader, "reader's token": scoped,
		} {
			want := http.StatusForbidden
			if who == "anonymous" {
				want = http.StatusUnauthorized
			}
			rec := do(t, w.h, op.method, op.path, jsonAs(creds), op.body)
			mustStatus(t, rec, want, who+" "+op.method+" "+op.path)
			if strings.Contains(rec.Body.String(), ldapSecret) || strings.Contains(rec.Body.String(), "ldap.example.com") {
				t.Fatalf("%s saw a connection: %s", who, rec.Body)
			}
		}
	}
	all, err := w.db.Configs().List(t.Context(), configdoc.ConfigLDAP)
	if err != nil || len(all) != 1 || ldapSecretRow(t, w, "corp") != ldapSecret {
		t.Fatal("a refused caller changed the connections", err)
	}
}
