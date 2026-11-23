package access_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/configuration/core/system"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/pkg/containertest"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

const (
	descriptorPath = "/artifactory/api/system/configuration"
	internalSwitch = "/access/api/v1/config/security/authentication/basic_authentication_enabled"
	accessLockPath = "/access/api/v1/config/security/user_lock_policy"
	encryptedPath  = "/artifactory/api/security/encryptedPassword"
	// securityConfigPath is the general security read.
	securityConfigPath = "/artifactory/api/securityconfig"
	devPassword        = "Dev-pass1"
)

// baselineWorld is a runtime on one backend behind the router, with login
// suspension off so that each wrong password below is about the lock alone,
// a local user dev and an administrator token that survives every switch.
type baselineWorld struct {
	t     *testing.T
	h     http.Handler
	admin map[string]string
}

func newBaselineWorld(t *testing.T, backend string, ep *containertest.LDAPEndpoint) (baselineWorld, *harness.Runtime) {
	t.Helper()
	opts := harness.AppOptions()
	if ep != nil {
		opts.LDAPRootCAs = ep.CA
	}
	rt := harness.OpenApp(t, ldapWorldConfig(t, backend), opts)
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	h := ldapRouter(rt)
	admin := basic("admin", "pw")
	ldapCall(t, h, http.MethodPut, accessLockPath, admin, `{"clientLoginSuspensionEnabled":false}`, http.StatusOK)
	ldapCall(t, h, http.MethodPut, secUsers+"/dev", admin,
		`{"name":"dev","email":"dev@example.com","password":"`+devPassword+`"}`, http.StatusCreated)
	var issued struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(ldapCall(t, h, http.MethodPost, "/access/api/v1/tokens", admin,
		`{"scope":"applied-permissions/admin"}`, http.StatusOK)), &issued); err != nil || issued.AccessToken == "" {
		t.Fatalf("admin token: %v %+v", err, issued)
	}
	return baselineWorld{t: t, h: h, admin: bearerAuth(issued.AccessToken)}, rt
}

// login is the status of an authenticated request with creds: 404 (no API
// key) once they verify, 401 or 403 when they do not.
func (w baselineWorld) login(creds map[string]string) (int, string) {
	w.t.Helper()
	rec := do(w.t, w.h, http.MethodGet, secAPIKey, creds, "")
	return rec.Code, rec.Body.String()
}

func (w baselineWorld) wantLogin(what string, creds map[string]string, want int) string {
	w.t.Helper()
	code, body := w.login(creds)
	if code != want {
		w.t.Fatalf("%s: %d, want %d: %s", what, code, want, body)
	}
	return body
}

func (w baselineWorld) patch(doc string, want int) string {
	w.t.Helper()
	headers := map[string]string{"Content-Type": "application/yaml"}
	for k, v := range w.admin {
		headers[k] = v
	}
	return ldapCall(w.t, w.h, http.MethodPatch, descriptorPath, headers, doc, want)
}

func (w baselineWorld) get(path string) string {
	w.t.Helper()
	return ldapCall(w.t, w.h, http.MethodGet, path, w.admin, "", http.StatusOK)
}

// securityXML is the export's security element.
var securityXML = regexp.MustCompile(`<security>.*</security>`)

func (w baselineWorld) exported() string {
	w.t.Helper()
	return securityXML.FindString(w.get(descriptorPath))
}

// TestAccSecurityBaselineDescriptor drives CFG-04 (#1828) on SQLite and
// PostgreSQL: the descriptor's userLockPolicy and passwordSettings read back
// through the REST APIs and the XML export, a REST write and the equivalent
// descriptor export the same XML, and every policy is enforced on real
// logins (the lock, its clearing, expiry and the encryption policy) while
// the administrator keeps a way back; a refused policy rolls back the rest
// of its document, a sealed secret included.
func TestAccSecurityBaselineDescriptor(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) { securityBaseline(t, backend) })
	}
}

