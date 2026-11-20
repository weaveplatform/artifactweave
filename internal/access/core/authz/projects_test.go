package authz

import (
	"context"
	"errors"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

type projectReader func(context.Context, string, kernel.Page) ([]artifactsapi.ProjectRepository, string, error)

func (r projectReader) ProjectRepositories(ctx context.Context, key string, page kernel.Page) ([]artifactsapi.ProjectRepository, string, error) {
	return r(ctx, key, page)
}

func projectWorld(t *testing.T) (*inmem.Store, Sources) {
	t.Helper()
	db, ctx := inmem.New(), t.Context()
	for _, key := range []string{"alpha", "beta"} {
		if err := db.Projects().Create(ctx, model.Project{Key: key, AdminPrivileges: model.AdminPrivileges{
			ManageResources: true, ManageRemoteRepository: true,
			CreateLocalRepository: false, DeleteLocalRepository: true,
			CreateRemoteRepository: true, DeleteRemoteRepository: false,
			CreateVirtualRepository: true, DeleteVirtualRepository: true,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Groups().Create(ctx, model.Group{Name: "team"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Users().Create(ctx, model.User{Name: "alice", Groups: []string{"team"}}); err != nil {
		t.Fatal(err)
	}
	src := Sources{
		Permissions: db.Permissions(), Groups: db.Groups(), Projects: db.Projects(),
		Roles: project.Service{Store: db},
		Repositories: projectReader(func(_ context.Context, key string, page kernel.Page) ([]artifactsapi.ProjectRepository, string, error) {
			// Split discovery across pages so authorization cannot quietly
			// authorize only a project's first page of resources.
			if page.After == "second" {
				return []artifactsapi.ProjectRepository{{Key: key + "-prod", Class: repo.Remote, ProjectKey: key, Environments: []string{"PROD"}}}, "", nil
			}
			return []artifactsapi.ProjectRepository{{Key: key + "-dev", Class: repo.Local, ProjectKey: key}}, "second", nil
		}),
	}
	return db, src
}

func projectMember(t *testing.T, db *inmem.Store, typ model.PrincipalType, name string, roles ...string) {
	t.Helper()
	if err := db.Projects().PutMember(t.Context(), model.ProjectMember{
		ProjectKey: "alpha", Type: typ, Name: name, Roles: roles,
	}); err != nil {
		t.Fatal(err)
	}
}

func projectDecider(t *testing.T, src Sources, subject accessapi.Subject) *Decider {
	t.Helper()
	d, err := New(t.Context(), src, subject)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestUnitProjectRolesRespectOwnershipEnvironmentsAndCurrentDefinitions(t *testing.T) {
	for _, group := range []bool{false, true} {
		t.Run(map[bool]string{false: "user", true: "group"}[group], func(t *testing.T) {
			db, src := projectWorld(t)
			role := model.Role{
				ProjectKey: "alpha", Name: "reader", Type: model.RoleCustom,
				Environments: []string{"dev"}, Actions: []string{"READ_REPOSITORY", "READ_BUILD"},
			}
			if err := db.Projects().PutRole(t.Context(), role); err != nil {
				t.Fatal(err)
			}
			typ, name := model.PrincipalUser, "alice"
			subject := accessapi.Subject{Name: "alice"}
			if group {
				typ, name, subject.Groups = model.PrincipalGroup, "team", []string{"team"}
			}
			projectMember(t, db, typ, name, "reader")
			before := projectDecider(t, src, subject)
			for _, rt := range []accessapi.ResourceType{accessapi.ResourceArtifact, accessapi.ResourceBuild} {
				if !before.Allows(rt, accessapi.Repo{Key: "alpha-dev", Class: "local"}, "same/path", r) ||
					before.Allows(rt, accessapi.Repo{Key: "beta-dev", Class: "local"}, "same/path", r) ||
					before.Allows(rt, accessapi.Repo{Key: "alpha-prod", Class: "remote"}, "same/path", r) {
					t.Fatal("project role escaped its environment/ownership boundary")
				}
			}
			role.Environments = []string{"PROD"}
			if err := db.Projects().PutRole(t.Context(), role); err != nil {
				t.Fatal(err)
			}
			after := projectDecider(t, src, subject)
			if after.Allows(accessapi.ResourceArtifact, accessapi.Repo{Key: "alpha-dev"}, "same/path", r) ||
				!after.Allows(accessapi.ResourceArtifact, accessapi.Repo{Key: "alpha-prod-cache"}, "same/path", r) {
				t.Fatal("next request retained the previous role definition or missed a later resource page")
			}
			if !before.Allows(accessapi.ResourceArtifact, accessapi.Repo{Key: "alpha-dev"}, "same/path", r) {
				t.Fatal("immutable request snapshot changed after a role write")
			}
			if err := db.Projects().DeleteMember(t.Context(), "alpha", typ, name); err != nil {
				t.Fatal(err)
			}
			if projectDecider(t, src, subject).Allows(accessapi.ResourceArtifact, accessapi.Repo{Key: "alpha-prod"}, "same/path", r) {
				t.Fatal("removed membership still granted an action")
			}
		})
	}
}

func TestUnitNamedAndGlobalProjectRolesRemainInsideResourceCeilings(t *testing.T) {
	db, src := projectWorld(t)
	if err := db.Projects().PutRole(t.Context(), model.Role{
		Name: "global-custom", Type: model.RoleCustomGlobal,
		Environments: []string{"DEV", "PROD"}, Actions: []string{"READ_REPOSITORY", "DEPLOY_CACHE_REPOSITORY"},
	}); err != nil {
		t.Fatal(err)
	}
	subject := accessapi.Subject{Name: "synthetic", Scope: scope(t, "applied-permissions/roles:alpha:global-custom artifact:alpha-dev/allowed/**:r")}
	authority, err := db.Projects().Authority(t.Context(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	subject.ProjectIncarnations = map[string]string{"alpha": string(authority.Incarnation)}
	d := projectDecider(t, src, subject)
	dev := accessapi.Repo{Key: "alpha-dev", Class: "local"}
	if !d.Allows(accessapi.ResourceArtifact, dev, "allowed/file", r) ||
		d.Allows(accessapi.ResourceArtifact, dev, "other/file", r) ||
		d.Allows(accessapi.ResourceArtifact, dev, "allowed/file", w) ||
		d.Allows(accessapi.ResourceArtifact, accessapi.Repo{Key: "alpha-prod", Class: "remote"}, "allowed/file", r) ||
		d.Allows(accessapi.ResourceArtifact, accessapi.Repo{Key: "beta-dev", Class: "local"}, "allowed/file", r) {
		t.Fatal("named role bypassed the token's action/path/repository ceiling")
	}
	if !d.AllowsAll(accessapi.ResourceArtifact, dev, "allowed", r) || d.AllowsAll(accessapi.ResourceArtifact, dev, "", r) {
		t.Fatal("whole-folder decision did not retain the resource path ceiling")
	}
	administrator := subject
	administrator.Scope = scope(t, `applied-permissions/roles:alpha:"Project Admin" artifact:alpha-dev:r`)
	if projectDecider(t, src, administrator).ManagesRepository("alpha", "local", accessapi.RepositoryDelete) {
		t.Fatal("mixed role/resource token manufactured repository administration outside its read ceiling")
	}
	for _, target := range []accessapi.Repo{dev, {Key: "alpha-prod", Class: "remote"}, {Key: "beta-dev", Class: "local"}} {
		if d.VisibleRepos().Has(target) != d.IsRepoVisible(target) {
			t.Fatalf("SQL listing filter differs from visibility: %+v", target)
		}
	}
	if err := db.Projects().DeleteRole(t.Context(), "", "global-custom"); err != nil {
		t.Fatal(err)
	}
	if projectDecider(t, src, subject).Allows(accessapi.ResourceArtifact, dev, "allowed/file", r) {
		t.Fatal("deleted global role still granted a named token")
	}
}

func TestUnitProjectTokenBoundsAdministratorAndDefinitionScopeGrantsNoRead(t *testing.T) {
	db, src := projectWorld(t)
	authority, err := db.Projects().Authority(t.Context(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	subject := accessapi.Subject{
		Name: "admin", Admin: true, Scope: scope(t, "applied-permissions/user system:info:r"),
		ProjectKey: "alpha", ProjectIncarnations: map[string]string{"alpha": string(authority.Incarnation)},
	}
	d := projectDecider(t, src, subject)
	if d.IsAdmin() || d.System("info", kernel.ScopeRead) ||
		d.Allows(accessapi.ResourceArtifact, accessapi.Repo{Key: "beta-dev"}, "same/path", r) ||
		!d.Allows(accessapi.ResourceArtifact, accessapi.Repo{Key: "alpha-dev"}, "same/path", r) ||
		d.ManagesRepository("beta", "local", accessapi.RepositoryDelete) {
		t.Fatal("project-bound administrator escaped its project ceiling")
	}
	for _, key := range []string{"alpha-dev", "alpha-prod", "beta-dev", "other"} {
		target := accessapi.Repo{Key: key, Class: "local"}
		if d.VisibleRepos().Has(target) != d.IsRepoVisible(target) || d.IsRepoVisible(target) != (key == "alpha-dev" || key == "alpha-prod") {
			t.Fatalf("project listing escaped boundary: %s", key)
		}
	}
	subject.Scope = scope(t, "project:alpha/roles:r")
	definitions := projectDecider(t, src, subject)
	if definitions.Allows(accessapi.ResourceArtifact, accessapi.Repo{Key: "alpha-dev"}, "file", r) || definitions.IsAdmin() {
		t.Fatal("definition-read scope manufactured content Read")
	}
	if err := db.Projects().Delete(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	if err := db.Projects().Create(t.Context(), model.Project{Key: "alpha"}); err != nil {
		t.Fatal(err)
	}
	subject.Scope = scope(t, "applied-permissions/admin")
	if stale, err := New(t.Context(), src, subject); stale != nil || !errors.Is(err, kernel.ErrBadCredentials) {
		t.Fatalf("recreated project revived prior administrator or management scope: %+v %v", stale, err)
	}
}

func TestUnitGranularProjectAdministrationDoesNotGrantContent(t *testing.T) {
	db, src := projectWorld(t)
	if err := db.Projects().PutRole(t.Context(), model.Role{
		ProjectKey: "alpha", Name: "manager", Type: model.RoleCustom,
		Actions: []string{"MANAGE_RESOURCES"}, Environments: []string{"DEV"},
	}); err != nil {
		t.Fatal(err)
	}
	projectMember(t, db, model.PrincipalUser, "alice", "manager")
	d := projectDecider(t, src, accessapi.Subject{Name: "alice"})
	for _, tc := range []struct {
		class string
		op    accessapi.RepositoryOperation
		want  bool
	}{
		{"local", accessapi.RepositoryCreate, false},
		{"local", accessapi.RepositoryDelete, true},
		{"remote", accessapi.RepositoryCreate, true},
		{"remote", accessapi.RepositoryDelete, false},
		{"virtual", accessapi.RepositoryCreate, true},
		{"virtual", accessapi.RepositoryDelete, true},
		{"local", accessapi.RepositoryUpdate, true},
		{"remote", accessapi.RepositoryUpdate, true},
		{"virtual", accessapi.RepositoryUpdate, true},
		{"federated", accessapi.RepositoryCreate, false},
	} {
		if got := d.ManagesRepository("alpha", tc.class, tc.op); got != tc.want {
			t.Fatalf("%s/%d=%t, want %t", tc.class, tc.op, got, tc.want)
		}
	}
	if d.IsAdmin() || d.Allows(accessapi.ResourceArtifact, accessapi.Repo{Key: "alpha-dev"}, "file", r) || d.ManagesRepository("beta", "remote", accessapi.RepositoryCreate) {
		t.Fatal("management manufactured content or cross-project administration")
	}
	resource := projectDecider(t, src, accessapi.Subject{Name: "alice", Scope: scope(t, "artifact:*:r")})
	if resource.ManagesRepository("alpha", "local", accessapi.RepositoryDelete) {
		t.Fatal("resource-only token borrowed project management privileges")
	}
}

func TestUnitProjectContextFailureReturnsNoPartialDecider(t *testing.T) {
	db, src := projectWorld(t)
	projectMember(t, db, model.PrincipalUser, "alice", "Viewer")
	fault := errors.New("repository projection unavailable")
	src.Repositories = projectReader(func(context.Context, string, kernel.Page) ([]artifactsapi.ProjectRepository, string, error) {
		return []artifactsapi.ProjectRepository{{Key: "partial"}}, "", fault
	})
	if d, err := New(t.Context(), src, accessapi.Subject{Name: "alice"}); d != nil || !errors.Is(err, fault) {
		t.Fatalf("partial authorization escaped: %+v, %v", d, err)
	}
}

func TestUnitCurrentProjectAdministrationUsesSelectedRolesAndCeilings(t *testing.T) {
	db, src := projectWorld(t)
	projectMember(t, db, model.PrincipalUser, "alice", "Project Admin")
	p, err := db.Projects().Get(t.Context(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	p.AdminPrivileges.ManageMembers = true
	if err := db.Projects().Update(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	authority, err := db.Projects().Authority(t.Context(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, raw string
		want      bool
	}{
		{name: "password", want: true},
		{name: "user", raw: "applied-permissions/user", want: true},
		{name: "named administrator", raw: `applied-permissions/roles:alpha:"Project Admin"`, want: true},
		{name: "resource ceiling", raw: "applied-permissions/user artifact:*:r"},
		{name: "definitions", raw: "project:alpha/roles:r"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			subject := accessapi.Subject{Name: "alice"}
			if tc.raw != "" {
				subject.Scope = scope(t, tc.raw)
			}
			if tc.name == "named administrator" || tc.name == "definitions" {
				subject.ProjectIncarnations = map[string]string{"alpha": string(authority.Incarnation)}
			}
			d := projectDecider(t, src, subject)
			for _, op := range []accessapi.ProjectOperation{accessapi.ProjectManage, accessapi.ProjectManageMembers} {
				if d.ManagesProject("alpha", op) != tc.want || d.ManagesProject("beta", op) || d.ManagesProject("", op) {
					t.Fatal("selected role or management ceiling was not retained")
				}
			}
			if d.HasProjectAdministration() != tc.want {
				t.Fatal("administrative collection gate ignored selected roles or token ceiling")
			}
			if d.ManagesProject("alpha", 0) {
				t.Fatal("unknown operation permitted")
			}
		})
	}
}
