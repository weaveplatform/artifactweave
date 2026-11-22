package accesshttp

import (
	"context"
	"errors"
	"net/http"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	accessgen "github.com/weaveplatform/artifactweave/internal/platform/gen/access"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

var errProjectHandlerStore = errors.New("injected project handler storage failure")

// handlerFault fails the (skip+1)th call of one project store method. The
// first Get is the administration transaction's own project read, so a
// handler's later read of the same project is skip 1.
type handlerFault struct {
	method string
	skip   int
	calls  int
}

func (f *handlerFault) fail(method string) error {
	if f.method != method {
		return nil
	}
	f.calls++
	if f.calls == f.skip+1 {
		return errProjectHandlerStore
	}
	return nil
}

type handlerFaultStore struct {
	Store
	f *handlerFault
}

func (s handlerFaultStore) Projects() port.ProjectStore {
	return handlerFaultProjects{ProjectStore: s.Store.Projects(), f: s.f}
}

type handlerFaultProjects struct {
	port.ProjectStore
	f *handlerFault
}

func (p handlerFaultProjects) Guard(ctx context.Context) error {
	if err := p.f.fail("Guard"); err != nil {
		return err
	}
	return p.ProjectStore.Guard(ctx) //nolint:wrapcheck // transparent test double
}

func (p handlerFaultProjects) Get(ctx context.Context, key string) (model.Project, error) {
	if err := p.f.fail("Get"); err != nil {
		return model.Project{}, err
	}
	return p.ProjectStore.Get(ctx, key) //nolint:wrapcheck // transparent test double
}

func (p handlerFaultProjects) List(ctx context.Context) ([]model.Project, error) {
	if err := p.f.fail("List"); err != nil {
		return nil, err
	}
	return p.ProjectStore.List(ctx) //nolint:wrapcheck // transparent test double
}

func (p handlerFaultProjects) Environments(ctx context.Context, key string) ([]model.Environment, error) {
	if err := p.f.fail("Environments"); err != nil {
		return nil, err
	}
	return p.ProjectStore.Environments(ctx, key) //nolint:wrapcheck // transparent test double
}

func (p handlerFaultProjects) Roles(ctx context.Context, key string) ([]model.Role, error) {
	if err := p.f.fail("Roles"); err != nil {
		return nil, err
	}
	return p.ProjectStore.Roles(ctx, key) //nolint:wrapcheck // transparent test double
}

func (p handlerFaultProjects) Role(ctx context.Context, key, name string) (model.Role, error) {
	if err := p.f.fail("Role"); err != nil {
		return model.Role{}, err
	}
	return p.ProjectStore.Role(ctx, key, name) //nolint:wrapcheck // transparent test double
}

func (p handlerFaultProjects) Members(ctx context.Context, key string, typ model.PrincipalType) ([]model.ProjectMember, error) {
	if err := p.f.fail("Members"); err != nil {
		return nil, err
	}
	return p.ProjectStore.Members(ctx, key, typ) //nolint:wrapcheck // transparent test double
}

func (p handlerFaultProjects) PutMember(ctx context.Context, m model.ProjectMember) error {
	if err := p.f.fail("PutMember"); err != nil {
		return err
	}
	return p.ProjectStore.PutMember(ctx, m) //nolint:wrapcheck // transparent test double
}

// Storage failures behind a project handler are internal errors that name
// nothing stored; a missing request body is the caller's 400.
func TestUnitProjectHandlersMapStorageFailuresAndMissingBodies(t *testing.T) {
	roles := []string{"Viewer"}
	for _, tc := range []struct {
		name   string
		method string
		skip   int
		status int
		call   func(context.Context, AccessService) error
	}{
		{"list projects", "List", 0, http.StatusInternalServerError, func(ctx context.Context, s AccessService) error {
			_, err := s.GetProjectsList(ctx, accessgen.GetProjectsListRequestObject{})
			return err
		}},
		{"read project", "Get", 1, http.StatusInternalServerError, func(ctx context.Context, s AccessService) error {
			_, err := s.GetProject(ctx, accessgen.GetProjectRequestObject{ProjectKey: "alpha"})
			return err
		}},
		{"update project read", "Get", 1, http.StatusInternalServerError, func(ctx context.Context, s AccessService) error {
			_, err := s.UpdateProject(ctx, accessgen.UpdateProjectRequestObject{ProjectKey: "alpha", Body: &accessgen.UpdateProjectJSONRequestBody{}})
			return err
		}},
		{"list environments", "Environments", 0, http.StatusInternalServerError, func(ctx context.Context, s AccessService) error {
			_, err := s.GetProjectEnvironments(ctx, accessgen.GetProjectEnvironmentsRequestObject{ProjectKey: "alpha"})
			return err
		}},
		{"rename environment", "Environments", 0, http.StatusInternalServerError, func(ctx context.Context, s AccessService) error {
			_, err := s.RenameProjectEnvironment(ctx, accessgen.RenameProjectEnvironmentRequestObject{
				ProjectKey: "alpha", EnvironmentName: "alpha-stage", Body: &accessgen.RenameProjectEnvironmentJSONRequestBody{NewName: "alpha-renamed"},
			})
			return err
		}},
		{"list roles", "Roles", 0, http.StatusInternalServerError, func(ctx context.Context, s AccessService) error {
			_, err := s.GetProjectRoles(ctx, accessgen.GetProjectRolesRequestObject{ProjectKey: "alpha"})
			return err
		}},
		{"read role", "Role", 0, http.StatusInternalServerError, func(ctx context.Context, s AccessService) error {
			_, err := s.GetProjectRole(ctx, accessgen.GetProjectRoleRequestObject{ProjectKey: "alpha", Role: "reader"})
			return err
		}},
		{"list users", "Members", 0, http.StatusInternalServerError, func(ctx context.Context, s AccessService) error {
			_, err := s.GetProjectUsers(ctx, accessgen.GetProjectUsersRequestObject{ProjectKey: "alpha"})
			return err
		}},
		{"list groups", "Members", 0, http.StatusInternalServerError, func(ctx context.Context, s AccessService) error {
			_, err := s.GetProjectGroups(ctx, accessgen.GetProjectGroupsRequestObject{ProjectKey: "alpha"})
			return err
		}},
		{"update group", "PutMember", 0, http.StatusInternalServerError, func(ctx context.Context, s AccessService) error {
			_, err := s.UpdateProjectGroup(ctx, accessgen.UpdateProjectGroupRequestObject{
				ProjectKey: "alpha", Group: "project-admins", Body: &accessgen.UpdateProjectGroupJSONRequestBody{Roles: &roles},
			})
			return err
		}},
		{"guard platform administration", "Guard", 0, http.StatusInternalServerError, func(ctx context.Context, s AccessService) error {
			_, err := s.GetProjectsList(ctx, accessgen.GetProjectsListRequestObject{})
			return err
		}},
		{"create project body", "", 0, http.StatusBadRequest, func(ctx context.Context, s AccessService) error {
			_, err := s.CreateProject(ctx, accessgen.CreateProjectRequestObject{})
			return err
		}},
		{"update project body", "", 0, http.StatusBadRequest, func(ctx context.Context, s AccessService) error {
			_, err := s.UpdateProject(ctx, accessgen.UpdateProjectRequestObject{ProjectKey: "alpha"})
			return err
		}},
		{"create environment body", "", 0, http.StatusBadRequest, func(ctx context.Context, s AccessService) error {
			_, err := s.CreateProjectEnvironment(ctx, accessgen.CreateProjectEnvironmentRequestObject{ProjectKey: "alpha"})
			return err
		}},
		{"rename environment body", "", 0, http.StatusBadRequest, func(ctx context.Context, s AccessService) error {
			_, err := s.RenameProjectEnvironment(ctx, accessgen.RenameProjectEnvironmentRequestObject{ProjectKey: "alpha", EnvironmentName: "alpha-stage"})
			return err
		}},
		{"create role body", "", 0, http.StatusBadRequest, func(ctx context.Context, s AccessService) error {
			_, err := s.CreateProjectRole(ctx, accessgen.CreateProjectRoleRequestObject{ProjectKey: "alpha"})
			return err
		}},
		{"update role body", "", 0, http.StatusBadRequest, func(ctx context.Context, s AccessService) error {
			_, err := s.UpdateProjectRole(ctx, accessgen.UpdateProjectRoleRequestObject{ProjectKey: "alpha", Role: "reader"})
			return err
		}},
		{"add user body", "", 0, http.StatusBadRequest, func(ctx context.Context, s AccessService) error {
			_, err := s.AddOrUpdateProjectUser(ctx, accessgen.AddOrUpdateProjectUserRequestObject{ProjectKey: "alpha", User: "alice"})
			return err
		}},
		{"update group body", "", 0, http.StatusBadRequest, func(ctx context.Context, s AccessService) error {
			_, err := s.UpdateProjectGroup(ctx, accessgen.UpdateProjectGroupRequestObject{ProjectKey: "alpha", Group: "project-admins"})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db := projectAccessWorld(t)
			ctx := t.Context()
			if err := db.Projects().CreateEnvironment(ctx, model.Environment{ProjectKey: "alpha", Name: "alpha-stage"}); err != nil {
				t.Fatal(err)
			}
			f := &handlerFault{method: tc.method, skip: tc.skip}
			s.deps.Store = handlerFaultStore{Store: db, f: f}
			s.deps.Repositories = support.Repositories(db)
			admin := kernel.WithPrincipal(ctx, kernel.Principal{Name: "admin", Admin: true})
			err := tc.call(admin, s)
			if got := errStatus(err); got != tc.status || tc.method != "" && f.calls <= tc.skip {
				t.Fatalf("status %d (reached=%v), want %d: %v", got, f.calls > tc.skip, tc.status, err)
			}
			if tc.status == http.StatusInternalServerError && errors.Is(err, errProjectHandlerStore) {
				t.Fatal("an internal error exposed its storage cause to the response")
			}
		})
	}
}

// The administration transaction refuses before running the operation when
// its store, authentication or authorization composition cannot decide.
func TestUnitProjectAdministrationRefusesIncompleteAuthority(t *testing.T) {
	boom := errors.New("authority unavailable")
	for _, tc := range []struct {
		name   string
		change func(*AccessService)
		want   error
	}{
		{"no store", func(s *AccessService) { s.deps.Store = nil }, nil},
		{"no authenticator", func(s *AccessService) { s.deps.API.Authenticator = nil }, nil},
		{"authorizer failure", func(s *AccessService) {
			s.deps.API.Authorizer = &accessapitest.Authorizer{DecideFunc: func(context.Context, accessapi.Subject, bool) (accessapi.Decider, error) {
				return nil, boom
			}}
		}, boom},
		{"no decision", func(s *AccessService) {
			s.deps.API.Authorizer = &accessapitest.Authorizer{DecideFunc: func(context.Context, accessapi.Subject, bool) (accessapi.Decider, error) {
				return nil, nil //nolint:nilnil // an authorizer that cannot decide
			}}
		}, kernel.ErrForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := projectAccessWorld(t)
			tc.change(&s)
			called := false
			err := s.projectAccess(kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "admin", Admin: true}), "alpha", projectAdministrator,
				func(context.Context, project.Service, projectCaller) error {
					called = true
					return nil
				})
			if err == nil || called || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("incomplete authority ran the operation: called=%v err=%v", called, err)
			}
		})
	}
}
