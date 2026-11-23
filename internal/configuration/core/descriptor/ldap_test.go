package descriptor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
)

const ldapPrivate = "ldap-private-fixture"

func ldapDeps(t *testing.T, st applyStore) Deps {
	t.Helper()
	d := depsOf(t, st)
	d.LDAP = accesstest.Over(t, st).API().LDAP
	return d
}

func TestUnitLDAPParseMapsEveryField(t *testing.T) {
	p, err := Parse([]byte(`security:
  ldapSettings:
    corp:
      key: corp
      enabled: false
      ldapUrl: ldap://h/dc=x
      userDnPattern: uid={0}
      emailAttribute: null
      autoCreateUser: false
      ldapPoisoningProtection: false
      allowUserToAccessProfile: true
      pagingSupportEnabled: false
      search:
        searchFilter: (uid={0})
        searchBase: ou=p
        searchSubTree: false
        managerDn: cn=m
        managerPassword: ${LDAP_PASSWORD}
    gone: null
`), env(map[string]string{"LDAP_PASSWORD": ldapPrivate}))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.LDAP) != 2 || !p.LDAP[1].Delete || p.LDAP[1].Key != "gone" || p.Changes != 15 {
		t.Fatalf("parsed %+v", p)
	}
	c := p.LDAP[0]
	strs := []*string{c.URL, c.UserDNPattern, c.EmailAttribute, c.SearchFilter, c.SearchBase, c.ManagerDN, c.ManagerPassword}
	flags := []*bool{c.Enabled, c.AutoCreateUser, c.PoisoningProtection, c.AllowUserToAccessProfile, c.PagingSupportEnabled, c.SearchSubTree}
	var got []string
	for _, s := range strs {
		got = append(got, *s)
	}
	for _, f := range flags {
		if *f {
			got = append(got, "true")
		} else {
			got = append(got, "false")
		}
	}
	want := "ldap://h/dc=x,uid={0},,(uid={0}),ou=p,cn=m," + ldapPrivate + ",false,false,false,true,false,false"
	if strings.Join(got, ",") != want {
		t.Fatalf("mapped %s", strings.Join(got, ","))
	}
}

func TestUnitLDAPApplyThroughAccess(t *testing.T) {
	db := inmem.New()
	ctx := context.Background()
	doc := "security:\n  ldapSettings:\n    corp:\n      ldapUrl: ldap://h\n      userDnPattern: uid={0}\n" +
		"      search:\n        managerDn: cn=m\n        managerPassword: " + ldapPrivate + "\n"
	p, err := Parse([]byte(doc), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	d := ldapDeps(t, db)
	if res, err := Apply(ctx, d, p, now, true); err != nil || strings.Join(res.Created, ",") != "security.ldapSettings.corp" {
		t.Fatal("dry run", res, err)
	}
	if all, _ := db.Configs().List(ctx, configdoc.ConfigLDAP); len(all) != 0 {
		t.Fatal("the dry run wrote")
	}
	if res, err := Apply(ctx, d, p, now, false); err != nil || strings.Join(res.Created, ",") != "security.ldapSettings.corp" {
		t.Fatal(res, err)
	}
	for _, step := range []struct{ doc, outcome string }{
		{"security:\n  ldapSettings:\n    corp:\n      enabled: false\n", "updated"},
		{"security:\n  ldapSettings:\n    corp:\n      enabled: false\n", "unchanged"},
		{"security:\n  ldapSettings:\n    corp: null\n", "deleted"},
		{"security:\n  ldapSettings:\n    corp: null\n", "unchanged"},
	} {
		doc, outcome := step.doc, step.outcome
		p, err := Parse([]byte(doc), noEnv)
		if err != nil {
			t.Fatal(err)
		}
		res, err := Apply(ctx, d, p, now, false)
		lists := map[string][]string{"updated": res.Updated, "unchanged": res.Unchanged, "deleted": res.Deleted}
		if err != nil || strings.Join(lists[outcome], ",") != "security.ldapSettings.corp" {
			t.Fatal(doc, res, err)
		}
	}
}

func TestUnitLDAPApplyFailures(t *testing.T) {
	ctx := context.Background()
	p, err := Parse([]byte("security:\n  ldapSettings:\n    corp:\n      ldapUrl: ldap://h\n      userDnPattern: uid={0}\n"), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	db := inmem.New()
	if _, err := Apply(ctx, depsOf(t, db), p, now, false); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("a descriptor with connections and no LDAP binding", err)
	}
	failing := faulty{Store: db, fail: "config get"}
	if _, err := Apply(ctx, ldapDeps(t, failing), p, now, false); !errors.Is(err, errBoom) || errors.Is(err, ErrInvalid) {
		t.Fatal("a store failure while planning", err)
	}
	if _, err := Apply(ctx, Deps{
		Tx: db, Repositories: depsOf(t, db).Repositories, Configs: db.Configs(),
		LDAP: failingLDAP{},
	}, p, now, false); !errors.Is(err, errLDAPApply) {
		t.Fatal("a failed write", err)
	}
}

var errLDAPApply = errors.New("ldap apply failure")

// failingLDAP plans every change and fails its write.
type failingLDAP struct{ accessapi.LDAPSettings }

func (failingLDAP) Plan(_ context.Context, changes []accessapi.LDAPChange) ([]accessapi.LDAPResult, error) {
	return []accessapi.LDAPResult{{Key: changes[0].Key, Outcome: accessapi.LDAPCreated}}, nil
}

func (failingLDAP) Apply(context.Context, []accessapi.LDAPChange) ([]accessapi.LDAPResult, error) {
	return nil, errLDAPApply
}

func (failingLDAP) Connections(context.Context) ([]accessapi.LDAPConnection, error) {
	return nil, errLDAPApply
}

// TestUnitLDAPConnectionsLeftOutOfExport pins 7.161.26's descriptor, whose
// SecurityDescriptor has no ldapSettings: a connection the descriptor wrote
// lives in access and the XML export carries no element for it.
func TestUnitLDAPConnectionsLeftOutOfExport(t *testing.T) {
	db := inmem.New()
	d := ldapDeps(t, db)
	p, err := Parse([]byte("security:\n  ldapSettings:\n    corp-ldap:\n      ldapUrl: ldap://h\n      userDnPattern: uid={0}\n"), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(context.Background(), d, p, now, false); err != nil {
		t.Fatal(err)
	}
	all, err := d.LDAP.Connections(context.Background())
	if err != nil || len(all) != 1 || all[0].Key != "corp-ldap" {
		t.Fatal("the descriptor did not write the connection to access", all, err)
	}
	_, body := exportXML(t, d)
	for _, absent := range []string{"ldapSetting", "corp-ldap", "ldap://h"} {
		if strings.Contains(body, absent) {
			t.Error("LDAP connection reached the XML", absent)
		}
	}
}
