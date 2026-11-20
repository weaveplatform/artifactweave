package project_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

var errStoreFailure = fmt.Errorf("injected project service storage failure: %w", kernel.ErrStore)

type failingStore struct {
	port.Store
	fail string
}

func (s failingStore) Projects() port.ProjectStore {
	return failingProjects{ProjectStore: s.Store.Projects(), fail: s.fail}
}

type failingProjects struct {
	port.ProjectStore
	fail string
}

func (s failingProjects) Guard(ctx context.Context) error {
	if s.fail == "binding guard" {
		return errStoreFailure
	}
	return s.ProjectStore.Guard(ctx)
}

func (s failingProjects) Assignment(ctx context.Context, key string) (model.RepositoryAssignment, error) {
	if s.fail == "binding assignment" {
		return model.RepositoryAssignment{}, errStoreFailure
	}
	return s.ProjectStore.Assignment(ctx, key)
}

func (s failingProjects) RoleProjects(ctx context.Context, q port.ProjectRoleQuery) ([]model.Project, bool, error) {
	if s.fail == "role project page" {
		return nil, false, errStoreFailure
	}
	return s.ProjectStore.RoleProjects(ctx, q)
}

func (s failingProjects) PutMember(ctx context.Context, m model.ProjectMember) error {
	if s.fail == "put member" {
		return errStoreFailure
	}
	return s.ProjectStore.PutMember(ctx, m)
}

func (s failingProjects) DeleteMember(ctx context.Context, key string, typ model.PrincipalType, name string) error {
	if s.fail == "delete group member" && typ == model.PrincipalGroup {
		return errStoreFailure
	}
	return s.ProjectStore.DeleteMember(ctx, key, typ, name)
}

func (s failingProjects) Members(ctx context.Context, key string, typ model.PrincipalType) ([]model.ProjectMember, error) {
	if s.fail == "read group members" && typ == model.PrincipalGroup {
		return nil, errStoreFailure
	}
	return s.ProjectStore.Members(ctx, key, typ)
}

func (s failingProjects) Get(ctx context.Context, key string) (model.Project, error) {
	if s.fail == "project get" {
		return model.Project{}, errStoreFailure
	}
	return s.ProjectStore.Get(ctx, key)
}

func (s failingProjects) Roles(ctx context.Context, key string) ([]model.Role, error) {
	if s.fail == "local roles" && key != "" || s.fail == "global roles" && key == "" {
		return nil, errStoreFailure
	}
	return s.ProjectStore.Roles(ctx, key)
}

func (s failingProjects) Role(ctx context.Context, key, name string) (model.Role, error) {
	if s.fail == "local role" && key != "" || s.fail == "global role" && key == "" {
		return model.Role{}, errStoreFailure
	}
	return s.ProjectStore.Role(ctx, key, name)
}

func (s failingProjects) PutRole(ctx context.Context, role model.Role) error {
	if s.fail == "put role" {
		return errStoreFailure
	}
	return s.ProjectStore.PutRole(ctx, role)
}

func (s failingProjects) DeleteRole(ctx context.Context, key, name string) error {
	if s.fail == "delete role" {
		return errStoreFailure
	}
	return s.ProjectStore.DeleteRole(ctx, key, name)
}

func (s failingProjects) Environments(ctx context.Context, key string) ([]model.Environment, error) {
	if s.fail == "environments" {
		return nil, errStoreFailure
	}
	return s.ProjectStore.Environments(ctx, key)
}

func (s failingProjects) Member(ctx context.Context, key string, typ model.PrincipalType, name string) (model.ProjectMember, error) {
	if s.fail == "member get" {
		return model.ProjectMember{}, errStoreFailure
	}
	return s.ProjectStore.Member(ctx, key, typ, name)
}

