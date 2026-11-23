package descriptor

import (
	"context"
	"encoding/xml"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/configuration/core/system"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

const securityDoc = `security:
  userLockPolicy:
    enabled: true
    loginAttempts: 3
  passwordSettings:
    encryptionPolicy: REQUIRED
    expirationPolicy:
      enabled: true
      passwordMaxAge: 30
      notifyByEmail: false
`

func securityDeps(t *testing.T, st applyStore) Deps {
	t.Helper()
	d := depsOf(t, st)
	d.Security = accesstest.Over(t, st).API().Security
	return d
}

func TestUnitSecurityParseMapsEveryField(t *testing.T) {
	p, err := Parse([]byte(securityDoc), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	s := p.Security
	if s == nil || p.Changes != 6 || !*s.LockEnabled || *s.LoginAttempts != 3 ||
		*s.Encryption != accessapi.EncryptionRequired || !*s.ExpirationEnabled || *s.PasswordMaxAge != 30 ||
		*s.NotifyByEmail {
		t.Fatalf("parsed %+v %+v", p, s)
	}
	// The XML spelling and a block naming nothing are accepted too; an
	// empty block is still a block the result names.
	p, err = Parse([]byte("security:\n  passwordSettings:\n    encryptionPolicy: unsupported\n  userLockPolicy: {}\n"), noEnv)
	if err != nil || *p.Security.Encryption != accessapi.EncryptionUnsupported || p.Security.LockEnabled != nil {
		t.Fatal(p, err)
	}
	if p, err := Parse([]byte("security:\n  anonAccessEnabled: true\n"), noEnv); err != nil || p.Security != nil {
		t.Fatal("anonymous access alone touches no policy", p, err)
	}
}

func TestUnitSecurityParseRefusals(t *testing.T) {
	for doc, want := range map[string]string{
		"security:\n  anonAccessToBuildInfosDisabled: true\n":                                       "security.anonAccessToBuildInfosDisabled is deprecated",
		"security:\n  ldapGroupSettings: {}\n":                                                      "LDAP group settings arrive with LDAP-03",
		"security:\n  samlSettings: {}\n":                                                           "SAML login is planned for P21",
		"security:\n  hideUnauthorizedResources: true\n":                                            "security: unknown field hideUnauthorizedResources",
		"security:\n  userLockPolicy: null\n":                                                       "security.userLockPolicy cannot be null",
		"security:\n  userLockPolicy: 3\n":                                                          "security.userLockPolicy must be a mapping",
		"security:\n  userLockPolicy:\n    attempts: 3\n":                                           "security.userLockPolicy: unknown field attempts",
		"security:\n  userLockPolicy:\n    enabled: null\n":                                         "security.userLockPolicy.enabled cannot be null",
		"security:\n  userLockPolicy:\n    enabled: 'yes'\n":                                        "security.userLockPolicy.enabled must be a boolean",
		"security:\n  userLockPolicy:\n    loginAttempts: null\n":                                   "security.userLockPolicy.loginAttempts cannot be null",
		"security:\n  userLockPolicy:\n    loginAttempts: '3'\n":                                    "security.userLockPolicy.loginAttempts must be a whole number",
		"security:\n  userLockPolicy:\n    loginAttempts: 2.5\n":                                    "security.userLockPolicy.loginAttempts must be a whole number",
		"security:\n  passwordSettings:\n    resetPolicy: {}\n":                                     "security.passwordSettings: unknown field resetPolicy",
		"security:\n  passwordSettings:\n    encryptionPolicy: null\n":                              "security.passwordSettings.encryptionPolicy cannot be null",
		"security:\n  passwordSettings:\n    encryptionPolicy: SOMETIMES\n":                         "must be REQUIRED, SUPPORTED or UNSUPPORTED",
		"security:\n  passwordSettings:\n    encryptionPolicy: 1\n":                                 "must be REQUIRED, SUPPORTED or UNSUPPORTED",
		"security:\n  passwordSettings:\n    expirationPolicy: null\n":                              "security.passwordSettings.expirationPolicy cannot be null",
		"security:\n  passwordSettings:\n    expirationPolicy:\n      currentPasswordValidFor: 1\n": "unknown field currentPasswordValidFor",
		"security:\n  passwordSettings:\n    expirationPolicy:\n      passwordMaxAge: x\n":          "passwordMaxAge must be a whole number",
		"security:\n  passwordSettings:\n    expirationPolicy:\n      notifyByEmail: 1\n":           "notifyByEmail must be a boolean",
	} {
		_, err := Parse([]byte(doc), noEnv)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", doc, err, want)
		}
	}
}

func TestUnitSecurityApplyThroughAccess(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	d := securityDeps(t, db)
	p, err := Parse([]byte(securityDoc), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Apply(ctx, d, p, now, true)
	if err != nil || strings.Join(res.Updated, ",") != "security.userLockPolicy,security.passwordSettings" {
		t.Fatal("dry run", res, err)
	}
	if keys, _ := db.Settings().Keys(ctx); len(keys) != 0 {
		t.Fatal("the dry run wrote", keys)
	}
	if _, err := Apply(ctx, d, p, now, false); err != nil {
		t.Fatal(err)
	}
	got, err := d.Security.Current(ctx)
	want := accessapi.SecurityPolicy{
		LockEnabled: true, LoginAttempts: 3, ExpirationEnabled: true, PasswordMaxAge: 30,
		Encryption: accessapi.EncryptionRequired,
	}
	if err != nil || got != want {
		t.Fatalf("stored %+v %v", got, err)
	}
	// The same document again changes nothing.
	if res, err := Apply(ctx, d, p, now, false); err != nil ||
		strings.Join(res.Unchanged, ",") != "security.userLockPolicy,security.passwordSettings" {
		t.Fatal(res, err)
	}
	// A block the document leaves out keeps its state; turning the lock
	// policy off clears every lock, as the REST APIs do.
	if err := db.Users().Create(ctx, accesstest.User{Name: "locked", Status: accesstest.UserLocked}); err != nil {
		t.Fatal(err)
	}
	p, err = Parse([]byte("security:\n  userLockPolicy:\n    enabled: false\n"), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := Apply(ctx, d, p, now, false); err != nil || strings.Join(res.Updated, ",") != "security.userLockPolicy" {
		t.Fatal(res, err)
	}
	if locked, _ := db.Users().Locked(ctx); len(locked) != 0 {
		t.Fatal("disabling the lock policy left locks", locked)
	}
	got, _ = d.Security.Current(ctx)
	if want.LockEnabled = false; got != want {
		t.Fatalf("after a lock-only document %+v", got)
	}
}

func TestUnitSecurityApplyRefusalsWriteNothing(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	d := securityDeps(t, db)
	for doc, want := range map[string]string{
		"security:\n  userLockPolicy:\n    loginAttempts: -1\n":                            "security.userLockPolicy: loginAttempts must be at least 1",
		"security:\n  passwordSettings:\n    expirationPolicy:\n      passwordMaxAge: 0\n": "security.passwordSettings.expirationPolicy: passwordMaxAge must be at least 1 day",
	} {
		// A valid repository, a mail password and anonymous access in the
		// same document are refused with it.
		p, err := Parse([]byte(doc+"  anonAccessEnabled: true\nlocalRepositories:\n  new-local: {type: generic}\n"+
			"mailServer:\n  host: mail.example.com\n  password: secret\n"), noEnv)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Apply(ctx, d, p, now, false); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), want) {
			t.Fatalf("%q: %v", doc, err)
		}
		if _, err := db.Repos().Get(ctx, "new-local"); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatal("a refused document created a repository")
		}
		if _, err := db.Configs().Get(ctx, configdoc.ConfigMailServer, system.MailServerKey); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatal("a refused document stored the mail password", err)
		}
		if keys, _ := db.Settings().Keys(ctx); len(keys) != 0 {
			t.Fatal("a refused document wrote settings", keys)
		}
	}
	p, err := Parse([]byte(securityDoc), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(ctx, depsOf(t, db), p, now, false); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("a document with policies and no binding", err)
	}
	failing := securityDeps(t, db)
	failing.Security = failingSecurity{SecurityPolicies: failing.Security, current: errBoom}
	if _, err := Apply(ctx, failing, p, now, false); !errors.Is(err, errBoom) || errors.Is(err, ErrInvalid) {
		t.Fatal("a failed read", err)
	}
	if err := exportPolicies(ctx, failing, &exportSecurity{}); !errors.Is(err, errBoom) {
		t.Fatal("a failed export read", err)
	}
}

