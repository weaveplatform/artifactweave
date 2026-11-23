package ldap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	platformmem "github.com/weaveplatform/artifactweave/internal/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

const password = "manager-private-fixture"

func ptr[T any](v T) *T { return &v }

func newService(t *testing.T) (Service, configdoc.ConfigStore) {
	t.Helper()
	db := memtx.New()
	configs := platformmem.New(db).Configs()
	return Service{
		Configs: configs, Tx: db, Guard: func(context.Context) error { return nil },
		Now: func() time.Time { return time.Unix(1, 0) },
	}, configs
}

// searchChange is a connection that finds users by a search with a manager.
func searchChange(key string) accessapi.LDAPChange {
	return accessapi.LDAPChange{
		Key: key, URL: ptr("ldaps://ldap.example.com:636/dc=example,dc=com"),
		SearchFilter: ptr("(uid={0})"), SearchBase: ptr("ou=people|ou=staff"),
		ManagerDN: ptr("cn=manager,dc=example,dc=com"), ManagerPassword: ptr(password),
	}
}

func must[T any](t *testing.T) func(T, error) T {
	t.Helper()
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func storedPassword(t *testing.T, configs configdoc.ConfigStore, key string) string {
	t.Helper()
	d, err := configs.Get(t.Context(), configdoc.ConfigLDAP, key)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(d.Document, []byte(password)) || bytes.Contains(d.Document, []byte("manager_password")) {
		t.Fatalf("the document holds the password: %s", d.Document)
	}
	return d.Secrets[secretPassword]
}

func TestUnitDefaultsAndReadBack(t *testing.T) {
	svc, configs := newService(t)
	ctx := t.Context()
	got := must[accessapi.LDAPConnection](t)(svc.Create(ctx, accessapi.LDAPChange{
		Key: "corp", URL: ptr("ldap://ldap.example.com/dc=example,dc=com"), UserDNPattern: ptr("uid={0},ou=people"),
	}))
	want := Defaults("corp")
	want.URL, want.UserDNPattern = "ldap://ldap.example.com/dc=example,dc=com", "uid={0},ou=people"
	if got != want {
		t.Fatalf("created %+v, want %+v", got, want)
	}
	if read := must[accessapi.LDAPConnection](t)(svc.Get(ctx, "corp")); read != want {
		t.Fatalf("read %+v, want %+v", read, want)
	}
	if all := must[[]accessapi.LDAPConnection](t)(svc.List(ctx)); len(all) != 1 || all[0] != want {
		t.Fatalf("listed %+v", all)
	}
	d := must[configdoc.ConfigDocument](t)(configs.Get(ctx, configdoc.ConfigLDAP, "corp"))
	if !d.Enabled || len(d.Secrets) != 0 {
		t.Fatalf("stored %+v", d)
	}
	if _, err := svc.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, searchChange("corp")); !errors.Is(err, ErrExists) || !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("a second create of the key", err)
	}
}

