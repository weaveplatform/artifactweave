package sql_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"testing/fstest"

	accsql "github.com/weaveplatform/artifactweave/internal/access/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/access/adapters/sql/migrations"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	pgdrv "github.com/weaveplatform/artifactweave/internal/platform/sql/postgres"
	sqlitedrv "github.com/weaveplatform/artifactweave/internal/platform/sql/sqlite"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func TestAccProjectAuthorityUpgrade(t *testing.T) {
	baseline := accsql.Module
	baseline.AfterMigrate = nil
	files := fstest.MapFS{}
	for _, name := range []string{"sqlite/0001_baseline.sql", "postgres/0001_baseline.sql"} {
		data, err := migrations.FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = &fstest.MapFile{Data: data}
	}
	baseline.Migrations = files
	sqltest.RunModule(t, []dialect.Module{baseline}, func(db *dialect.Store) *dialect.Store { return db },
		func(t *testing.T, newDB func(*testing.T) *dialect.Store) {
			db, ctx := newDB(t), t.Context()
			// Both collections cross the backfill's page boundary. Seed through
			// the old columns because an old process cannot write the new ones.
			storekit.Must(t, db.Tx(ctx, func(ctx context.Context) error {
				for i := range kernel.MaxLimit + 1 {
					key, repo := fmt.Sprintf("legacy-%04d", i), fmt.Sprintf("legacy-repo-%04d", i)
					if _, err := db.Exec(ctx, "seed legacy project",
						"INSERT INTO projects (key, display_name) VALUES (?, ?)", key, "Legacy "+key); err != nil {
						return err
					}
					if _, err := db.Exec(ctx, "seed legacy assignment",
						"INSERT INTO project_repositories (repo_key, project_key, environments) VALUES (?, ?, ?)",
						repo, key, `["DEV"]`); err != nil {
						return err
					}
				}
				_, err := db.Exec(ctx, "seed legacy share",
					"INSERT INTO project_repository_shares (repo_key, project_key) VALUES (?, ?)",
					"legacy-repo-0000", "legacy-0001")
				return err
			}))
			ps := accsql.New(db).Projects()
			projects := storekit.Get[[]model.Project](t)(ps.List(ctx))
			assignment := storekit.Get[model.RepositoryAssignment](t)(ps.Assignment(ctx, "legacy-repo-0000"))
			_, err := dialect.Migrate(ctx, db.DB(), db.Dialect(), []dialect.Module{accsql.Module}, "access")
			storekit.Must(t, err)
			storekit.Must(t, accsql.Module.AfterMigrate(ctx, db))
			initial := projectAuthorityRows(t, db)
			if len(initial) != 3*(kernel.MaxLimit+1) {
				t.Fatalf("backfilled authority fields = %d", len(initial))
			}
			incarnations := map[string]bool{}
			for i := range kernel.MaxLimit + 1 {
				key := fmt.Sprintf("legacy-%04d", i)
				a := storekit.Get[model.ProjectAuthority](t)(ps.Authority(ctx, key))
				if a.Incarnation == "" || a.Revision == "" || incarnations[string(a.Incarnation)] {
					t.Fatal("backfill did not allocate distinct complete project lifetimes")
				}
				incarnations[string(a.Incarnation)] = true
			}
			for range 2 {
				storekit.Must(t, accsql.Module.AfterMigrate(ctx, db))
				storekit.Same(t, "repeat backfill preserves authority", initial, projectAuthorityRows(t, db))
			}
			storekit.Same(t, "legacy public project values", projects, storekit.Get[[]model.Project](t)(ps.List(ctx)))
			storekit.Same(t, "legacy assignment and share", assignment, storekit.Get[model.RepositoryAssignment](t)(ps.Assignment(ctx, "legacy-repo-0000")))
		}, sqltest.WithPostgres())
}

