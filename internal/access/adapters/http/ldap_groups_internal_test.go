package accesshttp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/ldap"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	accessgen "github.com/weaveplatform/artifactweave/internal/platform/gen/access"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// groupDirectory answers the group calls as a test sets them.
type groupDirectory struct {
	ldap.Directory
	users  map[string]string
	groups []ldap.DirectoryGroup
	err    error
}

func (d groupDirectory) Lookup(_ context.Context, _ ldap.Connection, name string) (ldap.Entry, error) {
	if dn, ok := d.users[name]; ok {
		return ldap.Entry{DN: dn}, nil
	}
	return ldap.Entry{}, ldap.ErrUnknownUser
}

func (d groupDirectory) Groups(context.Context, ldap.Connection, accessapi.LDAPGroupSetting) ([]ldap.DirectoryGroup, error) {
	return d.groups, d.err
}

func (d groupDirectory) GroupsNamed(
	_ context.Context, _ ldap.Connection, _ accessapi.LDAPGroupSetting, name string,
) ([]ldap.DirectoryGroup, error) {
	var out []ldap.DirectoryGroup
	for _, g := range d.groups {
		if strings.EqualFold(g.Name, name) {
			out = append(out, g)
		}
	}
	return out, d.err
}

func (d groupDirectory) UserGroups(
	context.Context, ldap.Connection, accessapi.LDAPGroupSetting, string, string,
) ([]ldap.DirectoryGroup, error) {
	return d.groups, d.err
}

func groupStatus(err error) int {
	var e *apierror.Error
	if !errors.As(err, &e) {
		return 0
	}
	return e.Status
}