// TestUnitSecurityWriteRollsBackWithTheApply: the policies are written in
// the patch's transaction, so a failed write leaves the repository and the
// anonymous access switch of the same document unwritten.
func TestUnitSecurityWriteRollsBackWithTheApply(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	p, err := Parse([]byte(securityDoc+"  anonAccessEnabled: true\nlocalRepositories:\n  new-local: {type: generic}\n"), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	d := securityDeps(t, db)
	d.Security = failingSecurity{SecurityPolicies: d.Security, set: errBoom}
	if _, err := Apply(ctx, d, p, now, false); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	if _, err := db.Repos().Get(ctx, "new-local"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("the repository survived the failed transaction", err)
	}
	var on bool
	if err := db.Settings().Get(ctx, settings.SettingAnonymousAccess, &on); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("anonymous access survived the failed transaction", err)
	}
	// A store failure inside the binding's own write rolls back too.
	st := faulty{Store: db, fail: "setting put"}
	if _, err := Apply(ctx, securityDeps(t, st), p, now, false); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	if keys, _ := db.Settings().Keys(ctx); len(keys) != 0 {
		t.Fatal("settings survived the failed transaction", keys)
	}
}

type failingSecurity struct {
	accessapi.SecurityPolicies
	current, set error
}

func (f failingSecurity) Current(ctx context.Context) (accessapi.SecurityPolicy, error) {
	if f.current != nil {
		return accessapi.SecurityPolicy{}, f.current
	}
	return f.SecurityPolicies.Current(ctx) //nolint:wrapcheck // test double
}

