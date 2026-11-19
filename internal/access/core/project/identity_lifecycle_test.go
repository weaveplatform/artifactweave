package project_test

import (
	"context"
	"errors"
	"testing"

	accsql "github.com/weaveplatform/artifactweave/internal/access/adapters/sql"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/identity"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

// Deletion reads no account settings. These fixtures migrate the real access
// module alone; a future settings dependency must update the fixture explicitly.
type lifecycleStore struct{ port.Store }

func (lifecycleStore) Settings() settings.SettingsStore { return nil }

type lifecycleFaultStore struct {
	identity.Store
	fail string
}

func (s lifecycleFaultStore) Projects() port.ProjectStore {
	return lifecycleProjects{s.Store.Projects(), s.fail}
}

func (s lifecycleFaultStore) Users() port.UserStore { return lifecycleUsers{s.Store.Users(), s.fail} }

func (s lifecycleFaultStore) Groups() port.GroupStore {
	return lifecycleGroups{s.Store.Groups(), s.fail}
}

func (s lifecycleFaultStore) Tokens() port.TokenStore {
	return lifecycleTokens{s.Store.Tokens(), s.fail}
}

func (s lifecycleFaultStore) Permissions() port.PermissionStore {
	return lifecyclePermissions{s.Store.Permissions(), s.fail}
}

type lifecycleProjects struct {
	port.ProjectStore
	fail string
}

func (s lifecycleProjects) List(ctx context.Context) ([]model.Project, error) {
	if s.fail == "projects.list" {
		return nil, errStoreFailure
	}
	return s.ProjectStore.List(ctx)
}

func (s lifecycleProjects) DeleteMember(ctx context.Context, key string, typ model.PrincipalType, name string) error {
	if s.fail == "projects.delete.beta" && key == "beta" {
		return errStoreFailure
	}
	return s.ProjectStore.DeleteMember(ctx, key, typ, name)
}

func (s lifecycleProjects) Member(ctx context.Context, key string, typ model.PrincipalType, name string) (model.ProjectMember, error) {
	if s.fail == "projects.member.beta" && key == "beta" {
		return model.ProjectMember{}, errStoreFailure
	}
	return s.ProjectStore.Member(ctx, key, typ, name)
}

type lifecycleUsers struct {
	port.UserStore
	fail string
}

func (s lifecycleUsers) Delete(ctx context.Context, name string) error {
	if s.fail == "identity.delete" {
		return errStoreFailure
	}
	return s.UserStore.Delete(ctx, name)
}

type lifecycleGroups struct {
	port.GroupStore
	fail string
}

func (s lifecycleGroups) Delete(ctx context.Context, name string) error {
	if s.fail == "identity.delete" {
		return errStoreFailure
	}
	return s.GroupStore.Delete(ctx, name)
}

type lifecycleTokens struct {
	port.TokenStore
	fail string
}

func (s lifecycleTokens) Delete(ctx context.Context, id string) error {
	if s.fail == "tokens.delete" {
		return errStoreFailure
	}
	return s.TokenStore.Delete(ctx, id)
}

type lifecyclePermissions struct {
	port.PermissionStore
	fail string
}

func (s lifecyclePermissions) Update(ctx context.Context, p model.Permission) error {
	if s.fail == "permissions.update" {
		return errStoreFailure
	}
	return s.PermissionStore.Update(ctx, p)
}

func TestUnitIdentityDeletionAndProjectCleanupShareTransaction(t *testing.T) {
	testIdentityProjectCleanup(t, func(*testing.T) port.Store { return inmem.New() })
}

func TestAccIdentityDeletionAndProjectCleanupShareTransaction(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{accsql.Module}, accsql.New,
		func(t *testing.T, newStore func(*testing.T) *accsql.Store) {
			testIdentityProjectCleanup(t, func(t *testing.T) port.Store { return newStore(t) })
		}, sqltest.WithPostgres())
}

func testIdentityProjectCleanup(t *testing.T, newStore func(*testing.T) port.Store) {
	t.Helper()
	for _, typ := range []model.PrincipalType{model.PrincipalUser, model.PrincipalGroup} {
		failures := []string{"", "projects.list", "projects.member.beta", "projects.delete.beta", "identity.delete", "tokens.delete"}
		if typ == model.PrincipalGroup {
			failures[len(failures)-1] = "permissions.update"
		}
		for _, fail := range failures {
			name := fail
			if name == "" {
				name = "success"
			}
			t.Run(string(typ)+"/"+name, func(t *testing.T) {
				db, ctx := newStore(t), t.Context()
				must(t, db.Users().Create(ctx, model.User{Name: "member"}))
				must(t, db.Groups().Create(ctx, model.Group{Name: "member"}))
				must(t, db.Tokens().Create(ctx, model.Token{ID: "member-token", Subject: "jfac@svc/users/member"}))
				must(t, db.Permissions().Create(ctx, model.Permission{Name: "group-permission", Resources: map[accessapi.ResourceType]model.Resource{
					accessapi.ResourceArtifact: {Actions: model.Grants{Groups: map[string]accessapi.Actions{"member": accessapi.ActionRead}}},
				}}))
				projects := project.Service{Store: db}
				before := map[string]model.ProjectAuthority{}
				for _, key := range []string{"alpha", "beta", "unrelated"} {
					must(t, db.Projects().Create(ctx, model.Project{Key: key}))
					if key != "unrelated" {
						_, err := projects.PutMember(ctx, key, typ, "member", []string{"Viewer"})
						must(t, err)
					}
					var err error
					before[key], err = db.Projects().Authority(ctx, key)
					must(t, err)
				}
				service := identity.Service{Store: lifecycleFaultStore{Store: lifecycleStore{db}, fail: fail}}
				var err error
				if typ == model.PrincipalUser {
					err = service.DeleteUser(ctx, "MEMBER")
				} else {
					err = service.DeleteGroup(ctx, "member")
				}
				if fail == "" {
					must(t, err)
					if typ == model.PrincipalUser {
						must(t, db.Users().Create(ctx, model.User{Name: "member"}))
					} else {
						must(t, db.Groups().Create(ctx, model.Group{Name: "member"}))
					}
				} else if !errors.Is(err, errStoreFailure) || !errors.Is(err, kernel.ErrStore) {
					t.Fatalf("deletion fault was swallowed or misclassified: %v", err)
				}
				for key, initial := range before {
					after, err := db.Projects().Authority(ctx, key)
					must(t, err)
					changed := fail == "" && key != "unrelated"
					if after.Incarnation != initial.Incarnation || (after.Revision != initial.Revision) != changed {
						t.Fatalf("project %s authority after %s/%s: before=%+v after=%+v", key, typ, name, initial, after)
					}
					if key == "unrelated" {
						continue
					}
					_, err = projects.Member(ctx, key, typ, "member")
					if fail == "" {
						if !errors.Is(err, project.ErrNotFound) {
							t.Fatalf("deleted assignment remained in %s: %v", key, err)
						}
					} else {
						must(t, err)
					}
				}
				if fail != "" {
					_, err := db.Users().Get(ctx, "member")
					must(t, err)
					_, err = db.Groups().Get(ctx, "member")
					must(t, err)
					_, err = db.Tokens().Get(ctx, "member-token")
					must(t, err)
					permission, err := db.Permissions().Get(ctx, "group-permission")
					must(t, err)
					if permission.Resources[accessapi.ResourceArtifact].Actions.Groups["member"] != accessapi.ActionRead {
						t.Fatal("permission cleanup survived rollback")
					}
				}
			})
		}
	}
}
