package accesshttp

import (
	"context"
	"errors"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/access/core/authz"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func projectAccessWorld(t *testing.T) (AccessService, *inmem.Store) {
	t.Helper()
	db := inmem.New()
	for _, key := range []string{"alpha", "beta"} {
		if err := db.Projects().Create(t.Context(), model.Project{Key: key, DisplayName: key, AdminPrivileges: model.AdminPrivileges{ManageMembers: true}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Users().Create(t.Context(), model.User{Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Groups().Create(t.Context(), model.Group{Name: "project-admins"}); err != nil {
		t.Fatal(err)
	}
	api := accessapi.API{
		Authenticator: &accessapitest.Authenticator{},
		Authorizer: &accessapitest.Authorizer{DecideFunc: func(ctx context.Context, subject accessapi.Subject, _ bool) (accessapi.Decider, error) {
			return authz.New(ctx, authz.Sources{Permissions: db.Permissions(), Groups: db.Groups()}, subject)
		}},
	}
	return AccessService{accessState{deps: Deps{Store: db, API: api}}}, db
}

func TestUnitProjectManagementUsesCurrentRoleAndDelegatedPrivilege(t *testing.T) {
	for _, tc := range []struct {
		name       string
		role       string
		group      bool
		noMembers  bool
		platform   bool
		anonymous  bool
		scope      string
		need       projectPrivilege
		wantStatus int
	}{
		{name: "platform", platform: true, need: projectPlatform},
		{name: "ordinary member", role: "Viewer", need: projectAdministrator, wantStatus: 403},
		{name: "direct administrator", role: "Project Admin", need: projectAdministrator},
		{name: "group administrator", role: "Project Admin", group: true, need: projectAdministrator},
		{name: "member management", role: "Project Admin", need: projectManageMembers},
		{name: "delegation revoked", role: "Project Admin", noMembers: true, need: projectManageMembers, wantStatus: 403},
		{name: "project read survives members denial", role: "Project Admin", noMembers: true, need: projectAdministrator},
		{name: "project admin cannot create projects", role: "Project Admin", need: projectPlatform, wantStatus: 403},
		{name: "anonymous", anonymous: true, need: projectAdministrator, wantStatus: 401},
		{name: "roles read scope", scope: "project:alpha/roles:r", need: projectListRoles},
		{name: "roles scope cannot read project", scope: "project:alpha/roles:r", need: projectAdministrator, wantStatus: 403},
		{name: "roles scope cannot manage", scope: "project:alpha/roles:r", need: projectManageMembers, wantStatus: 403},
		{name: "foreign read scope", scope: "project:beta/roles:r", need: projectListRoles, wantStatus: 403},
		{name: "users read wildcard", scope: "project:alpha/members/**:r", need: projectListUsers},
		{name: "groups read wildcard", scope: "project:alpha/members/**:r", need: projectListGroups},
		{name: "members wildcard cannot read roles", scope: "project:alpha/members/**:r", need: projectListRoles, wantStatus: 403},
		{name: "resource ceiling", role: "Project Admin", group: true, scope: "artifact:*:r", need: projectAdministrator, wantStatus: 403},
		{name: "applied user", role: "Project Admin", scope: "applied-permissions/user", need: projectAdministrator},
		{name: "applied group", role: "Project Admin", group: true, scope: "applied-permissions/groups:project-admins", need: projectAdministrator},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db := projectAccessWorld(t)
			p := kernel.Principal{Name: "alice", Admin: tc.platform, Anonymous: tc.anonymous}
			if tc.group {
				p.Groups = []string{"project-admins"}
			}
			if tc.scope != "" {
				var err error
				p.Scope, err = kernel.ParseScope(tc.scope)
				if err != nil {
					t.Fatal(err)
				}
				p.IsTokenAuthenticated = true
			}
			if tc.role != "" {
				member := model.ProjectMember{ProjectKey: "alpha", Type: model.PrincipalUser, Name: "alice", Roles: []string{tc.role}}
				if tc.group {
					member.Type, member.Name = model.PrincipalGroup, "project-admins"
				}
				if err := db.Projects().PutMember(t.Context(), member); err != nil {
					t.Fatal(err)
				}
			}
			if tc.noMembers {
				current, err := db.Projects().Get(t.Context(), "alpha")
				if err != nil {
					t.Fatal(err)
				}
				current.AdminPrivileges.ManageMembers = false
				if err := db.Projects().Update(t.Context(), current); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			err := s.projectAccess(kernel.WithPrincipal(t.Context(), p), "alpha", tc.need,
				func(context.Context, project.Service, projectCaller) error { called = true; return nil })
			if errStatus(err) != tc.wantStatus || called != (tc.wantStatus == 0) {
				t.Fatalf("status=%d err=%v called=%t", errStatus(err), err, called)
			}
		})
	}
}

func TestUnitProjectManagementRechecksCredentialAndRollsBackEffects(t *testing.T) {
	s, db := projectAccessWorld(t)
	ctx := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "alice", Admin: true})
	s.deps.API.Authenticator = &accessapitest.Authenticator{CurrentFunc: func(context.Context, kernel.Principal) (kernel.Principal, error) {
		return kernel.Principal{}, kernel.ErrBadCredentials
	}}
	err := s.projectAccess(ctx, "alpha", projectPlatform, func(context.Context, project.Service, projectCaller) error {
		t.Fatal("revoked credential reached mutation")
		return nil
	})
	if !errors.Is(err, kernel.ErrBadCredentials) {
		t.Fatalf("current credential check: %v", err)
	}
	s.deps.API.Authenticator = &accessapitest.Authenticator{}
	err = s.projectAccess(ctx, "alpha", projectPlatform, func(ctx context.Context, svc project.Service, _ projectCaller) error {
		p, err := svc.Project(ctx, "alpha")
		if err != nil {
			return err
		}
		p.Description = "must roll back"
		if err := svc.UpdateProject(ctx, "alpha", p); err != nil {
			return err
		}
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("operation failure: %v", err)
	}
	p, err := db.Projects().Get(t.Context(), "alpha")
	if err != nil || p.Description != "" {
		t.Fatalf("effect escaped failed operation: %+v, %v", p, err)
	}
}