func projectAuthorityRows(t *testing.T, db *dialect.Store) map[string]string {
	t.Helper()
	values := map[string]string{}
	storekit.Must(t, db.Rows(t.Context(), "read project authority snapshot",
		"SELECT key, incarnation, policy_revision FROM projects ORDER BY key", nil, func(row *sql.Rows) error {
			var key, incarnation, revision string
			if err := row.Scan(&key, &incarnation, &revision); err != nil {
				return err
			}
			if incarnation == "" || revision == "" {
				t.Error("missing project authority after migration")
			}
			values["project/"+key] = incarnation
			values["policy/"+key] = revision
			return nil
		}))
	storekit.Must(t, db.Rows(t.Context(), "read sharing authority snapshot",
		"SELECT repo_key, sharing_revision FROM project_repositories ORDER BY repo_key", nil, func(row *sql.Rows) error {
			var key, revision string
			if err := row.Scan(&key, &revision); err != nil {
				return err
			}
			if revision == "" {
				t.Error("missing sharing authority after migration")
			}
			values["sharing/"+key] = revision
			return nil
		}))
	return values
}

func TestAccProjectAuthorityPersists(t *testing.T) {
	t.Run("sqlite file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "projects.db")
		testProjectAuthorityReopen(t, sqlitedrv.Dialect, func() (*sql.DB, error) {
			return sqlitedrv.OpenDB(path, sqlitedrv.Options{})
		})
	})
	t.Run("postgres", func(t *testing.T) {
		dsn := sqltest.ScratchDSN(t, sqltest.PostgresDSN(t))
		testProjectAuthorityReopen(t, pgdrv.Dialect, func() (*sql.DB, error) {
			return pgdrv.OpenDB(dsn, pgdrv.Options{})
		})
	})
}

func testProjectAuthorityReopen(t *testing.T, d dialect.Dialect, connect func() (*sql.DB, error)) {
	t.Helper()
	open := func() *accsql.Store {
		t.Helper()
		db, err := connect()
		storekit.Must(t, err)
		t.Cleanup(func() { _ = db.Close() })
		_, err = dialect.Migrate(t.Context(), db, d, []dialect.Module{accsql.Module}, "access")
		storekit.Must(t, err)
		shared := dialect.New(db, d, nil)
		storekit.Must(t, accsql.Module.AfterMigrate(t.Context(), shared))
		return accsql.New(shared)
	}
	first := open()
	storekit.Must(t, first.Projects().Create(t.Context(), model.Project{Key: "alpha"}))
	storekit.Must(t, first.Projects().PutAssignment(t.Context(), model.RepositoryAssignment{RepoKey: "libs", ProjectKey: "alpha"}))
	project := storekit.Get[model.ProjectAuthority](t)(first.Projects().Authority(t.Context(), "alpha"))
	sharing := storekit.Get[model.SharingAuthority](t)(first.Projects().SharingAuthority(t.Context(), "libs"))
	storekit.Must(t, first.Close())
	second := open()
	storekit.Same(t, "project authority after reopen", project, storekit.Get[model.ProjectAuthority](t)(second.Projects().Authority(t.Context(), "alpha")))
	storekit.Same(t, "sharing authority after reopen", sharing, storekit.Get[model.SharingAuthority](t)(second.Projects().SharingAuthority(t.Context(), "libs")))
}