// TestUnitLDAPGroupAnswers covers each group handler's answers and
// refusals over the in-memory store and a scripted directory; the router
// and a real directory are test/server/access's ldap_groups_test.go.
func TestUnitLDAPGroupAnswers(t *testing.T) {
	ctx := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "admin", Admin: true})
	db := inmem.New()
	dir := &groupDirectory{users: map[string]string{"alice": "uid=alice"}}
	s := AccessService{accessState{deps: Deps{Store: db, LDAP: dir}}}
	key, url, pattern := "corp", "ldap://ldap.example.com", "uid={0}"
	if _, err := s.CreateLdapSettings(ctx, accessgen.CreateLdapSettingsRequestObject{Body: &accessgen.LdapSettingsCreate{
		Key: key, LdapUrl: url, UserDnPattern: &pattern,
	}}); err != nil {
		t.Fatal(err)
	}
	str := func(v string) *string { return &v }
	create := func(body accessgen.LdapGroupSettingsCreate) error {
		_, err := s.CreateLdapGroupSettings(ctx, accessgen.CreateLdapGroupSettingsRequestObject{Body: &body})
		return err
	}
	if err := create(accessgen.LdapGroupSettingsCreate{Name: "devs", EnabledLdap: "corp", Strategy: str("")}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetLdapGroupSettingsByName(ctx, accessgen.GetLdapGroupSettingsByNameRequestObject{Name: "devs"})
	if g, ok := got.(accessgen.GetLdapGroupSettingsByName200JSONResponse); err != nil || !ok || *g.Strategy != "STATIC" {
		t.Fatalf("read %+v, %v", got, err)
	}
	for name, err := range map[string]error{
		"a second create": create(accessgen.LdapGroupSettingsCreate{Name: "devs", EnabledLdap: "corp"}),
		"an invalid create": create(accessgen.LdapGroupSettingsCreate{
			Name: "bad", EnabledLdap: "corp", Strategy: str("NESTED"),
		}),
		"no create body":  second(s.CreateLdapGroupSettings(ctx, accessgen.CreateLdapGroupSettingsRequestObject{})),
		"no replace body": second(s.UpdateLdapGroupSettings(ctx, accessgen.UpdateLdapGroupSettingsRequestObject{})),
		"no replace name": second(s.UpdateLdapGroupSettings(ctx, accessgen.UpdateLdapGroupSettingsRequestObject{
			Body: &accessgen.LdapGroupSettings{},
		})),
		"no search body": second(s.SearchLdapGroups(ctx, accessgen.SearchLdapGroupsRequestObject{})),
		"no import body": second(s.ImportLdapGroups(ctx, accessgen.ImportLdapGroupsRequestObject{})),
		"an invalid import": second(s.ImportLdapGroups(ctx, accessgen.ImportLdapGroupsRequestObject{
			Body: &accessgen.LdapImport{ImportGroups: &[]accessgen.LdapGroup{{GroupName: str("x")}}},
		})),
		"an invalid import setting": second(s.ImportLdapGroups(ctx, accessgen.ImportLdapGroupsRequestObject{
			Body: &accessgen.LdapImport{
				LdapGroupSettings: &accessgen.LdapGroupSettings{Strategy: str("NESTED")},
				ImportGroups:      &[]accessgen.LdapGroup{},
			},
		})),
		"a search through no connection": second(s.SearchLdapGroups(ctx, accessgen.SearchLdapGroupsRequestObject{
			Body: &accessgen.LdapGroupSettings{EnabledLdap: str("none")},
		})),
		"an unknown user": second(s.SearchLdapGroups(ctx, accessgen.SearchLdapGroupsRequestObject{
			Body: &accessgen.LdapGroupSettings{EnabledLdap: str("corp")}, Params: accessgen.SearchLdapGroupsParams{
				Username: str("nobody"),
			},
		})),
		"a bad operation": second(s.RefreshLdapGroupSettings(ctx, accessgen.RefreshLdapGroupSettingsRequestObject{
			Name: "devs", Params: accessgen.RefreshLdapGroupSettingsParams{
				Operation: ptrTo(accessgen.RefreshLdapGroupSettingsParamsOperation("SOMETIMES")),
			},
		})),
	} {
		if groupStatus(err) != http.StatusBadRequest {
			t.Errorf("%s: %v, want 400", name, err)
		}
	}
	for name, err := range map[string]error{
		"a read": second(s.GetLdapGroupSettingsByName(ctx, accessgen.GetLdapGroupSettingsByNameRequestObject{Name: "x"})),
		"a replace": second(s.UpdateLdapGroupSettings(ctx, accessgen.UpdateLdapGroupSettingsRequestObject{
			Body: &accessgen.LdapGroupSettings{Name: str("x")},
		})),
		"a delete":  second(s.DeleteLdapGroupSettings(ctx, accessgen.DeleteLdapGroupSettingsRequestObject{Name: "x"})),
		"a refresh": second(s.RefreshLdapGroupSettings(ctx, accessgen.RefreshLdapGroupSettingsRequestObject{Name: "x"})),
	} {
		if groupStatus(err) != http.StatusNotFound {
			t.Errorf("%s of a missing setting: %v, want 404", name, err)
		}
	}

	// The directory's answers: a search, an import of what it found, and a
	// refresh; then the directory's failure.
	dir.groups = []ldap.DirectoryGroup{{Name: "Devs", DN: "cn=devs,ou=groups"}}
	found, err := s.SearchLdapGroups(ctx, accessgen.SearchLdapGroupsRequestObject{
		Body: &accessgen.LdapGroupSettings{EnabledLdap: str("corp")},
	})
	list, ok := found.(accessgen.SearchLdapGroups200JSONResponse)
	if err != nil || !ok || len(list) != 1 || *list[0].RequiredUpdate != accessgen.DOESNOTEXIST {
		t.Fatalf("search %+v, %v", found, err)
	}
	if _, err := s.ImportLdapGroups(ctx, accessgen.ImportLdapGroupsRequestObject{Body: &accessgen.LdapImport{
		ImportGroups: &[]accessgen.LdapGroup{{
			GroupName: list[0].GroupName, GroupDn: list[0].GroupDn, Description: str("d"), RequiredUpdate: list[0].RequiredUpdate,
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	op := accessgen.RefreshLdapGroupSettingsParamsOperation("UPDATE")
	sum, err := s.RefreshLdapGroupSettings(ctx, accessgen.RefreshLdapGroupSettingsRequestObject{
		Name: "devs", Params: accessgen.RefreshLdapGroupSettingsParams{Operation: &op, Username: str("alice")},
	})
	if text, ok := sum.(accessgen.RefreshLdapGroupSettings200TextResponse); err != nil || !ok || text == "" {
		t.Fatalf("refresh %+v, %v", sum, err)
	}
	dir.err = &ldap.DirectoryError{Op: "search", Reason: "down"}
	if _, err := s.RefreshLdapGroupSettings(ctx, accessgen.RefreshLdapGroupSettingsRequestObject{Name: "devs"}); groupStatus(
		err) != http.StatusBadRequest {
		t.Fatalf("a failed directory: %v", err)
	}

	// The rest of the CRUD, and the strategy defaults.
	if _, err := s.UpdateLdapGroupSettings(ctx, accessgen.UpdateLdapGroupSettingsRequestObject{
		Body: &accessgen.LdapGroupSettings{Name: str("devs"), EnabledLdap: str("corp"), SubTree: ptrTo(true)},
	}); err != nil {
		t.Fatal(err)
	}
	all, err := s.GetAllLdapGroupSettings(ctx, accessgen.GetAllLdapGroupSettingsRequestObject{})
	if l, ok := all.(accessgen.GetAllLdapGroupSettings200JSONResponse); err != nil || !ok || len(l) != 1 || !*l[0].SubTree {
		t.Fatalf("list %+v, %v", all, err)
	}
	strategy, err := s.GetLdapGroupMappingStrategy(ctx, accessgen.GetLdapGroupMappingStrategyRequestObject{
		Params: accessgen.GetLdapGroupMappingStrategyParams{Strategy: str("dynamic")},
	})
	if d, ok := strategy.(accessgen.GetLdapGroupMappingStrategy200JSONResponse); err != nil || !ok || *d.GroupKeyMember != "memberOf" {
		t.Fatalf("strategy %+v, %v", strategy, err)
	}
	if _, err := s.DeleteLdapGroupSettings(ctx, accessgen.DeleteLdapGroupSettingsRequestObject{Name: "devs"}); err != nil {
		t.Fatal(err)
	}

	// Administrators alone; and no directory client answers 501.
	user := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "u"})
	if _, err := s.GetAllLdapGroupSettings(user, accessgen.GetAllLdapGroupSettingsRequestObject{}); !errors.Is(
		err, kernel.ErrForbidden) && groupStatus(err) != http.StatusForbidden {
		t.Fatalf("a non-administrator: %v", err)
	}
	if _, err := s.GetLdapGroupMappingStrategy(user, accessgen.GetLdapGroupMappingStrategyRequestObject{}); err == nil {
		t.Fatal("a non-administrator read the strategy defaults")
	}
	bare := AccessService{accessState{deps: Deps{Store: db}}}
	for name, err := range map[string]error{
		"search":  second(bare.SearchLdapGroups(ctx, accessgen.SearchLdapGroupsRequestObject{})),
		"refresh": second(bare.RefreshLdapGroupSettings(ctx, accessgen.RefreshLdapGroupSettingsRequestObject{Name: "x"})),
	} {
		if groupStatus(err) != http.StatusNotImplemented {
			t.Errorf("%s without a directory: %v", name, err)
		}
	}
	none := AccessService{accessState{deps: Deps{LDAP: dir}}}
	for name, err := range map[string]error{
		"search": second(none.SearchLdapGroups(ctx, accessgen.SearchLdapGroupsRequestObject{
			Body: &accessgen.LdapGroupSettings{},
		})),
		"import": second(none.ImportLdapGroups(ctx, accessgen.ImportLdapGroupsRequestObject{
			Body: &accessgen.LdapImport{ImportGroups: &[]accessgen.LdapGroup{}},
		})),
		"refresh": second(none.RefreshLdapGroupSettings(ctx, accessgen.RefreshLdapGroupSettingsRequestObject{Name: "x"})),
	} {
		if err == nil {
			t.Errorf("%s without a store answered", name)
		}
	}
}

// TestUnitLDAPGroupImportAnswers covers Create a Group's and Replace or
// Create a Group's import_from_ldap_group_setting: the group is stored
// under the directory's name in lower case in the ldap realm, with the
// directory's description unless the request sends one; a create over a
// taken name answers 409, a replacement of a group not imported from the
// same LDAP group 400, a missing setting or directory group 404, and a
// server without a directory client 501.
func TestUnitLDAPGroupImportAnswers(t *testing.T) {
	ctx := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "admin", Admin: true})
	db := inmem.New()
	dir := &groupDirectory{groups: []ldap.DirectoryGroup{
		{Name: "DevOps", DN: "cn=DevOps,ou=groups", Description: "the directory's"},
		{Name: "ops", DN: "cn=ops,ou=groups"},
	}}
	s := AccessService{accessState{deps: Deps{Store: db, LDAP: dir}}}
	key, url, pattern := "corp", "ldap://ldap.example.com", "uid={0}"
	if _, err := s.CreateLdapSettings(ctx, accessgen.CreateLdapSettingsRequestObject{Body: &accessgen.LdapSettingsCreate{
		Key: key, LdapUrl: url, UserDnPattern: &pattern,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateLdapGroupSettings(ctx, accessgen.CreateLdapGroupSettingsRequestObject{
		Body: &accessgen.LdapGroupSettingsCreate{Name: "teams", EnabledLdap: "corp"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Groups().Create(ctx, model.Group{Name: "ops"}); err != nil {
		t.Fatal(err)
	}
	teams := ptrTo("teams")
	create := func(name string, setting *string) (accessgen.CreateGroupResponseObject, error) {
		return s.CreateGroup(ctx, accessgen.CreateGroupRequestObject{Body: &accessgen.GroupCreateRequest{
			Name: name, ImportFromLdapGroupSetting: setting,
		}})
	}
	replace := func(svc AccessService, name string, b accessgen.GroupCreateRequest) error {
		_, err := svc.ReplaceGroup(ctx, accessgen.ReplaceGroupRequestObject{Name: accessgen.GroupName(name), Body: &b})
		return err
	}

	got, err := create("DEVOPS", teams)
	d, ok := got.(accessgen.CreateGroup201JSONResponse)
	if err != nil || !ok || d.Name != "devops" || d.Realm != model.RealmLDAP || *d.Description != "the directory's" ||
		*d.RealmAttributes != ldap.RealmAttributes("DevOps", accessapi.LDAPStatic, "cn=DevOps,ou=groups") {
		t.Fatalf("create %+v, %v", got, err)
	}
	replaced, err := s.ReplaceGroup(ctx, accessgen.ReplaceGroupRequestObject{
		Name: "DevOps", Body: &accessgen.GroupCreateRequest{ImportFromLdapGroupSetting: teams, Description: ptrTo("mine")},
	})
	if r, ok := replaced.(accessgen.ReplaceGroup200JSONResponse); err != nil || !ok || *r.Description != "mine" {
		t.Fatalf("replace %+v, %v", replaced, err)
	}
	if err := replace(s, "fresh", accessgen.GroupCreateRequest{ImportFromLdapGroupSetting: teams}); groupStatus(
		err) != http.StatusNotFound {
		t.Fatalf("a replace of a group the directory lacks: %v", err)
	}

	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"a second create":             {second(create("devops", teams)), http.StatusConflict},
		"an unknown setting":          {second(create("devops", ptrTo("absent"))), http.StatusNotFound},
		"a group the directory lacks": {second(create("qa", teams)), http.StatusNotFound},
		"a replace of a local group": {
			replace(s, "ops", accessgen.GroupCreateRequest{ImportFromLdapGroupSetting: teams}), http.StatusBadRequest,
		},
		"members too": {replace(s, "ops", accessgen.GroupCreateRequest{
			ImportFromLdapGroupSetting: teams, Members: &[]string{},
		}), http.StatusBadRequest},
		"no directory client": {replace(AccessService{accessState{deps: Deps{Store: db}}}, "ops",
			accessgen.GroupCreateRequest{ImportFromLdapGroupSetting: teams}), http.StatusNotImplemented},
	} {
		if groupStatus(tc.err) != tc.want {
			t.Errorf("%s: %v, want %d", name, tc.err, tc.want)
		}
	}
	dir.err = &ldap.DirectoryError{Op: "search", Reason: "down"}
	if _, err := create("devops", teams); groupStatus(err) != http.StatusBadRequest {
		t.Fatalf("a failed directory: %v", err)
	}
	if _, err := (AccessService{accessState{deps: Deps{LDAP: dir}}}).CreateGroup(ctx, accessgen.CreateGroupRequestObject{
		Body: &accessgen.GroupCreateRequest{Name: "x", ImportFromLdapGroupSetting: teams},
	}); err == nil {
		t.Fatal("an import without a store answered")
	}
	if g, err := db.Groups().Get(ctx, "ops"); err != nil || g.Realm == model.RealmLDAP || g.RealmAttributes != "" {
		t.Fatalf("a refused import changed the local group: %+v, %v", g, err)
	}
}

func second[T any](_ T, err error) error { return err }

func ptrTo[T any](v T) *T { return &v }
