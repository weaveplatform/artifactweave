package ldap

import (
	"errors"
	"strings"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
)

func groupChange(name string) accessapi.LDAPGroupChange {
	return accessapi.LDAPGroupChange{
		Name: name, EnabledLDAP: ptr("corp"), GroupBaseDN: ptr("ou=groups"),
		GroupMemberAttribute: ptr("uniqueMember"), Filter: ptr("(objectClass=groupOfNames)"),
	}
}

func TestUnitGroupSettingsCRUD(t *testing.T) {
	svc, configs := newService(t)
	ctx := t.Context()
	g, err := svc.CreateGroupSetting(ctx, groupChange("devs"))
	// 7.x's LdapGroupSetting defaults fill what the change leaves out.
	if err != nil || g.GroupNameAttribute != "cn" || g.Strategy != accessapi.LDAPStatic || g.SubTree ||
		g.ForceAttributeSearch || g.DescriptionAttribute != "" {
		t.Fatalf("created %+v, %v", g, err)
	}
	if _, err := svc.CreateGroupSetting(ctx, groupChange("devs")); !errors.Is(err, ErrGroupSettingExists) ||
		!errors.Is(err, kernel.ErrInvalid) {
		t.Fatalf("a second create: %v", err)
	}
	doc, err := configs.Get(ctx, configdoc.ConfigLDAPGroup, "devs")
	if err != nil || !doc.Enabled {
		t.Fatalf("stored %+v, %v", doc, err)
	}
	// A replace starts from the defaults again, as the Access PUT does.
	replaced, err := svc.ReplaceGroupSetting(ctx, accessapi.LDAPGroupChange{
		Name: "devs", EnabledLDAP: ptr(""), Strategy: ptr("DYNAMIC"), SubTree: ptr(true), ForceAttributeSearch: ptr(true),
		DescriptionAttribute: ptr("description"), GroupNameAttribute: ptr("ou"),
	})
	if err != nil || replaced.GroupBaseDN != "" || replaced.Strategy != accessapi.LDAPDynamic || !replaced.SubTree ||
		!replaced.ForceAttributeSearch || replaced.GroupNameAttribute != "ou" {
		t.Fatalf("replaced %+v, %v", replaced, err)
	}
	if doc, _ := configs.Get(ctx, configdoc.ConfigLDAPGroup, "devs"); doc.Enabled {
		t.Fatal("a setting with no connection is stored enabled")
	}
	if got, err := svc.GroupSetting(ctx, "devs"); err != nil || got != replaced {
		t.Fatalf("read back %+v, %v", got, err)
	}
	if _, err := svc.ReplaceGroupSetting(ctx, groupChange("gone")); !errors.Is(err, ErrGroupSettingNotFound) {
		t.Fatalf("replace of a missing setting: %v", err)
	}
	if _, err := svc.GroupSetting(ctx, "gone"); !errors.Is(err, ErrGroupSettingNotFound) {
		t.Fatalf("read of a missing setting: %v", err)
	}
	must[accessapi.LDAPGroupSetting](t)(svc.CreateGroupSetting(ctx, groupChange("ops")))
	all, err := svc.GroupSettings(ctx)
	if err != nil || len(all) != 2 || all[0].Name != "devs" || all[1].Name != "ops" {
		t.Fatalf("listed %+v, %v", all, err)
	}
	if err := svc.DeleteGroupSetting(ctx, "devs"); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteGroupSetting(ctx, "devs"); !errors.Is(err, ErrGroupSettingNotFound) {
		t.Fatalf("a second delete: %v", err)
	}
}

func TestUnitGroupValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		set   func(*accessapi.LDAPGroupSetting)
		field string
	}{
		{"no name", func(g *accessapi.LDAPGroupSetting) { g.Name = "" }, FieldGroupName},
		{"a slash", func(g *accessapi.LDAPGroupSetting) { g.Name = "a/b" }, FieldGroupName},
		{"a control", func(g *accessapi.LDAPGroupSetting) { g.Name = "a\x00b" }, FieldGroupName},
		{"a control key", func(g *accessapi.LDAPGroupSetting) { g.EnabledLDAP = "c\n" }, FieldGroupEnabledLDAP},
		{"a strategy", func(g *accessapi.LDAPGroupSetting) { g.Strategy = "HIERARCHY" }, FieldGroupStrategy},
		{"a base DN", func(g *accessapi.LDAPGroupSetting) { g.GroupBaseDN = "groups" }, FieldGroupBaseDN},
		{"a name attribute", func(g *accessapi.LDAPGroupSetting) { g.GroupNameAttribute = "" }, FieldGroupNameAttr},
		{"a member attribute", func(g *accessapi.LDAPGroupSetting) { g.GroupMemberAttribute = "a b" }, FieldGroupMemberAttr},
		{"a description", func(g *accessapi.LDAPGroupSetting) { g.DescriptionAttribute = "a=b" }, FieldGroupDescription},
		{"a filter", func(g *accessapi.LDAPGroupSetting) { g.Filter = "(objectClass=x" }, FieldGroupFilter},
	} {
		g := MergeGroup(GroupDefaults("devs"), groupChange("devs"))
		tc.set(&g)
		var invalid *accessapi.LDAPGroupInvalid
		if err := ValidateGroup(g); !errors.As(err, &invalid) || invalid.Field != tc.field ||
			!errors.Is(err, kernel.ErrInvalid) || !strings.HasPrefix(err.Error(), tc.field+" ") {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	// The Entra ID form with a matching rule is a member attribute.
	g := MergeGroup(GroupDefaults("devs"), groupChange("devs"))
	g.GroupMemberAttribute = "member:1.2.840.113556.1.4.1941:"
	if err := ValidateGroup(g); err != nil {
		t.Fatal(err)
	}
	svc, _ := newService(t)
	bad := groupChange("devs")
	bad.Strategy = ptr("NESTED")
	if _, err := svc.CreateGroupSetting(t.Context(), bad); err == nil {
		t.Fatal("an invalid setting was created")
	}
}

func TestUnitGroupPlanAndApply(t *testing.T) {
	svc, _ := newService(t)
	ctx := t.Context()
	must[accessapi.LDAPGroupSetting](t)(svc.CreateGroupSetting(ctx, groupChange("devs")))
	before := must[Snapshot](t)(svc.Snapshot(ctx))
	changes := []accessapi.LDAPGroupChange{
		{Name: "devs", SubTree: ptr(true)},
		groupChange("ops"),
		{Name: "gone", Delete: true},
		{Name: "devs", SubTree: ptr(true), Strategy: ptr("STATIC")},
	}
	plan, err := svc.PlanGroups(ctx, changes)
	if err != nil || plan[0].Outcome != accessapi.LDAPUpdated || plan[1].Outcome != accessapi.LDAPCreated ||
		plan[2].Outcome != accessapi.LDAPUnchanged {
		t.Fatalf("plan %+v, %v", plan, err)
	}
	if after := must[Snapshot](t)(svc.Snapshot(ctx)); after.Revision != before.Revision {
		t.Fatal("planning wrote")
	}
	if _, err := svc.ApplyGroups(ctx, changes[:3]); err != nil {
		t.Fatal(err)
	}
	// The same changes again write nothing and keep every revision.
	applied := must[Snapshot](t)(svc.Snapshot(ctx))
	again, err := svc.ApplyGroups(ctx, changes[:3])
	if err != nil || again[0].Outcome != accessapi.LDAPUnchanged || again[1].Outcome != accessapi.LDAPUnchanged {
		t.Fatalf("again %+v, %v", again, err)
	}
	if must[Snapshot](t)(svc.Snapshot(ctx)).Revision != applied.Revision {
		t.Fatal("an unchanged apply changed a revision")
	}
	g, ok := applied.GroupSetting("devs")
	if !ok || !g.SubTree || g.Revision == "" || len(applied.GroupsOf("corp")) != 2 || len(applied.GroupsOf("x")) != 0 {
		t.Fatalf("snapshot %+v", applied)
	}
	if _, ok := applied.GroupSetting("gone"); ok {
		t.Fatal("found a missing setting")
	}
	if res, err := svc.ApplyGroups(ctx, []accessapi.LDAPGroupChange{{Name: "ops", Delete: true}}); err != nil ||
		res[0].Outcome != accessapi.LDAPDeleted {
		t.Fatalf("delete %+v, %v", res, err)
	}
	// A refusal names its setting.
	var invalid *accessapi.LDAPGroupInvalid
	if _, err := svc.PlanGroups(ctx, []accessapi.LDAPGroupChange{{Name: "devs", GroupBaseDN: ptr("x")}}); !errors.As(
		err, &invalid) || invalid.Name != "devs" || invalid.Field != FieldGroupBaseDN {
		t.Fatalf("refusal %v", err)
	}
}

func TestUnitGroupStoreFailures(t *testing.T) {
	ctx := t.Context()
	for _, op := range []string{"get", "list", "put", "delete", "random"} {
		t.Run(op, func(t *testing.T) {
			svc, configs := newService(t)
			must[accessapi.LDAPGroupSetting](t)(svc.CreateGroupSetting(ctx, groupChange("devs")))
			svc.Configs = failingConfigs{ConfigStore: configs, op: op}
			if op == "random" {
				svc.Random = failingReader{}
			}
			errs := []error{
				second(svc.GroupSetting(ctx, "devs")), second(svc.GroupSettings(ctx)), second(svc.Snapshot(ctx)),
				second(svc.CreateGroupSetting(ctx, groupChange("ops"))),
				second(svc.ReplaceGroupSetting(ctx, groupChange("devs"))),
				svc.DeleteGroupSetting(ctx, "devs"),
				second(svc.PlanGroups(ctx, []accessapi.LDAPGroupChange{{Name: "devs", SubTree: ptr(true)}})),
				second(svc.ApplyGroups(ctx, []accessapi.LDAPGroupChange{
					{Name: "devs", SubTree: ptr(true)}, {Name: "devs", Delete: true},
				})),
			}
			if !errors.Is(errors.Join(errs...), errStore) {
				t.Fatalf("no operation reported the failure: %v", errs)
			}
		})
	}
}

func TestUnitGroupCorruptDocuments(t *testing.T) {
	ctx := t.Context()
	for name, body := range map[string]string{"not json": "{", "another name": `{"name":"other"}`} {
		t.Run(name, func(t *testing.T) {
			svc, configs := newService(t)
			if err := configs.Put(ctx, configdoc.ConfigDocument{
				Kind: configdoc.ConfigLDAPGroup, Key: "devs", Document: []byte(body),
			}); err != nil {
				t.Fatal(err)
			}
			for _, err := range []error{
				second(svc.GroupSetting(ctx, "devs")), second(svc.GroupSettings(ctx)), second(svc.Snapshot(ctx)),
			} {
				if !errors.Is(err, kernel.ErrStore) {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestUnitStrategyDefaults(t *testing.T) {
	for strategy, want := range map[string]StrategyDefaults{
		"":             {"uniqueMember", "(objectClass=groupOfNames)", "cn", "description"},
		"static":       {"uniqueMember", "(objectClass=groupOfNames)", "cn", "description"},
		"DYNAMIC":      {"memberOf", "(objectClass=group)", "cn", "description"},
		"HIERARCHICAL": {"ou", "(objectClass=organizationalUnit)", "cn", ""},
		"hierarchy":    {"ou", "(objectClass=organizationalUnit)", "cn", ""},
		"nested":       {"uniqueMember", "(objectClass=groupOfNames)", "cn", "description"},
	} {
		if got := DefaultsOf(strategy); got != want {
			t.Errorf("%q: %+v", strategy, got)
		}
	}
}

func TestUnitRealmAttributes(t *testing.T) {
	attrs := RealmAttributes("Devs", accessapi.LDAPStatic, "cn=Devs;x,ou=groups")
	if attrs != "ldapGroupName=Devs;groupsStrategy=STATIC;groupDn=cn=Devs;x,ou=groups" ||
		GroupDN(attrs) != "cn=Devs;x,ou=groups" || GroupDN("ldapGroupName=x") != "" {
		t.Fatal(attrs)
	}
	if LocalName("Devs") != "devs" {
		t.Fatal("not lower case")
	}
	for op, want := range map[Operation][2]bool{
		OpUpdate: {false, true}, OpImport: {true, false}, OpUpdateAndImport: {true, true},
	} {
		if op.imports() != want[0] || op.updates() != want[1] {
			t.Errorf("%s", op)
		}
	}
}
