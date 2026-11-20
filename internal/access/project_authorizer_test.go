package access_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

func TestUnitModuleProjectRolesUseCurrentMembershipAndCanonicalRepositories(t *testing.T) {
	w, ctx := newWorld(t), t.Context()
	must(t, w.st.Projects().Create(ctx, model.Project{Key: "alpha"}))
	must(t, w.st.Projects().Create(ctx, model.Project{Key: "beta"}))
	for _, c := range []repo.Config{
		{Key: "alpha-dev", Class: repo.Local, ProjectKey: "alpha"},
		{Key: "alpha-prod", Class: repo.Local, ProjectKey: "alpha", Environments: []string{"PROD"}},
		{Key: "beta-dev", Class: repo.Local, ProjectKey: "beta"},
	} {
		must(t, w.st.Repos().Create(ctx, *repo.Defaults(&c)))
	}
	must(t, w.st.Projects().PutRole(ctx, model.Role{
		Name: "reader", ProjectKey: "alpha", Type: model.RoleCustom,
		Actions: []string{"READ_REPOSITORY"}, Environments: []string{"DEV"},
	}))
	must(t, w.st.Projects().PutMember(ctx, model.ProjectMember{
		ProjectKey: "alpha", Type: model.PrincipalGroup, Name: "readers", Roles: []string{"reader"},
	}))
	p, err := w.api.Authenticator.Basic(ctx, "ann", "secret")
	must(t, err)
	m, err := access.New(access.Deps{Store: w.st, ProjectRepositories: support.ProjectRepositories(w.st)})
	must(t, err)
	authorizer := m.API().Authorizer
	for _, key := range []string{"alpha-dev", "alpha-prod", "beta-dev"} {
		d, err := authorizer.For(ctx, p)
		must(t, err)
		if d.Allows(accessapi.ResourceArtifact, accessapi.Repo{Key: key, Class: "local"}, "same/file", accessapi.ActionRead) != (key == "alpha-dev") {
			t.Fatalf("role escaped current canonical project/environment: %s", key)
		}
	}
	// Reuse the verified principal with old group names. A password hot-path
	// cache or an already-running request cannot preserve removed membership.
	u, err := w.st.Users().Get(ctx, "ann")
	must(t, err)
	u.Groups = nil
	must(t, w.st.Users().Update(ctx, u))
	fresh, d, err := authorizer.Authorize(ctx, p, false)
	must(t, err)
	if len(fresh.Groups) != 0 || d.Allows(accessapi.ResourceArtifact, accessapi.Repo{Key: "alpha-dev"}, "same/file", accessapi.ActionRead) {
		t.Fatal("new evaluation retained the principal's old group assignment")
	}
}
