package accesshttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	accessgen "github.com/weaveplatform/artifactweave/internal/platform/gen/access"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

func TestUnitProjectRepositoryOperationsPublishAuthoredResponses(t *testing.T) {
	s, db := projectAccessWorld(t)
	s.deps.Repositories = support.Repositories(db)
	ctx := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "alice", Admin: true})
	key := "existing-key"
	if err := db.Repos().Create(ctx, *repo.Defaults(&repo.Config{Key: key, Class: repo.Local, PackageType: repo.Generic})); err != nil {
		t.Fatal(err)
	}
	attach := accessgen.AttachRepositoryToProjectRequestObject{RepoName: key, TargetProjectKey: "alpha"}
	r, err := s.AttachRepositoryToProject(ctx, attach)
	checkProjectResponse(t, err, r.VisitAttachRepositoryToProjectResponse, 204)
	c, err := db.Repos().Get(ctx, key)
	if err != nil || c.ProjectKey != "alpha" || len(c.Environments) != 1 || c.Environments[0] != "DEV" {
		t.Fatalf("204 attach did not publish configuration: %+v, %v", c, err)
	}
	attach.TargetProjectKey = "beta"
	if _, err := s.AttachRepositoryToProject(ctx, attach); errStatus(err) != 400 {
		t.Fatalf("moving an existing binding needs force: %v", err)
	}
	attach.Params.Force = httpkit.PtrTo(true)
	r, err = s.AttachRepositoryToProject(ctx, attach)
	checkProjectResponse(t, err, r.VisitAttachRepositoryToProjectResponse, 204)
	share, err := s.ShareRepositoryWithTargetProject(ctx, accessgen.ShareRepositoryWithTargetProjectRequestObject{
		RepoName: key, TargetProjectKey: "alpha", Params: accessgen.ShareRepositoryWithTargetProjectParams{ReadOnly: httpkit.PtrTo(true)},
	})
	checkProjectResponse(t, err, share.VisitShareRepositoryWithTargetProjectResponse, 204)
	all, err := s.ShareRepositoryWithAllProjects(ctx, accessgen.ShareRepositoryWithAllProjectsRequestObject{
		RepoName: key, Params: accessgen.ShareRepositoryWithAllProjectsParams{ReadOnly: httpkit.PtrTo(true)},
	})
	checkProjectResponse(t, err, all.VisitShareRepositoryWithAllProjectsResponse, 204)
	detach, err := s.UnassignProjectFromRepository(ctx, accessgen.UnassignProjectFromRepositoryRequestObject{RepoName: key})
	checkProjectResponse(t, err, detach.VisitUnassignProjectFromRepositoryResponse, 204)
	status, err := s.GetProjectRepositoryStatus(ctx, accessgen.GetProjectRepositoryStatusRequestObject{RepoName: key})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	if err := status.VisitGetProjectRepositoryStatusResponse(w); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || strings.Contains(w.Body.String(), "assigned_to") || !strings.Contains(w.Body.String(), `"shared_with_projects":["alpha"]`) ||
		!strings.Contains(w.Body.String(), `"shared_read_only":true`) {
		t.Fatalf("status after detach must retain shares: %d %s", w.Code, w.Body.String())
	}
	unshareAll, err := s.UnshareRepositoryWithAllProjects(ctx, accessgen.UnshareRepositoryWithAllProjectsRequestObject{RepoName: key})
	checkProjectResponse(t, err, unshareAll.VisitUnshareRepositoryWithAllProjectsResponse, 204)
	a, err := db.Projects().Assignment(ctx, key)
	if err != nil || a.SharedWithAll || len(a.SharedWith) != 1 {
		t.Fatalf("removing all-project share removed individual share: %+v, %v", a, err)
	}
	unshare, err := s.UnshareRepositoryWithTargetProject(ctx, accessgen.UnshareRepositoryWithTargetProjectRequestObject{RepoName: key, TargetProjectKey: "alpha"})
	checkProjectResponse(t, err, unshare.VisitUnshareRepositoryWithTargetProjectResponse, 204)
}

func checkProjectResponse(t *testing.T, err error, visit func(http.ResponseWriter) error, status int) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	if err := visit(w); err != nil {
		t.Fatal(err)
	}
	if w.Code != status {
		t.Fatalf("status=%d want=%d body=%s", w.Code, status, w.Body.String())
	}
}

type repositoryMetadataOnly struct {
	artifactsapi.Repositories
	t *testing.T
}

