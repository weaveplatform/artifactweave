package sql_test

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

var errLifecycleRollback = errors.New("injected lifecycle rollback")

func lifecycleCustomRole(name string) model.Role {
	return model.Role{Name: name, Type: model.RoleCustom, Actions: []string{"READ_REPOSITORY"}, Environments: []string{"DEV"}}
}

func lifecycleSQL(t *testing.T, suite func(*testing.T, func(*testing.T) *support.AccessArtifactsSQLStore)) {
	t.Helper()
	support.RunAccessArtifactsSQL(t, suite)
}

func lifecycleService(db *support.AccessArtifactsSQLStore) project.Service {
	return project.Service{Store: db, Repositories: support.Repositories(db)}
}

func failLifecycleSQL(t *testing.T, db *support.AccessArtifactsSQLStore, event, table string) {
	t.Helper()
	if db.Dialect().Name == "postgres" {
		sqltest.Exec(t, db.DB(),
			"CREATE FUNCTION lifecycle_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$",
			"CREATE TRIGGER lifecycle_failure BEFORE "+event+" ON "+table+" FOR EACH ROW EXECUTE FUNCTION lifecycle_failure()")
	} else {
		sqltest.Exec(t, db.DB(), sqltest.FailTrigger(event, table))
	}
}

func TestAccProjectLifecycleCreateRollback(t *testing.T) {
	lifecycleSQL(t, func(t *testing.T, newStore func(*testing.T) *support.AccessArtifactsSQLStore) {
		for _, failure := range []string{"project insert", "repository insert", "after both writes"} {
			t.Run(failure, func(t *testing.T) {
				db, ctx := newStore(t), t.Context()
				svc := lifecycleService(db)
				p := model.Project{Key: "alpha", DisplayName: "Alpha"}
				if failure == "project insert" {
					failLifecycleSQL(t, db, "INSERT", "projects")
				} else if failure == "repository insert" {
					failLifecycleSQL(t, db, "INSERT", "repositories")
				}
				err := db.Tx(ctx, func(ctx context.Context) error {
					if err := svc.CreateProject(ctx, p); err != nil {
						return err
					}
					if _, err := db.Repos().Get(ctx, "alpha-build-info"); err != nil {
						return err
					}
					return errLifecycleRollback // Both real writes happened before the enclosing transaction fails.
				})
				if err == nil {
					t.Fatal("injected creation failure committed")
				}
				if _, err := db.Projects().Get(ctx, p.Key); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatalf("project survived rollback: %v", err)
				}
				if _, err := db.Repos().Get(ctx, "alpha-build-info"); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatalf("build-info survived rollback: %v", err)
				}
			})
		}
	})
}

func TestAccProjectLifecycleDeleteRollbackAndRetainedContent(t *testing.T) {
	lifecycleSQL(t, func(t *testing.T, newStore func(*testing.T) *support.AccessArtifactsSQLStore) {
		for _, fail := range []bool{true, false} {
			t.Run(map[bool]string{true: "failed final delete", false: "committed deletion"}[fail], func(t *testing.T) {
				db, ctx := newStore(t), t.Context()
				svc := lifecycleService(db)
				storekit.Must(t, svc.CreateProject(ctx, model.Project{Key: "alpha", DisplayName: "Alpha"}))
				storekit.Must(t, db.Users().Create(ctx, model.User{Name: "alice"}))
				storekit.Must(t, svc.CreateRole(ctx, "alpha", lifecycleCustomRole("reader")))
				storekit.Get[model.ProjectMember](t)(svc.PutMember(ctx, "alpha", model.PrincipalUser, "alice", []string{"reader"}))
				owned := *repo.Defaults(&repo.Config{Key: "owned", Class: repo.Local, PackageType: repo.Generic, ProjectKey: "alpha", Environments: []string{"DEV"}})
				storekit.Must(t, db.Repos().Create(ctx, owned))
				content := storekit.Get[support.Item](t)(db.Items().Put(ctx, support.Item{RepoKey: "owned", Path: ".", Name: "kept.txt", Size: 7}))
				before := storekit.Get[model.ProjectAuthority](t)(db.Projects().Authority(ctx, "alpha"))
				if fail {
					failLifecycleSQL(t, db, "DELETE", "projects")
				}
				err := svc.DeleteProject(ctx, "alpha")
				if fail {
					if err == nil {
						t.Fatal("final project-delete failure committed earlier unassignment")
					}
					storekit.Same(t, "project authority rollback", before, storekit.Get[model.ProjectAuthority](t)(db.Projects().Authority(ctx, "alpha")))
					storekit.Same(t, "repository unassignment rollback", owned, storekit.Get[repo.Config](t)(db.Repos().Get(ctx, owned.Key)))
					storekit.Get[model.ProjectMember](t)(svc.Member(ctx, "alpha", model.PrincipalUser, "alice"))
					storekit.Get[model.Role](t)(svc.Role(ctx, "alpha", "reader"))
				} else {
					storekit.Must(t, err)
					if _, err := svc.Project(ctx, "alpha"); !errors.Is(err, project.ErrNotFound) {
						t.Fatalf("deleted project remains: %v", err)
					}
					for _, key := range []string{"owned", "alpha-build-info"} {
						got := storekit.Get[repo.Config](t)(db.Repos().Get(ctx, key))
						if got.ProjectKey != "" || len(got.Environments) != 0 {
							t.Fatalf("repository was not retained/unassigned: %+v", got)
						}
					}
				}
				storekit.Same(t, "retained item metadata", content, storekit.Get[support.Item](t)(db.Items().Get(ctx, "owned", false, "kept.txt")))
			})
		}
	})
}