func TestUnitProjectStorageFailuresNeverReturnPartialRoleGrants(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail string
		read func(project.Service) ([]model.Role, error)
	}{
		{"list global definitions", "global roles", func(s project.Service) ([]model.Role, error) { return s.Roles(t.Context(), "alpha") }},
		{"list project definitions", "local roles", func(s project.Service) ([]model.Role, error) { return s.Roles(t.Context(), "alpha") }},
		{"assigned membership", "member get", func(s project.Service) ([]model.Role, error) {
			return s.AssignedRoles(t.Context(), "alpha", "alice", nil)
		}},
		{"assigned project definition", "local role", func(s project.Service) ([]model.Role, error) {
			return s.AssignedRoles(t.Context(), "alpha", "alice", nil)
		}},
		{"assigned global definition", "global role", func(s project.Service) ([]model.Role, error) {
			return s.AssignedRoles(t.Context(), "alpha", "alice", nil)
		}},
		{"named global definition", "global role", func(s project.Service) ([]model.Role, error) {
			return s.NamedRoles(t.Context(), "alpha", []string{"reader", "Viewer"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db := world(t)
			must(t, db.Users().Create(t.Context(), model.User{Name: "alice"}))
			must(t, s.CreateRole(t.Context(), "alpha", custom("reader")))
			_, err := s.PutMember(t.Context(), "alpha", model.PrincipalUser, "alice", []string{"reader", "Viewer"})
			must(t, err)
			got, err := tc.read(project.Service{Store: failingStore{Store: db, fail: tc.fail}})
			if got != nil || !errors.Is(err, errStoreFailure) || !errors.Is(err, kernel.ErrStore) {
				t.Fatalf("failed read returned partial roles or hid its storage cause: %+v %v", got, err)
			}
		})
	}
}

func TestUnitProjectFailedMutationsPreserveDefinitionsAssignmentsAndAuthority(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail string
		run  func(project.Service) error
	}{
		{"create role", "put role", func(s project.Service) error { return s.CreateRole(t.Context(), "alpha", custom("new")) }},
		{"create after definition failure", "local role", func(s project.Service) error {
			return s.CreateRole(t.Context(), "alpha", custom("new"))
		}},
		{"create after environment failure", "environments", func(s project.Service) error {
			return s.CreateRole(t.Context(), "alpha", custom("new"))
		}},
		{"replace role", "put role", func(s project.Service) error {
			return s.UpdateRole(t.Context(), "alpha", "reader", custom("reader"))
		}},
		{"replace after definition failure", "global role", func(s project.Service) error {
			return s.UpdateRole(t.Context(), "alpha", "reader", custom("reader"))
		}},
		{"delete role", "delete role", func(s project.Service) error { return s.DeleteRole(t.Context(), "alpha", "reader") }},
		{"replace member", "put member", func(s project.Service) error {
			_, err := s.PutMember(t.Context(), "alpha", model.PrincipalGroup, "team", []string{"Developer"})
			return err
		}},
		{"replace after definition failure", "global role", func(s project.Service) error {
			_, err := s.PutMember(t.Context(), "alpha", model.PrincipalGroup, "team", []string{"Developer"})
			return err
		}},
		{"delete member", "delete group member", func(s project.Service) error {
			return s.DeleteMember(t.Context(), "alpha", model.PrincipalGroup, "team")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db := world(t)
			ctx := t.Context()
			must(t, s.CreateRole(ctx, "alpha", custom("reader")))
			must(t, db.Groups().Create(ctx, model.Group{Name: "team"}))
			_, err := s.PutMember(ctx, "alpha", model.PrincipalGroup, "team", []string{"reader"})
			must(t, err)
			before, err := db.Projects().Authority(ctx, "alpha")
			must(t, err)
			roles, err := s.Roles(ctx, "alpha")
			must(t, err)
			member, err := s.Member(ctx, "alpha", model.PrincipalGroup, "team")
			must(t, err)
			if err := tc.run(project.Service{Store: failingStore{Store: db, fail: tc.fail}}); !errors.Is(err, errStoreFailure) {
				t.Fatalf("storage failure became a successful or missing-record mutation: %v", err)
			}
			after, err := db.Projects().Authority(ctx, "alpha")
			must(t, err)
			gotRoles, err := s.Roles(ctx, "alpha")
			must(t, err)
			gotMember, err := s.Member(ctx, "alpha", model.PrincipalGroup, "team")
			must(t, err)
			if before != after || !reflect.DeepEqual(roles, gotRoles) || !reflect.DeepEqual(member, gotMember) {
				t.Fatal("failed mutation changed project authority, role definitions or direct membership")
			}
		})
	}
}
