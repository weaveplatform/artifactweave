package app

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
)

// projectMatrix covers every activated route against the common principal
// matrix. None of its non-platform principals is a project admin; own/foreign
// project admins and standalone scopes are covered in test/server/access.
func projectMatrix() []matrixRow {
	const base = "/access/api/v1/projects"
	const key = base + "/{projectKey}"
	const mine = base + "/p11-{p}"
	seed := func(t *testing.T, f *matrixFixture, principal string) {
		t.Helper()
		ctx := t.Context()
		project := "p11-" + principal
		if err := f.db.Projects().Create(ctx, accesstest.Project{Key: project, DisplayName: "P11 " + principal}); err != nil {
			t.Fatal(err)
		}
		if err := f.db.Projects().PutRole(ctx, accesstest.Role{ProjectKey: project, Name: "p11-reader", Type: "CUSTOM", Environments: []string{"DEV"}}); err != nil {
			t.Fatal(err)
		}
		if err := f.db.Projects().CreateEnvironment(ctx, accesstest.Environment{ProjectKey: project, Name: project + "-qa"}); err != nil {
			t.Fatal(err)
		}
		for _, member := range []accesstest.ProjectMember{
			{ProjectKey: project, Type: "user", Name: "admin", Roles: []string{"p11-reader"}},
			{ProjectKey: project, Type: "group", Name: "readers", Roles: []string{"p11-reader"}},
		} {
			if err := f.db.Projects().PutMember(ctx, member); err != nil {
				t.Fatal(err)
			}
		}
	}
	return []matrixRow{
		at("GET "+base, "", administrators, 200),
		at("POST "+base, `{"project_key":"new-{p}","display_name":"New {p}"}`, administrators, 201),
		row("GET "+key, mine, "", administrators, 200).seeding(seed),
		row("PUT "+key, mine, `{"description":"changed"}`, administrators, 200).seeding(seed),
		row("DELETE "+key, mine, "", administrators, 204).seeding(seed),
		row("GET "+key+"/roles", mine+"/roles", "", administrators, 200).seeding(seed),
		row("POST "+key+"/roles", mine+"/roles", `{"name":"created","type":"CUSTOM","environments":["DEV"]}`, administrators, 201).seeding(seed),
		row("GET "+key+"/roles/{role}", mine+"/roles/p11-reader", "", administrators, 200).seeding(seed),
		row("PUT "+key+"/roles/{role}", mine+"/roles/p11-reader", `{"type":"CUSTOM","environments":["DEV"]}`, administrators, 200).seeding(seed),
		row("DELETE "+key+"/roles/{role}", mine+"/roles/p11-reader", "", administrators, 204).seeding(seed),
		row("GET "+key+"/users", mine+"/users", "", administrators, 200).seeding(seed),
		row("GET "+key+"/users/{user}", mine+"/users/admin", "", administrators, 200).seeding(seed),
		row("PUT "+key+"/users/{user}", mine+"/users/admin", `{"roles":["Viewer"]}`, administrators, 200).seeding(seed),
		row("DELETE "+key+"/users/{user}", mine+"/users/admin", "", administrators, 204).seeding(seed),
		row("GET "+key+"/groups", mine+"/groups", "", administrators, 200).seeding(seed),
		row("GET "+key+"/groups/{group}", mine+"/groups/readers", "", administrators, 200).seeding(seed),
		row("PUT "+key+"/groups/{group}", mine+"/groups/readers", `{"roles":["Viewer"]}`, administrators, 200).seeding(seed),
		row("DELETE "+key+"/groups/{group}", mine+"/groups/readers", "", administrators, 204).seeding(seed),
		row("GET "+key+"/environments", mine+"/environments", "", administrators, 200).seeding(seed),
		row("POST "+key+"/environments", mine+"/environments", `{"name":"p11-{p}-stage"}`, administrators, 201).seeding(seed),
		row("POST "+key+"/environments/{environmentName}/rename", mine+"/environments/p11-{p}-qa/rename", `{"new_name":"p11-{p}-stage"}`, administrators, 200).seeding(seed),
		row("DELETE "+key+"/environments/{environmentName}", mine+"/environments/p11-{p}-qa", "", administrators, 204).seeding(seed),
		row("GET /access/api/v2/projects", "/access/api/v2/projects?role_name=Viewer", "", administrators, 200),
	}
}