func TestAccProjectLifecycleEnvironmentRenameRollback(t *testing.T) {
	lifecycleSQL(t, func(t *testing.T, newStore func(*testing.T) *support.AccessArtifactsSQLStore) {
		for _, fail := range []bool{true, false} {
			t.Run(map[bool]string{true: "repository update fails", false: "all references renamed"}[fail], func(t *testing.T) {
				db, ctx := newStore(t), t.Context()
				svc := lifecycleService(db)
				storekit.Must(t, svc.CreateProject(ctx, model.Project{Key: "alpha", DisplayName: "Alpha"}))
				storekit.Get[model.Environment](t)(svc.CreateEnvironment(ctx, "alpha", "alpha-qa"))
				role := lifecycleCustomRole("reader")
				role.Environments = []string{"alpha-qa"}
				storekit.Must(t, svc.CreateRole(ctx, "alpha", role))
				owned := *repo.Defaults(&repo.Config{Key: "owned", Class: repo.Local, PackageType: repo.Generic, ProjectKey: "alpha", Environments: []string{"alpha-qa"}})
				storekit.Must(t, db.Repos().Create(ctx, owned))
				storekit.Must(t, db.Projects().PutAssignment(ctx, model.RepositoryAssignment{RepoKey: "owned", ProjectKey: "alpha", Environments: []string{"alpha-qa"}}))
				before := storekit.Get[model.ProjectAuthority](t)(db.Projects().Authority(ctx, "alpha"))
				assignment := storekit.Get[model.RepositoryAssignment](t)(db.Projects().Assignment(ctx, "owned"))
				if fail {
					failLifecycleSQL(t, db, "UPDATE", "repositories")
				}
				_, err := svc.RenameEnvironment(ctx, "alpha", "ALPHA-QA", "alpha-stage")
				if fail {
					if err == nil {
						t.Fatal("repository update fault did not fail environment rename")
					}
					storekit.Same(t, "authority rollback", before, storekit.Get[model.ProjectAuthority](t)(db.Projects().Authority(ctx, "alpha")))
					storekit.Same(t, "assignment rollback", assignment, storekit.Get[model.RepositoryAssignment](t)(db.Projects().Assignment(ctx, "owned")))
					storekit.Same(t, "repository rollback", owned, storekit.Get[repo.Config](t)(db.Repos().Get(ctx, "owned")))
				} else {
					storekit.Must(t, err)
				}
				want := "alpha-stage"
				if fail {
					want = "alpha-qa"
				}
				envs := storekit.Get[[]model.Environment](t)(svc.Environments(ctx, "alpha"))
				current := storekit.Get[model.Role](t)(svc.Role(ctx, "alpha", "reader"))
				assigned := storekit.Get[model.RepositoryAssignment](t)(db.Projects().Assignment(ctx, "owned"))
				stored := storekit.Get[repo.Config](t)(db.Repos().Get(ctx, "owned"))
				if len(envs) != 1 || envs[0].Name != want || current.Environments[0] != want || assigned.Environments[0] != want || stored.Environments[0] != want {
					t.Fatal("environment, role, assignment and repository names diverged")
				}
			})
		}
	})
}
