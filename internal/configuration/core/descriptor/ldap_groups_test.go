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
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func ldapGroupDeps(t *testing.T, st applyStore) Deps {
	t.Helper()
	d := depsOf(t, st)
	d.LDAPGroups = accesstest.Over(t, st).API().LDAPGroups
	return d
}

func TestUnitLDAPGroupsParseMapsEveryField(t *testing.T) {
	p, err := Parse([]byte(`security:
  ldapGroupSettings:
    devs:
      name: devs
      enabledLdap: corp
      groupBaseDn: ou=groups
      groupNameAttribute: cn
      groupMemberAttribute: uniqueMember
      subTree: true
      filter: (objectClass=groupOfUniqueNames)
      descriptionAttribute: null
      strategy: STATIC
      forceAttributeSearch: false
    gone: null
`), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.LDAPGroups) != 2 || p.LDAPGroups[0].Name != "devs" || !p.LDAPGroups[1].Delete ||
		p.LDAPGroups[1].Name != "gone" || p.LDAPGroupsCleared {
		t.Fatalf("parsed %+v", p.LDAPGroups)
	}
	c := p.LDAPGroups[0]
	got := strings.Join([]string{
		*c.EnabledLDAP, *c.GroupBaseDN, *c.GroupNameAttribute, *c.GroupMemberAttribute, *c.Filter,
		*c.DescriptionAttribute, *c.Strategy,
	}, ",")
	if got != "corp,ou=groups,cn,uniqueMember,(objectClass=groupOfUniqueNames),,STATIC" ||
		!*c.SubTree || *c.ForceAttributeSearch {
		t.Fatalf("mapped %s %+v", got, c)
	}
	// The section set to null deletes every setting.
	p, err = Parse([]byte("security:\n  ldapGroupSettings: ~\n"), noEnv)
	if err != nil || !p.LDAPGroupsCleared {
		t.Fatalf("a null section %+v, %v", p, err)
	}
}

func TestUnitLDAPGroupsParseRefusals(t *testing.T) {
	for doc, want := range map[string]string{
		"security:\n  ldapGroupSettings: []\n":                           "must be a mapping of names to group settings",
		"security:\n  ldapGroupSettings:\n    devs: 3\n":                 "security.ldapGroupSettings.devs must be a mapping or null",
		"security:\n  ldapGroupSettings:\n    devs:\n      name: ops\n":  "name ops does not match the entry name",
		"security:\n  ldapGroupSettings:\n    devs:\n      nested: 1\n":  "unknown field nested",
		"security:\n  ldapGroupSettings:\n    devs:\n      subTree: x\n": "subTree must be a boolean",
		"security:\n  ldapGroupSettings:\n    devs:\n      filter: 1\n":  "filter must be a string",
	} {
		_, err := Parse([]byte(doc), noEnv)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", doc, err, want)
		}
	}
}

