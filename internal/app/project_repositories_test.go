package app

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

func projectRepositoryMatrix() []matrixRow {
	const base = "/access/api/v1/projects/_"
	const attach = base + "/attach/repositories/{repoName}"
	const share = base + "/share/repositories/{repoName}"
	seed := func(t *testing.T, f *matrixFixture, principal string) {
		t.Helper()
		if err := f.db.Projects().Create(t.Context(), accesstest.Project{Key: "target-" + principal, DisplayName: "Target " + principal}); err != nil {
			t.Fatal(err)
		}
		if err := f.db.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{Key: "binding-" + principal, Class: repo.Local, PackageType: repo.Generic})); err != nil {
			t.Fatal(err)
		}
	}
	return []matrixRow{
		row("GET "+base+"/repositories/{repoName}", base+"/repositories/binding-{p}", "", administrators, 200).seeding(seed),
		row("PUT "+attach+"/{targetProjectKey}", base+"/attach/repositories/binding-{p}/target-{p}", "", administrators, 204).seeding(seed),
		row("DELETE "+attach, base+"/attach/repositories/binding-{p}", "", administrators, 204).seeding(seed),
		row("PUT "+share, base+"/share/repositories/binding-{p}?readOnly=true", "", administrators, 204).seeding(seed),
		row("DELETE "+share, base+"/share/repositories/binding-{p}", "", administrators, 204).seeding(seed),
		row("PUT "+share+"/{targetProjectKey}", base+"/share/repositories/binding-{p}/target-{p}?readOnly=true", "", administrators, 204).seeding(seed),
		row("DELETE "+share+"/{targetProjectKey}", base+"/share/repositories/binding-{p}/target-{p}", "", administrators, 204).seeding(seed),
	}
}

func projectRepositoryStatusRows() []statusCase {
	const base = "/access/api/v1/projects/_"
	const target = "/access/api/v1/projects"
	const attach = base + "/attach/repositories/{repoName}"
	const share = base + "/share/repositories/{repoName}"
	return []statusCase{
		{"POST " + target, target, "admin", `{"project_key":"binding","display_name":"Binding status"}`, 201},
		{"GET " + base + "/repositories/{repoName}", base + "/repositories/libs-local", "admin", "", 200},
		{"GET " + base + "/repositories/{repoName}", base + "/repositories/absent-binding", "admin", "", 404},
		{"GET " + base + "/repositories/{repoName}", base + "/repositories/libs-local", "user", "", 403},
		{"GET " + base + "/repositories/{repoName}", base + "/repositories/libs-local", "", "", 401},
		{"PUT " + attach + "/{targetProjectKey}", base + "/attach/repositories/libs-local/binding", "admin", "", 204},
		{"PUT " + attach + "/{targetProjectKey}", base + "/attach/repositories/libs-local/proj", "admin", "", 400},
		{"PUT " + attach + "/{targetProjectKey}", base + "/attach/repositories/libs-local/missing-binding", "admin", "", 404},
		{"DELETE " + attach, base + "/attach/repositories/libs-local", "admin", "", 204},
		{"PUT " + share + "/{targetProjectKey}", base + "/share/repositories/libs-local/binding?readOnly=true", "admin", "", 204},
		{"PUT " + share, base + "/share/repositories/libs-local?readOnly=true", "admin", "", 204},
		{"DELETE " + share, base + "/share/repositories/libs-local", "admin", "", 204},
		{"DELETE " + share + "/{targetProjectKey}", base + "/share/repositories/libs-local/binding", "admin", "", 204},
	}
}