func TestAccProjectAuthorityMutationRollback(t *testing.T) {
	cases := []struct {
		name  string
		event string
		table string
		apply func(context.Context, port.ProjectStore) error
	}{
		{"project role put", "UPDATE", "projects", func(ctx context.Context, ps port.ProjectStore) error {
			return ps.PutRole(ctx, model.Role{ProjectKey: "p1", Name: "qa", Actions: []string{"WRITE_REPOSITORY"}})
		}},
		{"project role delete", "UPDATE", "projects", func(ctx context.Context, ps port.ProjectStore) error {
			return ps.DeleteRole(ctx, "p1", "qa")
		}},
		{"global role put", "UPDATE", "projects", func(ctx context.Context, ps port.ProjectStore) error {
			return ps.PutRole(ctx, model.Role{Name: "global", Actions: []string{"WRITE_REPOSITORY"}})
		}},
		{"global role delete", "UPDATE", "projects", func(ctx context.Context, ps port.ProjectStore) error {
			return ps.DeleteRole(ctx, "", "global")
		}},
		{"project environment create", "UPDATE", "projects", func(ctx context.Context, ps port.ProjectStore) error {
			return ps.CreateEnvironment(ctx, model.Environment{ProjectKey: "p1", Name: "p1-NEW"})
		}},
		{"project environment rename", "UPDATE", "projects", func(ctx context.Context, ps port.ProjectStore) error {
			return ps.RenameEnvironment(ctx, "p1", "p1-E", "p1-NEW")
		}},
		{"project environment delete", "UPDATE", "projects", func(ctx context.Context, ps port.ProjectStore) error {
			return ps.DeleteEnvironment(ctx, "p1", "p1-E")
		}},
		{"global environment create", "UPDATE", "projects", func(ctx context.Context, ps port.ProjectStore) error {
			return ps.CreateEnvironment(ctx, model.Environment{Name: "GLOBAL-NEW"})
		}},
		{"global environment rename", "UPDATE", "projects", func(ctx context.Context, ps port.ProjectStore) error {
			return ps.RenameEnvironment(ctx, "", "GLOBAL-E", "GLOBAL-NEW")
		}},
		{"global environment delete", "UPDATE", "projects", func(ctx context.Context, ps port.ProjectStore) error {
			return ps.DeleteEnvironment(ctx, "", "GLOBAL-E")
		}},
		{"member put", "UPDATE", "projects", func(ctx context.Context, ps port.ProjectStore) error {
			return ps.PutMember(ctx, model.ProjectMember{ProjectKey: "p1", Type: model.PrincipalUser, Name: "ann", Roles: []string{"qa"}})
		}},
		{"member delete", "UPDATE", "projects", func(ctx context.Context, ps port.ProjectStore) error {
			return ps.DeleteMember(ctx, "p1", model.PrincipalUser, "ann")
		}},
		{"share replacement", "DELETE", "project_repository_shares", func(ctx context.Context, ps port.ProjectStore) error {
			return ps.PutAssignment(ctx, model.RepositoryAssignment{RepoKey: "repo-a", ProjectKey: "p1"})
		}},
		{"receiving project deletion", "UPDATE", "project_repositories", func(ctx context.Context, ps port.ProjectStore) error {
			return ps.Delete(ctx, "p2")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := projectStore(t)
			storekit.Must(t, s.Projects().CreateEnvironment(t.Context(), model.Environment{Name: "GLOBAL-E"}))
			storekit.Must(t, s.Projects().PutRole(t.Context(), model.Role{Name: "global", Environments: []string{"GLOBAL-E"}}))
			before := projectValueSnapshot(t, s)
			sqltest.Exec(t, s.DB(), sqltest.FailTrigger(tc.event, tc.table))
			storekit.WantErr(t, tc.apply(t.Context(), s.Projects()), kernel.ErrStore)
			storekit.Same(t, "failed mutation rolls back values and authorities", before, projectValueSnapshot(t, s))
		})
	}
}

type projectValues struct {
	Projects           []model.Project
	Roles              []model.Role
	GlobalRoles        []model.Role
	Environments       []model.Environment
	GlobalEnvironments []model.Environment
	Members            []model.ProjectMember
	Assignments        []model.RepositoryAssignment
	Authorities        map[string]string
}

func projectValueSnapshot(t *testing.T, s *accsql.Store) projectValues {
	t.Helper()
	ps, ctx := s.Projects(), t.Context()
	return projectValues{
		Projects:           storekit.Get[[]model.Project](t)(ps.List(ctx)),
		Roles:              storekit.Get[[]model.Role](t)(ps.Roles(ctx, "p1")),
		GlobalRoles:        storekit.Get[[]model.Role](t)(ps.Roles(ctx, "")),
		Environments:       storekit.Get[[]model.Environment](t)(ps.Environments(ctx, "p1")),
		GlobalEnvironments: storekit.Get[[]model.Environment](t)(ps.Environments(ctx, "")),
		Members:            storekit.Get[[]model.ProjectMember](t)(ps.Members(ctx, "p1", model.PrincipalUser)),
		Assignments:        storekit.Get[[]model.RepositoryAssignment](t)(ps.Assignments(ctx, "p1")),
		Authorities:        projectAuthorityRows(t, s.Store),
	}
}