func (r repositoryMetadataOnly) Get(context.Context, string) (repo.Config, error) {
	r.t.Error("status/sharing must not open sealed repository configuration")
	return repo.Config{}, kernel.ErrStore
}

func TestUnitProjectRepositoryStatusAndSharesDoNotOpenSecrets(t *testing.T) {
	s, db := projectAccessWorld(t)
	s.deps.Repositories = repositoryMetadataOnly{Repositories: support.Repositories(db), t: t}
	ctx := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "alice", Admin: true})
	key := "secret-remote"
	if err := db.Repos().
		Create(ctx, *repo.Defaults(&repo.Config{Key: key, Class: repo.Remote, PackageType: repo.Generic, URL: "https://example.invalid", Password: "secret"})); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetProjectRepositoryStatus(ctx, accessgen.GetProjectRepositoryStatusRequestObject{RepoName: key}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ShareRepositoryWithAllProjects(ctx, accessgen.ShareRepositoryWithAllProjectsRequestObject{RepoName: key}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UnshareRepositoryWithAllProjects(ctx, accessgen.UnshareRepositoryWithAllProjectsRequestObject{RepoName: key}); err != nil {
		t.Fatal(err)
	}
}

func TestUnitProjectRepositoryAdministrationRequiresCurrentPlatformAuthority(t *testing.T) {
	s, db := projectAccessWorld(t)
	s.deps.Repositories = support.Repositories(db)
	if err := db.Projects().
		PutMember(t.Context(), model.ProjectMember{ProjectKey: "alpha", Type: model.PrincipalUser, Name: "alice", Roles: []string{"Project Admin"}}); err != nil {
		t.Fatal(err)
	}
	operations := []func(context.Context) error{
		func(ctx context.Context) error {
			_, err := s.GetProjectRepositoryStatus(ctx, accessgen.GetProjectRepositoryStatusRequestObject{RepoName: "missing"})
			return err
		},
		func(ctx context.Context) error {
			_, err := s.AttachRepositoryToProject(ctx, accessgen.AttachRepositoryToProjectRequestObject{RepoName: "missing", TargetProjectKey: "alpha"})
			return err
		},
		func(ctx context.Context) error {
			_, err := s.UnassignProjectFromRepository(ctx, accessgen.UnassignProjectFromRepositoryRequestObject{RepoName: "missing"})
			return err
		},
		func(ctx context.Context) error {
			_, err := s.ShareRepositoryWithTargetProject(ctx, accessgen.ShareRepositoryWithTargetProjectRequestObject{RepoName: "missing", TargetProjectKey: "alpha"})
			return err
		},
		func(ctx context.Context) error {
			_, err := s.UnshareRepositoryWithTargetProject(ctx, accessgen.UnshareRepositoryWithTargetProjectRequestObject{RepoName: "missing", TargetProjectKey: "alpha"})
			return err
		},
		func(ctx context.Context) error {
			_, err := s.ShareRepositoryWithAllProjects(ctx, accessgen.ShareRepositoryWithAllProjectsRequestObject{RepoName: "missing"})
			return err
		},
		func(ctx context.Context) error {
			_, err := s.UnshareRepositoryWithAllProjects(ctx, accessgen.UnshareRepositoryWithAllProjectsRequestObject{RepoName: "missing"})
			return err
		},
	}
	for _, principal := range []kernel.Principal{{Name: "alice"}, {Name: "anonymous", Anonymous: true}} {
		want := 403
		if principal.Anonymous {
			want = 401
		}
		for i, operation := range operations {
			if err := operation(kernel.WithPrincipal(t.Context(), principal)); errStatus(err) != want {
				t.Fatalf("operation %d read nonexistent repository before authority: %v", i, err)
			}
		}
	}
	// A project-bound administrative ceiling cannot become platform authority.
	p := kernel.Principal{Name: "alice", Admin: true, IsTokenAuthenticated: true, TokenProject: "alpha"}
	var err error
	p.Scope, err = kernel.ParseScope("applied-permissions/admin")
	if err != nil {
		t.Fatal(err)
	}
	authority, err := db.Projects().Authority(t.Context(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	p.ProjectIncarnations = map[string]string{"alpha": string(authority.Incarnation)}
	if _, err := s.GetProjectRepositoryStatus(kernel.WithPrincipal(t.Context(), p), accessgen.GetProjectRepositoryStatusRequestObject{RepoName: "missing"}); errStatus(err) != 403 {
		t.Fatalf("project-bound administrator became platform administrator: %v", err)
	}
}
