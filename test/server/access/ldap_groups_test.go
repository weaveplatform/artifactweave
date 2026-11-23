package access_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/pkg/containertest"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

const ldapGroups = "/access/api/v1/ldap/groups"

// staticGroups is the STATIC group setting over the seeded ou=groups.
const staticGroups = `{"name":"corp-groups","enabled_ldap":"corp","group_base_dn":"ou=groups",` +
	`"group_name_attribute":"cn","group_member_attribute":"uniqueMember",` +
	`"filter":"(objectClass=groupOfUniqueNames)","description_attribute":"description",` +
	`"strategy":"STATIC","sub_tree":false,"force_attribute_search":false}`

type foundGroup struct {
	Name     string `json:"group_name"`
	DN       string `json:"group_dn"`
	Status   string `json:"required_update"`
	Describe string `json:"description"`
}

func searchGroups(t *testing.T, h http.Handler, creds map[string]string, query, body string) map[string]foundGroup {
	t.Helper()
	var found []foundGroup
	if err := json.Unmarshal([]byte(ldapCall(t, h, http.MethodPost, ldapGroups+"/search"+query, creds, body,
		http.StatusOK)), &found); err != nil {
		t.Fatal(err)
	}
	out := map[string]foundGroup{}
	for _, f := range found {
		out[f.Name] = f
	}
	return out
}

// seedGroupAccess gives the imported LDAP group developers read on
// libs-local and removes the default Anything permission, which gives the
// auto-join readers group read on every repository, so a request reaches
// the artifact through that membership alone.
func seedGroupAccess(t *testing.T, rt *harness.Runtime) {
	t.Helper()
	ctx := t.Context()
	if err := rt.Store.Permissions().Delete(ctx, "Anything"); err != nil {
		t.Fatal(err)
	}
	if err := rt.Store.Repos().Create(ctx, *repo.Defaults(&repo.Config{Key: "libs-local", Class: repo.Local})); err != nil {
		t.Fatal(err)
	}
	if err := rt.Store.Permissions().Create(ctx, accesstest.Permission{
		Name: "developers-libs",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Groups: map[string]api.Actions{"developers": api.ActionRead}},
			Targets: map[string]accesstest.Target{"libs-local": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
}

// TestAccLDAPGroups drives LDAP-03 (#1834) end to end against a real
// OpenLDAP directory, on SQLite and PostgreSQL: the group settings' CRUD
// through the Access API and the descriptor, the strategies' defaults, the
// search by each strategy, an import, Create a Group's import of one group
// by name, the refresh, the memberships a fresh login publishes, and their
// effect on artifact requests, with Basic credentials and with a token
// issued before the change, and a restart. Refreshes and logins racing a
// settings change are internal/access/core/ldap's sync_test.go and
// internal/access/adapters/ldap's group_publication_test.go.
func TestAccLDAPGroups(t *testing.T) {
	ep := containertest.OpenLDAP(t)
	accesstest.SeedDirectory(t, ep)
	accesstest.SeedDirectoryGroups(t, ep)
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			ldapGroupSync(t, ep, backend)
			// The next backend starts from the directory as seeded.
			accesstest.SetGroupMembers(t, ep, accesstest.Developers, accesstest.Developers.Members...)
		})
	}
}

