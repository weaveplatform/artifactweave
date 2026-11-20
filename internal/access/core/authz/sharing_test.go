package authz

import (
	"context"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

type sharedProjectReader struct {
	artifactsapi.ProjectRepositoryReader
	lookup func(context.Context, string) (artifactsapi.ProjectRepository, error)
}

func (r sharedProjectReader) ProjectRepository(ctx context.Context, key string) (artifactsapi.ProjectRepository, error) {
	return r.lookup(ctx, key)
}

func TestUnitStandardSharesUseConsumerRolesAndCurrentReadOnlyCeiling(t *testing.T) {
	db, src := projectWorld(t)
	ctx := t.Context()
	if err := db.Projects().
		PutRole(ctx, model.Role{ProjectKey: "alpha", Name: "Consumer", Type: model.RoleCustom, Actions: []string{"READ_REPOSITORY", "DEPLOY_CACHE_REPOSITORY", "DELETE_OVERWRITE_REPOSITORY"}, Environments: []string{"DEV"}}); err != nil {
		t.Fatal(err)
	}
	projectMember(t, db, model.PrincipalUser, "alice", "Consumer")
	if err := db.Repos().
		Create(ctx, *repo.Defaults(&repo.Config{Key: "shared-npm", Class: repo.Local, PackageType: repo.NPM, ProjectKey: "beta", Environments: []string{"DEV"}})); err != nil {
		t.Fatal(err)
	}
	src.Repositories = sharedProjectReader{ProjectRepositoryReader: src.Repositories, lookup: db.Repos().ProjectRepository}
	assignment := model.RepositoryAssignment{RepoKey: "shared-npm", ProjectKey: "beta", SharedWith: []string{"alpha"}, SharedReadOnly: true}
	if err := db.Projects().PutAssignment(ctx, assignment); err != nil {
		t.Fatal(err)
	}
	target := accessapi.Repo{Key: "shared-npm", Class: "local"}
	d := projectDecider(t, src, accessapi.Subject{Name: "alice"})
	if !d.Allows(accessapi.ResourceArtifact, target, "package.tgz", accessapi.ActionRead) || d.Allows(accessapi.ResourceArtifact, target, "package.tgz", accessapi.ActionWrite) {
		t.Fatal("read-only share did not bound the consumer's current role")
	}
	assignment.SharedReadOnly = false
	if err := db.Projects().PutAssignment(ctx, assignment); err != nil {
		t.Fatal(err)
	}
	d = projectDecider(t, src, accessapi.Subject{Name: "alice"})
	if !d.Allows(accessapi.ResourceArtifact, target, "package.tgz", accessapi.ActionWrite) || !d.AllowsAll(accessapi.ResourceArtifact, target, "", accessapi.ActionRead) {
		t.Fatal("writable share did not use the consumer's deployment role")
	}
	limited := projectDecider(t, src, accessapi.Subject{Name: "alice", Scope: scope(t, "applied-permissions/user artifact:shared-npm:r")})
	if limited.Allows(accessapi.ResourceArtifact, target, "package.tgz", accessapi.ActionWrite) {
		t.Fatal("consumer role bypassed resource token ceiling")
	}
	if err := db.Projects().DeleteMember(ctx, "alpha", model.PrincipalUser, "alice"); err != nil {
		t.Fatal(err)
	}
	if projectDecider(t, src, accessapi.Subject{Name: "alice"}).IsRepoVisible(target) {
		t.Fatal("revoked consumer membership retained share access")
	}
}

func TestUnitProjectBoundAdministratorCannotWidenReadOnlyShare(t *testing.T) {
	db, src := projectWorld(t)
	ctx := t.Context()
	projectMember(t, db, model.PrincipalUser, "alice", "Developer")
	if err := db.Repos().
		Create(ctx, *repo.Defaults(&repo.Config{Key: "shared-local", Class: repo.Local, PackageType: repo.Generic, ProjectKey: "beta", Environments: []string{"DEV"}})); err != nil {
		t.Fatal(err)
	}
	src.Repositories = sharedProjectReader{ProjectRepositoryReader: src.Repositories, lookup: db.Repos().ProjectRepository}
	if err := db.Projects().PutAssignment(ctx, model.RepositoryAssignment{RepoKey: "shared-local", ProjectKey: "beta", SharedWithAll: true, SharedReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	authority, err := db.Projects().Authority(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	d := projectDecider(
		t,
		src,
		accessapi.Subject{
			Name:                "alice",
			Admin:               true,
			ProjectKey:          "alpha",
			ProjectIncarnations: map[string]string{"alpha": string(authority.Incarnation)},
			Scope:               scope(t, "applied-permissions/admin"),
		},
	)
	target := accessapi.Repo{Key: "shared-local", Class: "local"}
	if !d.Allows(accessapi.ResourceArtifact, target, "item", accessapi.ActionRead) || d.Allows(accessapi.ResourceArtifact, target, "item", accessapi.ActionWrite) ||
		d.AllowsAll(accessapi.ResourceArtifact, target, "", accessapi.ActionWrite) {
		t.Fatal("project-bound administrator widened a consumer's read-only share")
	}
}