func securityBaseline(t *testing.T, backend string) {
	w, rt := newBaselineWorld(t, backend, nil)
	dev := basic("dev", devPassword)
	wrong := basic("dev", "wrong")

	w.patch(`security:
  userLockPolicy:
    enabled: true
    loginAttempts: 2
  passwordSettings:
    expirationPolicy:
      enabled: true
      passwordMaxAge: 30
      notifyByEmail: false
`, http.StatusOK)
	if got := w.get(secLockPol); got != "{\"enabled\":true,\"loginAttempts\":2}\n" {
		t.Fatalf("lock policy through REST: %q", got)
	}
	if got := w.get(secExpiry); got != "{\"enabled\":true,\"notifyByEmail\":false,\"passwordMaxAge\":30}\n" {
		t.Fatalf("expiration policy through REST: %q", got)
	}
	want := "<security><anonAccessEnabled>false</anonAccessEnabled>" +
		"<userLockPolicy><enabled>true</enabled><loginAttempts>2</loginAttempts></userLockPolicy>" +
		"<passwordSettings><encryptionPolicy>supported</encryptionPolicy><expirationPolicy><enabled>true</enabled>" +
		"<passwordMaxAge>30</passwordMaxAge><notifyByEmail>false</notifyByEmail></expirationPolicy></passwordSettings>" +
		"<ldapSettings></ldapSettings></security>"
	if got := w.exported(); got != want {
		t.Fatalf("export %s", got)
	}

	// A REST write and the equivalent descriptor export alike.
	ldapCall(t, w.h, http.MethodPut, secLockPol, w.admin, `{"enabled":true,"loginAttempts":3}`, http.StatusOK)
	viaREST := w.exported()
	w.patch("security:\n  userLockPolicy:\n    loginAttempts: 2\n", http.StatusOK)
	w.patch("security:\n  userLockPolicy:\n    loginAttempts: 3\n", http.StatusOK)
	if got := w.exported(); got != viaREST || !strings.Contains(got, "<loginAttempts>3</loginAttempts>") {
		t.Fatalf("REST %s, descriptor %s", viaREST, got)
	}

	// Three wrong passwords lock dev; the administrator still logs in,
	// sees the lock and clears it.
	w.wantLogin("dev before any failure", dev, http.StatusNotFound)
	for range 3 {
		w.wantLogin("a wrong password", wrong, http.StatusUnauthorized)
	}
	w.wantLogin("dev locked", dev, http.StatusForbidden)
	w.wantLogin("the administrator while dev is locked", basic("admin", "pw"), http.StatusNotFound)
	if got := w.get("/artifactory/api/security/lockedUsers"); !strings.Contains(got, `"dev"`) {
		t.Fatalf("locked users %s", got)
	}
	ldapCall(t, w.h, http.MethodPost, "/artifactory/api/security/unlockUsers/dev", w.admin, "", http.StatusOK)
	w.wantLogin("dev unlocked", dev, http.StatusNotFound)
	// Turning the policy off through the descriptor clears every lock.
	for range 3 {
		w.wantLogin("a wrong password", wrong, http.StatusUnauthorized)
	}
	w.wantLogin("dev locked again", dev, http.StatusForbidden)
	w.patch("security:\n  userLockPolicy:\n    enabled: false\n", http.StatusOK)
	w.wantLogin("dev after the descriptor turned locking off", dev, http.StatusNotFound)

	// A password older than the descriptor's age expires; turning expiry
	// off through the descriptor lets it in again.
	ctx := t.Context()
	u, err := rt.Store.Users().Get(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	u.PasswordChanged = time.Now().Add(-48 * time.Hour)
	if err := rt.Store.Users().Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	w.patch("security:\n  passwordSettings:\n    expirationPolicy:\n      passwordMaxAge: 1\n", http.StatusOK)
	if body := w.wantLogin("an expired password", dev, http.StatusForbidden); !strings.Contains(body, "Password has expired") {
		t.Fatalf("expired: %s", body)
	}
	w.patch("security:\n  passwordSettings:\n    expirationPolicy:\n      enabled: false\n", http.StatusOK)
	w.wantLogin("dev after expiry was turned off", dev, http.StatusNotFound)

	// REQUIRED takes only the encrypted password outside the UI;
	// UNSUPPORTED only the clear-text one. The administrator's token is
	// the way back from either.
	ref := ldapCall(t, w.h, http.MethodGet, encryptedPath, dev, "", http.StatusOK)
	encrypted := basic("dev", ref)
	w.patch("security:\n  passwordSettings:\n    encryptionPolicy: REQUIRED\n", http.StatusOK)
	if body := w.wantLogin("a clear-text password while required", dev, http.StatusUnauthorized); !strings.Contains(
		body, "Only encrypted passwords are accepted") {
		t.Fatalf("required: %s", body)
	}
	w.wantLogin("the administrator's clear-text password", basic("admin", "pw"), http.StatusUnauthorized)
	w.wantLogin("the encrypted password while required", encrypted, http.StatusNotFound)
	ui := basic("dev", devPassword)
	ui["Request-Agent"] = "artifactoryUI"
	w.wantLogin("a UI login while required", ui, http.StatusNotFound)
	w.patch("security:\n  passwordSettings:\n    encryptionPolicy: unsupported\n", http.StatusOK)
	w.wantLogin("the encrypted password while unsupported", encrypted, http.StatusUnauthorized)
	w.wantLogin("a clear-text password while unsupported", dev, http.StatusNotFound)
	if got := w.exported(); !strings.Contains(got, "<encryptionPolicy>unsupported</encryptionPolicy>") {
		t.Fatalf("export %s", got)
	}
	// The general security read artifactory_general_security refreshes from
	// reports the same state, administrators only.
	if got := strings.TrimSpace(w.get(securityConfigPath)); got != `{"anonAccessEnabled":false,"basicAuthEnabled":true,`+
		`"passwordSettings":{"encryptionEnabled":false,"encryptionPolicy":"UNSUPPORTED","encryptionRequired":false,`+
		`"expirationPolicy":{"enabled":false,"notifyByEmail":false,"passwordMaxAge":1}},`+
		`"userLockPolicy":{"enabled":false,"loginAttempts":3}}` {
		t.Fatalf("security configuration %s", got)
	}
	ldapCall(t, w.h, http.MethodGet, securityConfigPath, dev, "", http.StatusForbidden)
	ldapCall(t, w.h, http.MethodGet, securityConfigPath, nil, "", http.StatusUnauthorized)

	// A refused policy refuses its whole document: neither the repository
	// nor the mail server and its password are written.
	before := w.exported()
	body := w.patch(`localRepositories:
  baseline-local: {type: generic}
mailServer:
  host: mail.example.com
  password: mail-private-fixture
security:
  anonAccessEnabled: true
  userLockPolicy:
    loginAttempts: -1
`, http.StatusBadRequest)
	if !strings.Contains(body, "security.userLockPolicy: loginAttempts must be at least 1") {
		t.Fatalf("refusal %s", body)
	}
	ldapCall(t, w.h, http.MethodGet, "/artifactory/api/repositories/baseline-local", w.admin, "", http.StatusNotFound)
	if xml := w.get(descriptorPath); strings.Contains(xml, "mail.example.com") || w.exported() != before {
		t.Fatalf("a refused document wrote %s", xml)
	}
	if _, err := rt.Store.Configs().Get(ctx, configdoc.ConfigMailServer, system.MailServerKey); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("a refused document stored the mail server and its password", err)
	}
}

