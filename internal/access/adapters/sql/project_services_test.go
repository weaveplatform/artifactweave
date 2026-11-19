package sql_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	accsql "github.com/weaveplatform/artifactweave/internal/access/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
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

func TestUnitProjectRoleCleanupRollsBack(t *testing.T) {
	testRoleCleanupRollback(t, func(*testing.T) port.Store { return inmem.New() })
}

func TestAccProjectRoleCleanupRollsBack(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{accsql.Module}, accsql.New,
		func(t *testing.T, newStore func(*testing.T) *accsql.Store) {
			testRoleCleanupRollback(t, func(t *testing.T) port.Store { return newStore(t) })
		}, sqltest.WithPostgres())
}

// These are faults at the store port, over real transaction implementations.
// They prove rollback after earlier statements, not a simulated driver error.
func testRoleCleanupRollback(t *testing.T, newStore func(*testing.T) port.Store) {
	t.Helper()
	for _, failure := range []string{"put member", "delete group member", "read group members"} {
		t.Run(failure, func(t *testing.T) {
			db, ctx := newStore(t), t.Context()
			must(t, db.Projects().Create(ctx, model.Project{Key: "alpha"}))
			must(t, db.Users().Create(ctx, model.User{Name: "alice"}))
			must(t, db.Users().Create(ctx, model.User{Name: "bob"}))
			must(t, db.Groups().Create(ctx, model.Group{Name: "team"}))
			s := project.Service{Store: db}
			must(t, s.CreateRole(ctx, "alpha", custom("reader")))
			members := []model.ProjectMember{
				{ProjectKey: "alpha", Type: model.PrincipalUser, Name: "alice", Roles: []string{"reader", "Viewer"}},
				{ProjectKey: "alpha", Type: model.PrincipalUser, Name: "bob", Roles: []string{"reader"}},
				{ProjectKey: "alpha", Type: model.PrincipalGroup, Name: "team", Roles: []string{"reader"}},
			}
			for _, member := range members {
				_, err := s.PutMember(ctx, "alpha", member.Type, member.Name, member.Roles)
				must(t, err)
			}
			before, err := db.Projects().Authority(ctx, "alpha")
			must(t, err)
			broken := project.Service{Store: failingStore{Store: db, fail: failure}}
			if err := broken.DeleteRole(ctx, "alpha", "reader"); !errors.Is(err, errStoreFailure) || !errors.Is(err, kernel.ErrStore) {
				t.Fatalf("cleanup storage failure was hidden or misclassified: %v", err)
			}
			if _, err := s.Role(ctx, "alpha", "reader"); err != nil {
				t.Fatalf("role removal survived rollback: %v", err)
			}
			for _, want := range members {
				got, err := s.Member(ctx, "alpha", want.Type, want.Name)
				must(t, err)
				if !slices.Equal(got.Roles, want.Roles) {
					t.Errorf("membership %s/%s did not roll back: %v, want %v", want.Type, want.Name, got.Roles, want.Roles)
				}
			}
			after, err := db.Projects().Authority(ctx, "alpha")
			must(t, err)
			if before != after {
				t.Fatal("policy revision survived a failed role cleanup")
			}
		})
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func custom(name string) model.Role {
	return model.Role{Name: name, Type: model.RoleCustom, Actions: []string{"READ_REPOSITORY"}, Environments: []string{"DEV"}}
}