func TestUnitLDAPGroupsApplyThroughAccess(t *testing.T) {
	db := inmem.New()
	ctx := context.Background()
	d := ldapGroupDeps(t, db)
	doc := "security:\n  ldapGroupSettings:\n    devs:\n      enabledLdap: corp\n      groupMemberAttribute: uniqueMember\n" +
		"    ops:\n      enabledLdap: corp\n      strategy: DYNAMIC\n      groupMemberAttribute: memberOf\n"
	p, err := Parse([]byte(doc), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := Apply(ctx, d, p, now, true); err != nil || strings.Join(res.Created, ",") !=
		"security.ldapGroupSettings.devs,security.ldapGroupSettings.ops" {
		t.Fatal("dry run", res, err)
	}
	if all, _ := d.LDAPGroups.GroupSettings(ctx); len(all) != 0 {
		t.Fatal("the dry run wrote", all)
	}
	if _, err := Apply(ctx, d, p, now, false); err != nil {
		t.Fatal(err)
	}
	all, err := d.LDAPGroups.GroupSettings(ctx)
	if err != nil || len(all) != 2 || all[0].GroupNameAttribute != "cn" || all[0].Strategy != accessapi.LDAPStatic ||
		all[1].Strategy != accessapi.LDAPDynamic {
		t.Fatal(all, err)
	}
	for _, step := range []struct{ doc, outcome, names string }{
		{"security:\n  ldapGroupSettings:\n    devs:\n      subTree: true\n", "updated", "security.ldapGroupSettings.devs"},
		{"security:\n  ldapGroupSettings:\n    devs:\n      subTree: true\n", "unchanged", "security.ldapGroupSettings.devs"},
		{"security:\n  ldapGroupSettings:\n    devs: null\n", "deleted", "security.ldapGroupSettings.devs"},
		{"security:\n  ldapGroupSettings:\n    devs: null\n", "unchanged", "security.ldapGroupSettings.devs"},
		{"security:\n  ldapGroupSettings: ~\n", "deleted", "security.ldapGroupSettings.ops"},
		{"security:\n  ldapGroupSettings: ~\n", "unchanged", "security.ldapGroupSettings"},
	} {
		p, err := Parse([]byte(step.doc), noEnv)
		if err != nil {
			t.Fatal(err)
		}
		res, err := Apply(ctx, d, p, now, false)
		lists := map[string][]string{"updated": res.Updated, "unchanged": res.Unchanged, "deleted": res.Deleted}
		if err != nil || strings.Join(lists[step.outcome], ",") != step.names {
			t.Fatal(step.doc, res, err)
		}
	}
	// A setting access refuses is refused with the descriptor's names.
	p, err = Parse([]byte("security:\n  ldapGroupSettings:\n    devs:\n      groupBaseDn: not-a-dn\n"), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(ctx, d, p, now, false); !errors.Is(err, ErrInvalid) ||
		!strings.Contains(err.Error(), "security.ldapGroupSettings.devs") || !strings.Contains(err.Error(), "groupBaseDn") {
		t.Fatal(err)
	}
}

func TestUnitLDAPGroupsApplyFailures(t *testing.T) {
	ctx := context.Background()
	p, err := Parse([]byte("security:\n  ldapGroupSettings:\n    devs:\n      enabledLdap: corp\n"), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	db := inmem.New()
	if _, err := Apply(ctx, depsOf(t, db), p, now, false); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("a descriptor with group settings and no binding", err)
	}
	failing := faulty{Store: db, fail: "config get"}
	if _, err := Apply(ctx, ldapGroupDeps(t, failing), p, now, false); !errors.Is(err, errBoom) || errors.Is(err, ErrInvalid) {
		t.Fatal("a store failure while planning", err)
	}
	d := depsOf(t, db)
	d.LDAPGroups = failingGroups{}
	if _, err := Apply(ctx, d, p, now, false); !errors.Is(err, errLDAPApply) {
		t.Fatal("a failed write", err)
	}
	cleared, err := Parse([]byte("security:\n  ldapGroupSettings: ~\n"), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(ctx, d, cleared, now, false); !errors.Is(err, errLDAPApply) {
		t.Fatal("a failed read of the settings a null section deletes", err)
	}
}

// failingGroups plans every change, fails its write and its read.
type failingGroups struct{ accessapi.LDAPGroupSettings }

func (failingGroups) PlanGroups(_ context.Context, changes []accessapi.LDAPGroupChange) ([]accessapi.LDAPResult, error) {
	return []accessapi.LDAPResult{{Key: changes[0].Name, Outcome: accessapi.LDAPCreated}}, nil
}

func (failingGroups) ApplyGroups(context.Context, []accessapi.LDAPGroupChange) ([]accessapi.LDAPResult, error) {
	return nil, errLDAPApply
}

func (failingGroups) GroupSettings(context.Context) ([]accessapi.LDAPGroupSetting, error) {
	return nil, errLDAPApply
}

func TestUnitLDAPGroupsExportProjection(t *testing.T) {
	if out, err := exportLDAPGroups(context.Background(), Deps{}); out != nil || err != nil {
		t.Fatal("an export without the binding", out, err)
	}
	if _, err := exportLDAPGroups(context.Background(), Deps{LDAPGroups: failingGroups{}}); !errors.Is(err, errLDAPApply) {
		t.Fatal(err)
	}
	db := inmem.New()
	p, err := Parse([]byte("security:\n  ldapGroupSettings:\n    devs:\n      enabledLdap: corp\n"+
		"      groupMemberAttribute: uniqueMember\n      filter: (objectClass=groupOfNames)\n"), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(context.Background(), ldapGroupDeps(t, db), p, now, false); err != nil {
		t.Fatal(err)
	}
	out := exportSecurity{}
	if out.LDAPGroups, err = exportLDAPGroups(context.Background(), ldapGroupDeps(t, db)); err != nil {
		t.Fatal(err)
	}
	body, err := xml.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	// 7.x's element order; groupBaseDn and enabledLdap are written empty,
	// descriptionAttribute, empty, is left out.
	want := "<ldapGroupSettings><ldapGroupSetting><name>devs</name><groupBaseDn></groupBaseDn>" +
		"<groupNameAttribute>cn</groupNameAttribute><groupMemberAttribute>uniqueMember</groupMemberAttribute>" +
		"<subTree>false</subTree><filter>(objectClass=groupOfNames)</filter><strategy>STATIC</strategy>" +
		"<enabledLdap>corp</enabledLdap><forceAttributeSearch>false</forceAttributeSearch></ldapGroupSetting>" +
		"</ldapGroupSettings>"
	if !strings.Contains(string(body), want) {
		t.Fatalf("%s", body)
	}
}