// TestAccInternalPasswordLogin drives "Disable Internal Password Login"
// (#1828) against a real OpenLDAP directory on SQLite and PostgreSQL:
// enabling the first connection turns internal passwords off, internal and
// directory passwords with the switch on and off and LDAP enabled and
// disabled, an administrator's explicit choice surviving later connection
// writes, and the answer without LDAP unchanged.
func TestAccInternalPasswordLogin(t *testing.T) {
	ep := containertest.OpenLDAP(t)
	accesstest.SeedDirectory(t, ep)
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) { internalPasswordLogin(t, ep, backend) })
	}
}

func internalPasswordLogin(t *testing.T, ep containertest.LDAPEndpoint, backend string) {
	w, rt := newBaselineWorld(t, backend, &ep)
	seedLDAPAccess(t, rt)
	settings := "/access/api/v1/ldap/settings"
	url := ep.TLSURL(ep.BaseDN)
	dev, admin := basic("dev", devPassword), basic("admin", "pw")
	alice := basic("alice", accesstest.Alice.Password)
	internal := func(want bool) {
		t.Helper()
		var st struct{ Enabled *bool }
		if err := json.Unmarshal([]byte(w.get(internalSwitch)), &st); err != nil || st.Enabled == nil || *st.Enabled != want {
			t.Fatalf("internal password login %+v %v, want %t", st, err, want)
		}
	}
	turn := func(on bool) {
		t.Helper()
		body, _ := json.Marshal(map[string]bool{"enabled": on})
		ldapCall(t, w.h, http.MethodPut, internalSwitch, w.admin, string(body), http.StatusOK)
	}

	// No LDAP: internal passwords on by default, directory users unknown.
	internal(true)
	w.wantLogin("dev without LDAP", dev, http.StatusNotFound)
	w.wantLogin("alice without LDAP", alice, http.StatusUnauthorized)

	// The first enabled connection turns internal passwords off.
	ldapCall(t, w.h, http.MethodPost, settings, admin, ldapBody(ep, "corp", url, nil), http.StatusCreated)
	internal(false)
	w.wantLogin("dev with LDAP and the switch off", dev, http.StatusUnauthorized)
	w.wantLogin("the administrator's password with LDAP and the switch off", admin, http.StatusUnauthorized)
	w.wantLogin("alice with LDAP and the switch off", alice, http.StatusNotFound)
	w.wantLogin("the administrator's token", w.admin, http.StatusNotFound)

	// Switch on with LDAP enabled: both kinds of password.
	turn(true)
	w.wantLogin("dev with LDAP and the switch on", dev, http.StatusNotFound)
	w.wantLogin("the administrator with LDAP and the switch on", admin, http.StatusNotFound)
	w.wantLogin("alice with LDAP and the switch on", alice, http.StatusNotFound)

	// LDAP disabled, switch on: internal passwords only. Disabling never
	// writes the switch.
	ldapCall(t, w.h, http.MethodPut, settings, w.admin, ldapBody(ep, "corp", url, map[string]any{"enabled": false}),
		http.StatusOK)
	internal(true)
	w.wantLogin("dev with LDAP disabled and the switch on", dev, http.StatusNotFound)
	w.wantLogin("alice with LDAP disabled", alice, http.StatusUnauthorized)

	// LDAP disabled, switch off: no password logs in, with the answer the
	// switch gave before LDAP existed; tokens go on.
	turn(false)
	if body := w.wantLogin("dev with LDAP disabled and the switch off", dev, http.StatusUnauthorized); !strings.Contains(
		body, "Basic authentication with a password is disabled") {
		t.Fatalf("switch off without LDAP: %s", body)
	}
	w.wantLogin("alice with LDAP disabled and the switch off", alice, http.StatusUnauthorized)
	w.wantLogin("the administrator's token with the switch off", w.admin, http.StatusNotFound)

	// An administrator's explicit choice survives enabling the connection
	// again: only a switch never written is turned off.
	turn(true)
	ldapCall(t, w.h, http.MethodPut, settings, w.admin, ldapBody(ep, "corp", url, nil), http.StatusOK)
	internal(true)
	w.wantLogin("dev after re-enabling LDAP", dev, http.StatusNotFound)
	w.wantLogin("alice after re-enabling LDAP", alice, http.StatusNotFound)
}