func (f failingSecurity) Set(ctx context.Context, p accessapi.SecurityPolicy) error {
	if f.set != nil {
		return f.set
	}
	return f.SecurityPolicies.Set(ctx, p) //nolint:wrapcheck // test double
}

func TestUnitSecurityExportProjection(t *testing.T) {
	ctx := context.Background()
	var out exportSecurity
	if err := exportPolicies(ctx, Deps{}, &out); err != nil || out.UserLockPolicy != nil || out.PasswordSettings != nil {
		t.Fatal("an export without the binding", out, err)
	}
	db := inmem.New()
	d := securityDeps(t, db)
	// Never written: the defaults are in force, and exported.
	if err := exportPolicies(ctx, d, &out); err != nil {
		t.Fatal(err)
	}
	body, err := xml.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	want := "<exportSecurity><anonAccessEnabled>false</anonAccessEnabled>" +
		"<userLockPolicy><enabled>false</enabled><loginAttempts>5</loginAttempts></userLockPolicy>" +
		"<passwordSettings><encryptionPolicy>supported</encryptionPolicy><expirationPolicy>" +
		"<enabled>false</enabled><passwordMaxAge>60</passwordMaxAge><notifyByEmail>true</notifyByEmail>" +
		"</expirationPolicy></passwordSettings><ldapSettings></ldapSettings></exportSecurity>"
	if string(body) != want {
		t.Fatalf("%s", body)
	}
}

// TestUnitSecurityForgetAfterCommit: the API drops the authenticator's
// cached policies after a committed patch with policies or connections, and
// not after a dry run.
func TestUnitSecurityForgetAfterCommit(t *testing.T) {
	db := inmem.New()
	d := securityDeps(t, db)
	counting := &forgetting{SecurityPolicies: d.Security}
	d.Security = counting
	api := NewAPI(d)
	for _, step := range []struct {
		doc    string
		dryRun bool
		want   int
	}{
		{securityDoc, true, 0},
		{securityDoc, false, 1},
		{"security:\n  anonAccessEnabled: true\n", false, 0},
		{"security:\n  ldapSettings:\n    corp: null\n", false, 1},
	} {
		plan, err := api.Descriptor.Parse([]byte(step.doc), noEnv)
		if err != nil {
			t.Fatal(err)
		}
		d.LDAP = accesstest.Over(t, db).API().LDAP
		api = NewAPI(d)
		counting.n = 0
		if _, err := api.Descriptor.Apply(context.Background(), plan, now, step.dryRun); err != nil {
			t.Fatal(err)
		}
		if counting.n != step.want {
			t.Fatalf("%q dry %t: forgot %d times", step.doc, step.dryRun, counting.n)
		}
	}
}

type forgetting struct {
	accessapi.SecurityPolicies
	n int
}

func (f *forgetting) Forget() { f.n++ }