func ldapGroupSync(t *testing.T, ep containertest.LDAPEndpoint, backend string) {
	cfg := ldapWorldConfig(t, backend)
	opts := harness.AppOptions()
	opts.LDAPRootCAs = ep.CA
	rt := harness.OpenApp(t, cfg, opts)
	open := true
	t.Cleanup(func() {
		if open {
			_ = rt.Close(context.Background())
		}
	})
	h := ldapRouter(rt)
	admin := basic("admin", "pw")
	ldapCall(t, h, http.MethodPut, internalSwitch, admin, `{"enabled":true}`, http.StatusOK)
	ldapCall(t, h, http.MethodPost, "/access/api/v1/ldap/settings", admin,
		ldapBody(ep, "corp", ep.TLSURL(ep.BaseDN), nil), http.StatusCreated)

	// The strategies' defaults, as 7.x's GroupMappingStrategyService gives
	// them.
	for strategy, want := range map[string]string{
		"STATIC":       `"groupKeyMember":"uniqueMember"`,
		"dynamic":      `"groupKeyMember":"memberOf"`,
		"HIERARCHICAL": `"groupKeyMember":"ou"`,
		"hierarchy":    `"filter":"(objectClass=organizationalUnit)"`,
		"other":        `"filter":"(objectClass=groupOfNames)"`,
	} {
		if body := ldapCall(t, h, http.MethodGet, ldapGroups+"/strategy?strategy="+strategy, admin, "",
			http.StatusOK); !strings.Contains(body, want) {
			t.Fatalf("strategy %s: %s", strategy, body)
		}
	}

	// CRUD, through the trailing-slash collection the v2 provider uses.
	ldapCall(t, h, http.MethodPost, ldapGroups+"/", admin, staticGroups, http.StatusCreated)
	ldapCall(t, h, http.MethodPost, ldapGroups, admin, staticGroups, http.StatusBadRequest)
	if body := ldapCall(t, h, http.MethodGet, ldapGroups+"/corp-groups", admin, "", http.StatusOK); !strings.Contains(
		body, `"group_member_attribute":"uniqueMember"`) || !strings.Contains(body, `"strategy":"STATIC"`) {
		t.Fatalf("stored setting: %s", body)
	}
	ldapCall(t, h, http.MethodPut, ldapGroups+"/", admin, `{"name":"gone","enabled_ldap":"corp"}`, http.StatusNotFound)
	ldapCall(t, h, http.MethodPost, ldapGroups, admin, `{"name":"bad","enabled_ldap":"corp","strategy":"NESTED"}`,
		http.StatusBadRequest)
	ldapCall(t, h, http.MethodGet, ldapGroups+"/gone", admin, "", http.StatusNotFound)
	// Administrators' alone. Dave's login is remembered for a while
	// (DefaultLoginTTL), so it is one whose memberships do not matter.
	ldapCall(t, h, http.MethodGet, ldapGroups, basic("dave", accesstest.Dave.Password), "", http.StatusForbidden)

	// The search: every group, then a user's by each strategy.
	all := searchGroups(t, h, admin, "", staticGroups)
	if len(all) != 4 || all["Developers"].Status != "DOES_NOT_EXIST" ||
		all["Developers"].Describe != "the developers" {
		t.Fatalf("search %+v", all)
	}
	if bob := searchGroups(t, h, admin, "?username=bob", staticGroups); len(bob) != 2 ||
		bob["readers"].Name == "" || bob["Developers"].Name == "" {
		t.Fatalf("bob's static groups %+v", bob)
	}
	dynamic := strings.NewReplacer(`"uniqueMember"`, `"memberOf"`, `"STATIC"`, `"DYNAMIC"`).Replace(staticGroups)
	if bob := searchGroups(t, h, admin, "?username=bob", dynamic); len(bob) != 2 || bob["readers"].Name == "" {
		t.Fatalf("bob's dynamic groups %+v", bob)
	}
	hier := `{"name":"units","enabled_ldap":"corp","group_member_attribute":"ou","group_name_attribute":"ou",` +
		`"strategy":"HIERARCHICAL","filter":"(objectClass=organizationalUnit)"}`
	if dave := searchGroups(t, h, admin, "?username=dave", hier); len(dave) != 1 || dave["contractors"].Name == "" {
		t.Fatalf("dave's units %+v", dave)
	}
	if body := ldapCall(t, h, http.MethodPost, ldapGroups+"/search?username=nobody", admin, staticGroups,
		http.StatusBadRequest); !strings.Contains(body, "Could not find DN for user: 'nobody'") {
		t.Fatalf("unknown user: %s", body)
	}

	// An import creates developers in the ldap realm, lower-cased; the
	// search then finds it current.
	imp := `{"ldap_group_settings":` + staticGroups + `,"import_groups":[{"group_name":"Developers","group_dn":"` +
		all["Developers"].DN + `","description":"the developers","required_update":"DOES_NOT_EXIST"}]}`
	ldapCall(t, h, http.MethodPost, ldapGroups+"/import", admin, imp, http.StatusNoContent)
	ldapCall(t, h, http.MethodPost, ldapGroups+"/import", admin, imp, http.StatusBadRequest)
	if body := ldapCall(t, h, http.MethodGet, "/access/api/v2/groups/developers", admin, "", http.StatusOK); !strings.Contains(
		body, `"realm":"ldap"`) || !strings.Contains(body, "groupsStrategy=STATIC;groupDn=") {
		t.Fatalf("imported group: %s", body)
	}
	if found := searchGroups(t, h, admin, "", staticGroups); found["Developers"].Status != "IN_ARTIFACTORY" {
		t.Fatalf("after the import %+v", found)
	}

	// Alice's first login joins developers, which grants the artifact;
	// Dave, in no group, is refused it.
	seedGroupAccess(t, rt)
	ldapCall(t, h, http.MethodPut, "/artifactory/libs-local/team.txt", admin, "team only", http.StatusCreated)
	alice := basic("alice", accesstest.Alice.Password)
	ldapCall(t, h, http.MethodGet, "/artifactory/libs-local/team.txt", alice, "", http.StatusOK)
	ldapCall(t, h, http.MethodGet, "/artifactory/libs-local/team.txt", basic("dave", accesstest.Dave.Password), "",
		http.StatusForbidden)
	var issued struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(ldapCall(t, h, http.MethodPost, "/access/api/v1/tokens", alice, `{}`,
		http.StatusOK)), &issued); err != nil || issued.AccessToken == "" {
		t.Fatalf("token: %v %+v", err, issued)
	}
	token := map[string]string{"Authorization": "Bearer " + issued.AccessToken}
	ldapCall(t, h, http.MethodGet, "/artifactory/libs-local/team.txt", token, "", http.StatusOK)

	// Alice leaves developers in the directory: her refresh revokes the
	// membership, and with it the token's authority, at once.
	accesstest.SetGroupMembers(t, ep, accesstest.Developers, accesstest.Bob)
	ldapCall(t, h, http.MethodPost, ldapGroups+"/corp-groups/refresh?operation=UPDATE&username=alice", admin, "",
		http.StatusOK)
	ldapCall(t, h, http.MethodGet, "/artifactory/libs-local/team.txt", token, "", http.StatusForbidden)
	ldapCall(t, h, http.MethodGet, "/artifactory/libs-local/team.txt", alice, "", http.StatusForbidden)
	// Create a Group imports one directory group by name, which the
	// directory compares without case, under its lower-case name; sent
	// again through Replace or Create a Group it replaces the group, and it
	// never takes over the internal readers group.
	v2Groups := "/access/api/v2/groups"
	byName := `{"import_from_ldap_group_setting":"corp-groups"}`
	if body := ldapCall(t, h, http.MethodPost, v2Groups, admin,
		`{"name":"CYCLE-A","import_from_ldap_group_setting":"corp-groups"}`, http.StatusCreated); !strings.Contains(
		body, `"name":"cycle-a"`) || !strings.Contains(body, `"realm":"ldap"`) {
		t.Fatalf("Create a Group import: %s", body)
	}
	ldapCall(t, h, http.MethodPut, v2Groups+"/Cycle-A", admin, byName, http.StatusOK)
	ldapCall(t, h, http.MethodPut, v2Groups+"/readers", admin, byName, http.StatusBadRequest)
	ldapCall(t, h, http.MethodPost, v2Groups, admin,
		`{"name":"nobody","import_from_ldap_group_setting":"corp-groups"}`, http.StatusNotFound)
	// Back in the directory, the full refresh restores it and imports the
	// group not yet present: cycle-b, not readers, whose name the default
	// internal group already has and keeps.
	accesstest.SetGroupMembers(t, ep, accesstest.Developers, accesstest.Alice, accesstest.Bob)
	if body := ldapCall(t, h, http.MethodPost, ldapGroups+"/corp-groups/refresh", admin, "", http.StatusOK); !strings.HasPrefix(
		body, "Imported 1 LDAP groups") {
		t.Fatalf("refresh: %s", body)
	}
	ldapCall(t, h, http.MethodGet, "/artifactory/libs-local/team.txt", token, "", http.StatusOK)
	if body := ldapCall(t, h, http.MethodGet, "/access/api/v2/groups/readers", admin, "", http.StatusOK); !strings.Contains(
		body, `"realm":"internal"`) {
		t.Fatalf("the refresh took over the internal readers group: %s", body)
	}
	ldapCall(t, h, http.MethodPost, ldapGroups+"/missing/refresh", admin, "", http.StatusNotFound)
	ldapCall(t, h, http.MethodPost, ldapGroups+"/corp-groups/refresh?operation=SOMETIMES", admin, "",
		http.StatusBadRequest)

	// The descriptor exports the setting in 7.x's element order and writes
	// it as the legacy provider does.
	if body := ldapCall(t, h, http.MethodGet, "/artifactory/api/system/configuration", admin, "", http.StatusOK); !strings.Contains(
		body, "<ldapGroupSettings><ldapGroupSetting><name>corp-groups</name><groupBaseDn>ou=groups</groupBaseDn>") {
		t.Fatalf("export: %s", body)
	}
	yaml := map[string]string{"Content-Type": "application/yaml"}
	for k, v := range admin {
		yaml[k] = v
	}
	ldapCall(t, h, http.MethodPatch, "/artifactory/api/system/configuration", yaml,
		"security:\n  ldapGroupSettings:\n    legacy:\n      name: legacy\n      enabledLdap: corp\n"+
			"      groupBaseDn: ou=groups\n      groupMemberAttribute: uniqueMember\n      strategy: STATIC\n", http.StatusOK)
	ldapCall(t, h, http.MethodGet, ldapGroups+"/legacy", admin, "", http.StatusOK)

	// A restart keeps the settings and the groups; Alice, out of the
	// directory group again, loses the artifact at her next login, which
	// the new process answers afresh.
	if err := rt.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	open = false
	accesstest.SetGroupMembers(t, ep, accesstest.Developers, accesstest.Bob)
	rt = harness.OpenApp(t, cfg, opts)
	open = true
	h = ldapRouter(rt)
	ldapCall(t, h, http.MethodGet, ldapGroups+"/corp-groups", admin, "", http.StatusOK)
	ldapCall(t, h, http.MethodGet, "/artifactory/libs-local/team.txt", token, "", http.StatusOK)
	ldapCall(t, h, http.MethodGet, "/artifactory/libs-local/team.txt", alice, "", http.StatusForbidden)
	ldapCall(t, h, http.MethodGet, "/artifactory/libs-local/team.txt", token, "", http.StatusForbidden)
	ldapCall(t, h, http.MethodGet, "/artifactory/libs-local/team.txt", basic("bob", accesstest.Bob.Password), "",
		http.StatusOK)

	// The legacy provider deletes by clearing the section; the groups
	// imported through the settings stay.
	ldapCall(t, h, http.MethodPatch, "/artifactory/api/system/configuration", yaml,
		"security:\n  ldapGroupSettings: ~\n", http.StatusOK)
	if body := ldapCall(t, h, http.MethodGet, ldapGroups, admin, "", http.StatusOK); strings.TrimSpace(body) != "[]" {
		t.Fatalf("after clearing the section: %s", body)
	}
	ldapCall(t, h, http.MethodDelete, ldapGroups+"/corp-groups", admin, "", http.StatusNotFound)
	var groups []struct {
		Name string `json:"group_name"`
	}
	if err := json.Unmarshal([]byte(ldapCall(t, h, http.MethodGet, "/access/api/v2/groups", admin, "",
		http.StatusOK)), &struct {
		Groups *[]struct {
			Name string `json:"group_name"`
		} `json:"groups"`
	}{&groups}); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, g := range groups {
		got = append(got, g.Name)
	}
	if !slices.Contains(got, "developers") || !slices.Contains(got, "cycle-a") {
		t.Fatalf("groups after the settings went: %v", got)
	}
}