func TestUnitReplaceKeepsAndClearsThePassword(t *testing.T) {
	svc, configs := newService(t)
	ctx := t.Context()
	must[accessapi.LDAPConnection](t)(svc.Create(ctx, searchChange("corp")))
	if got := storedPassword(t, configs, "corp"); got != password {
		t.Fatal("the password was not stored")
	}
	kept := searchChange("corp")
	kept.ManagerPassword = nil
	kept.Enabled = ptr(false)
	got := must[accessapi.LDAPConnection](t)(svc.Replace(ctx, kept))
	if got.Enabled || storedPassword(t, configs, "corp") != password {
		t.Fatal("a replace without the password did not keep it")
	}
	if d := must[configdoc.ConfigDocument](t)(configs.Get(ctx, configdoc.ConfigLDAP, "corp")); d.Enabled {
		t.Fatal("the document's enabled flag does not follow the connection")
	}
	// A replace takes defaults for what it leaves out, the password apart.
	if got.SearchSubTree != true || got.EmailAttribute != "mail" {
		t.Fatalf("replace did not reset to defaults: %+v", got)
	}
	cleared := searchChange("corp")
	cleared.ManagerPassword = ptr("")
	must[accessapi.LDAPConnection](t)(svc.Replace(ctx, cleared))
	if storedPassword(t, configs, "corp") != "" {
		t.Fatal("an empty password did not clear it")
	}
	must[accessapi.LDAPConnection](t)(svc.Replace(ctx, searchChange("corp")))
	noManager := searchChange("corp")
	noManager.ManagerPassword, noManager.ManagerDN = nil, nil
	must[accessapi.LDAPConnection](t)(svc.Replace(ctx, noManager))
	if storedPassword(t, configs, "corp") != "" {
		t.Fatal("a password outlived its manager DN")
	}
	if _, err := svc.Replace(ctx, searchChange("missing")); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestUnitDelete(t *testing.T) {
	svc, configs := newService(t)
	ctx := t.Context()
	must[accessapi.LDAPConnection](t)(svc.Create(ctx, searchChange("corp")))
	if err := svc.Delete(ctx, "corp"); err != nil {
		t.Fatal(err)
	}
	if _, err := configs.Get(ctx, configdoc.ConfigLDAP, "corp"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("the document outlived the delete", err)
	}
	if err := svc.Delete(ctx, "corp"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	// A key created again starts without the deleted connection's password.
	again := searchChange("corp")
	again.ManagerPassword = nil
	must[accessapi.LDAPConnection](t)(svc.Create(ctx, again))
	if storedPassword(t, configs, "corp") != "" {
		t.Fatal("a recreated connection inherited a password")
	}
}

func TestUnitValidation(t *testing.T) {
	base := searchChange("corp")
	for name, tc := range map[string]struct {
		change func(*accessapi.LDAPChange)
		field  string
	}{
		"no key":              {func(c *accessapi.LDAPChange) { c.Key = "" }, FieldKey},
		"slash in key":        {func(c *accessapi.LDAPChange) { c.Key = "a/b" }, FieldKey},
		"control in key":      {func(c *accessapi.LDAPChange) { c.Key = "a\x00b" }, FieldKey},
		"no url":              {func(c *accessapi.LDAPChange) { c.URL = nil }, FieldURL},
		"http url":            {func(c *accessapi.LDAPChange) { c.URL = ptr("http://ldap.example.com") }, FieldURL},
		"no host":             {func(c *accessapi.LDAPChange) { c.URL = ptr("ldap:///dc=example") }, FieldURL},
		"unparsable url":      {func(c *accessapi.LDAPChange) { c.URL = ptr("ldap://[::1") }, FieldURL},
		"credentials in url":  {func(c *accessapi.LDAPChange) { c.URL = ptr("ldap://u:p@ldap.example.com") }, FieldURL},
		"query in url":        {func(c *accessapi.LDAPChange) { c.URL = ptr("ldap://ldap.example.com/dc=x?cn") }, FieldURL},
		"bad base dn":         {func(c *accessapi.LDAPChange) { c.URL = ptr("ldap://ldap.example.com/not-a-dn") }, FieldURL},
		"no way to a user":    {func(c *accessapi.LDAPChange) { c.SearchFilter = ptr("") }, ""},
		"pattern without {0}": {func(c *accessapi.LDAPChange) { c.UserDNPattern = ptr("uid=x,ou=people") }, FieldUserDNPattern},
		"pattern not a dn":    {func(c *accessapi.LDAPChange) { c.UserDNPattern = ptr("{0}") }, FieldUserDNPattern},
		"pattern trailing \\": {func(c *accessapi.LDAPChange) { c.UserDNPattern = ptr("uid={0}\\") }, FieldUserDNPattern},
		"pattern empty rdn":   {func(c *accessapi.LDAPChange) { c.UserDNPattern = ptr("uid={0},,ou=x") }, FieldUserDNPattern},
		"pattern empty ava":   {func(c *accessapi.LDAPChange) { c.UserDNPattern = ptr("uid={0}+,ou=x") }, FieldUserDNPattern},
		"filter without {0}":  {func(c *accessapi.LDAPChange) { c.SearchFilter = ptr("(uid=x)") }, FieldSearchFilter},
		"unbalanced filter":   {func(c *accessapi.LDAPChange) { c.SearchFilter = ptr("(uid={0}") }, FieldSearchFilter},
		"closed early":        {func(c *accessapi.LDAPChange) { c.SearchFilter = ptr("uid={0})(") }, FieldSearchFilter},
		"bad search base":     {func(c *accessapi.LDAPChange) { c.SearchBase = ptr("ou=people|") }, FieldSearchBase},
		"bad manager dn":      {func(c *accessapi.LDAPChange) { c.ManagerDN = ptr("manager") }, FieldManagerDN},
		"password, no dn":     {func(c *accessapi.LDAPChange) { c.ManagerDN = ptr("") }, FieldManagerPassword},
		"bad email attribute": {func(c *accessapi.LDAPChange) { c.EmailAttribute = ptr("e mail") }, FieldEmailAttribute},
	} {
		t.Run(name, func(t *testing.T) {
			svc, configs := newService(t)
			ch := base
			tc.change(&ch)
			_, err := svc.Create(t.Context(), ch)
			var refused *accessapi.LDAPInvalid
			if !errors.As(err, &refused) || refused.Field != tc.field || !errors.Is(err, kernel.ErrInvalid) {
				t.Fatalf("got %v, want a refusal of %q", err, tc.field)
			}
			if strings.Contains(err.Error(), password) {
				t.Fatal("the refusal names the password")
			}
			if all, _ := configs.List(t.Context(), configdoc.ConfigLDAP); len(all) != 0 {
				t.Fatal("a refused connection was stored")
			}
		})
	}
	for name, ch := range map[string]accessapi.LDAPChange{
		"entra filter without parentheses": {
			Key: "entra", URL: ptr("ldaps://ldap.example.com"), SearchFilter: ptr("sAMAccountName={0}"),
		},
		"escaped dn and oid attribute": {
			Key: "x", URL: ptr("LDAP://ldap.example.com:389/dc=a\\,b,dc=c"),
			UserDNPattern: ptr("2.5.4.3={0}+ou=a,ou=people"), EmailAttribute: ptr("mail;lang-en"),
			SearchFilter: ptr("(cn=\\28{0}\\29)"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc, _ := newService(t)
			if _, err := svc.Create(t.Context(), ch); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUnitPlanAndApply(t *testing.T) {
	svc, configs := newService(t)
	ctx := t.Context()
	must[accessapi.LDAPConnection](t)(svc.Create(ctx, searchChange("kept")))
	must[accessapi.LDAPConnection](t)(svc.Create(ctx, searchChange("gone")))
	changes := []accessapi.LDAPChange{
		{Key: "kept", Enabled: ptr(false)},
		{Key: "gone", Delete: true},
		{Key: "never", Delete: true},
		{Key: "new", URL: ptr("ldap://ldap.example.com"), UserDNPattern: ptr("uid={0}")},
	}
	want := []accessapi.LDAPResult{
		{Key: "kept", Outcome: accessapi.LDAPUpdated},
		{Key: "gone", Outcome: accessapi.LDAPDeleted},
		{Key: "never", Outcome: accessapi.LDAPUnchanged},
		{Key: "new", Outcome: accessapi.LDAPCreated},
	}
	planned := must[[]accessapi.LDAPResult](t)(svc.Plan(ctx, changes))
	if fmt.Sprint(planned) != fmt.Sprint(want) {
		t.Fatalf("planned %v", planned)
	}
	if _, err := svc.Get(ctx, "new"); !errors.Is(err, ErrNotFound) {
		t.Fatal("Plan wrote")
	}
	applied := must[[]accessapi.LDAPResult](t)(svc.Apply(ctx, changes))
	if fmt.Sprint(applied) != fmt.Sprint(want) {
		t.Fatalf("applied %v", applied)
	}
	kept := must[accessapi.LDAPConnection](t)(svc.Get(ctx, "kept"))
	if kept.Enabled || kept.SearchFilter != "(uid={0})" || storedPassword(t, configs, "kept") != password {
		t.Fatalf("a field-by-field change lost what it left out: %+v", kept)
	}
	before := must[configdoc.ConfigDocument](t)(configs.Get(ctx, configdoc.ConfigLDAP, "kept"))
	again := must[[]accessapi.LDAPResult](t)(svc.Apply(ctx, changes))
	for _, r := range again {
		if r.Outcome != accessapi.LDAPUnchanged {
			t.Fatalf("a second apply changed %v", again)
		}
	}
	after := must[configdoc.ConfigDocument](t)(configs.Get(ctx, configdoc.ConfigLDAP, "kept"))
	if !bytes.Equal(before.Document, after.Document) {
		t.Fatal("a second apply rewrote the document and its revision")
	}
	_, err := svc.Plan(ctx, []accessapi.LDAPChange{
		{Key: "kept", ManagerDN: ptr("")},
		{Key: "bad", URL: ptr("http://x")},
	})
	var refused *accessapi.LDAPInvalid
	if !errors.As(err, &refused) || refused.Key != "bad" || refused.Field != FieldURL {
		t.Fatalf("got %v", err)
	}
	if _, err := svc.Plan(ctx, []accessapi.LDAPChange{{Key: "kept", ManagerPassword: ptr("x"), ManagerDN: ptr("")}}); !errors.As(err, &refused) ||
		refused.Field != FieldManagerPassword {
		t.Fatalf("a password without a manager DN: %v", err)
	}
}

func TestUnitSnapshotRevisions(t *testing.T) {
	svc, _ := newService(t)
	ctx := t.Context()
	empty := must[Snapshot](t)(svc.Snapshot(ctx))
	must[accessapi.LDAPConnection](t)(svc.Create(ctx, searchChange("corp")))
	first := must[Snapshot](t)(svc.Snapshot(ctx))
	c, ok := first.Connection("corp")
	if !ok || c.ManagerPassword.Reveal() != password || c.ManagerPassword.IsZero() || c.Revision == "" ||
		first.Revision == empty.Revision {
		t.Fatalf("snapshot %+v", first)
	}
	if _, ok := first.Connection("missing"); ok {
		t.Fatal("found a missing connection")
	}
	if again := must[Snapshot](t)(svc.Snapshot(ctx)); again.Revision != first.Revision {
		t.Fatal("a read changed the revision")
	}
	// Disabling, a password change and a re-create each give a revision
	// never seen before; the delete between them leaves no connection, whose
	// snapshot is the empty one.
	seen := map[string]bool{first.Revision: true}
	disabled := searchChange("corp")
	disabled.Enabled = ptr(false)
	newPassword := searchChange("corp")
	newPassword.ManagerPassword = ptr("another-private")
	for _, step := range []func() error{
		func() error { _, err := svc.Replace(ctx, disabled); return err },
		func() error { _, err := svc.Replace(ctx, newPassword); return err },
		func() error { return svc.Delete(ctx, "corp") },
		func() error { _, err := svc.Create(ctx, searchChange("corp")); return err },
	} {
		seen[empty.Revision] = false
		if err := step(); err != nil {
			t.Fatal(err)
		}
		s := must[Snapshot](t)(svc.Snapshot(ctx))
		if seen[s.Revision] {
			t.Fatal("a change repeated a revision")
		}
		if s.Revision == empty.Revision && len(s.Connections) != 0 {
			t.Fatal("a snapshot with a connection has the empty revision")
		}
		seen[s.Revision] = true
	}
}

func TestUnitSnapshotRefusals(t *testing.T) {
	svc, _ := newService(t)
	if _, err := (Service{Configs: svc.Configs}).Snapshot(t.Context()); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("a snapshot without a transaction", err)
	}
	failure := errors.New("guard failure")
	svc.Guard = func(context.Context) error { return failure }
	if _, err := svc.Snapshot(t.Context()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
}

func TestUnitSecretNeverPrints(t *testing.T) {
	c := Connection{LDAPConnection: accessapi.LDAPConnection{Key: "corp"}, ManagerPassword: Secret{password}}
	var logged bytes.Buffer
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("connection", "c", c, "secret", c.ManagerPassword)
	encoded, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := c.ManagerPassword.MarshalText()
	for _, out := range []string{
		fmt.Sprint(c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c), fmt.Sprintf("%s", c.ManagerPassword),
		logged.String(), string(encoded), string(text),
	} {
		if strings.Contains(out, password) || !strings.Contains(out, redacted) {
			t.Fatalf("the password printed: %s", out)
		}
	}
}

// failingConfigs fails the named operation.
type failingConfigs struct {
	configdoc.ConfigStore
	op string
}

var errStore = errors.New("store failure")

func (f failingConfigs) Get(ctx context.Context, k configdoc.ConfigKind, key string) (configdoc.ConfigDocument, error) {
	if f.op == "get" {
		return configdoc.ConfigDocument{}, errStore
	}
	return f.ConfigStore.Get(ctx, k, key) //nolint:wrapcheck // test double
}

func (f failingConfigs) List(ctx context.Context, k configdoc.ConfigKind) ([]configdoc.ConfigDocument, error) {
	if f.op == "list" {
		return nil, errStore
	}
	return f.ConfigStore.List(ctx, k) //nolint:wrapcheck // test double
}

func (f failingConfigs) Put(ctx context.Context, d configdoc.ConfigDocument) error {
	if f.op == "put" {
		return errStore
	}
	return f.ConfigStore.Put(ctx, d) //nolint:wrapcheck // test double
}

func (f failingConfigs) Delete(ctx context.Context, k configdoc.ConfigKind, key string) error {
	if f.op == "delete" {
		return errStore
	}
	return f.ConfigStore.Delete(ctx, k, key) //nolint:wrapcheck // test double
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errStore }

func TestUnitStoreFailures(t *testing.T) {
	ctx := t.Context()
	for _, op := range []string{"get", "list", "put", "delete", "random"} {
		t.Run(op, func(t *testing.T) {
			svc, configs := newService(t)
			must[accessapi.LDAPConnection](t)(svc.Create(ctx, searchChange("corp")))
			svc.Configs = failingConfigs{ConfigStore: configs, op: op}
			if op == "random" {
				svc.Random = failingReader{}
			}
			errs := []error{
				second(svc.Get(ctx, "corp")), second(svc.List(ctx)), second(svc.Snapshot(ctx)),
				second(svc.Create(ctx, searchChange("other"))), second(svc.Replace(ctx, searchChange("corp"))),
				svc.Delete(ctx, "corp"),
				second(svc.Plan(ctx, []accessapi.LDAPChange{{Key: "corp", Enabled: ptr(false)}})),
				second(svc.Apply(ctx, []accessapi.LDAPChange{{Key: "corp", Enabled: ptr(false)}, {Key: "corp", Delete: true}})),
			}
			if !errors.Is(errors.Join(errs...), errStore) {
				t.Fatalf("no operation reported the failure: %v", errs)
			}
		})
	}
}

func TestUnitCorruptDocuments(t *testing.T) {
	ctx := t.Context()
	for name, body := range map[string]string{"not json": "{", "another key": `{"key":"other"}`} {
		t.Run(name, func(t *testing.T) {
			svc, configs := newService(t)
			if err := configs.Put(ctx, configdoc.ConfigDocument{
				Kind: configdoc.ConfigLDAP, Key: "corp", Document: []byte(body),
			}); err != nil {
				t.Fatal(err)
			}
			for _, err := range []error{second(svc.Get(ctx, "corp")), second(svc.List(ctx)), second(svc.Snapshot(ctx))} {
				if !errors.Is(err, kernel.ErrStore) {
					t.Fatal(err)
				}
			}
		})
	}
}

func second[T any](_ T, err error) error { return err }
