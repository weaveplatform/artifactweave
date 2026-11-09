package sql_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	accsql "github.com/weaveplatform/artifactweave/migration/access/adapters/sql"
	"github.com/weaveplatform/artifactweave/migration/access/core/model"
	"github.com/weaveplatform/artifactweave/migration/access/core/port"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/sqlite"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/sqltest"
)

// projectStore opens a SQLite store, sealed with the test keyring, that
// holds the access module's tables alone, loaded with one of every project
// record.
func projectStore(t *testing.T) *accsql.Store {
	t.Helper()
	ctx := context.Background()
	s := accsql.New(sqltest.OpenSQLite(t, accsql.Module))
	ps := s.Projects()
	for _, err := range []error{
		ps.Create(ctx, model.Project{Key: "p1"}),
		ps.Create(ctx, model.Project{Key: "p2"}),
		ps.PutRole(ctx, model.Role{ProjectKey: "p1", Name: "qa", Environments: []string{"p1-E"}}),
		ps.CreateEnvironment(ctx, model.Environment{ProjectKey: "p1", Name: "p1-E"}),
		ps.PutMember(ctx, model.ProjectMember{ProjectKey: "p1", Type: model.PrincipalUser, Name: "ann"}),
		ps.PutAssignment(ctx, model.RepositoryAssignment{
			RepoKey: "repo-a", ProjectKey: "p1", Environments: []string{"p1-E"}, SharedWith: []string{"p2"},
		}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// TestAccProjectsFaults injects a failure into the statement after a
// successful one and checks every multi-statement write and read reports
// it instead of half-applying or swallowing it.
func TestAccProjectsFaults(t *testing.T) {
	ps := func(s port.Store) port.ProjectStore { return s.Projects() }
	cases := []struct {
		name   string
		inject []string
		op     func(context.Context, port.Store) error
	}{
		{
			"project delete",
			[]string{sqltest.DropTable("roles")},
			func(ctx context.Context, s port.Store) error {
				return ps(s).Delete(ctx, "p1")
			},
		},
		{
			"put role",
			[]string{sqltest.DropTable("roles")},
			func(ctx context.Context, s port.Store) error {
				return ps(s).PutRole(ctx, model.Role{ProjectKey: "p1", Name: "x"})
			},
		},
		{
			"create environment",
			[]string{sqltest.FailTrigger("INSERT", "environments")},
			func(ctx context.Context, s port.Store) error {
				return ps(
					s,
				).CreateEnvironment(ctx, model.Environment{ProjectKey: "p1", Name: "p1-F"})
			},
		},
		{
			"rename environment roles read",
			[]string{sqltest.DropTable("roles")},
			func(ctx context.Context, s port.Store) error {
				return ps(s).RenameEnvironment(ctx, "p1", "p1-E", "p1-G")
			},
		},
		{
			"rename environment role update",
			[]string{sqltest.FailTrigger("UPDATE", "roles")},
			func(ctx context.Context, s port.Store) error {
				return ps(s).RenameEnvironment(ctx, "p1", "p1-E", "p1-G")
			},
		},
		{
			"rename environment assignments read",
			[]string{sqltest.DropTable("project_repository_shares")},
			func(ctx context.Context, s port.Store) error {
				return ps(s).RenameEnvironment(ctx, "p1", "p1-E", "p1-G")
			},
		},
		{
			"rename environment assignment update",
			[]string{sqltest.FailTrigger("UPDATE", "project_repositories")},
			func(ctx context.Context, s port.Store) error {
				return ps(s).RenameEnvironment(ctx, "p1", "p1-E", "p1-G")
			},
		},
		{
			"put member",
			[]string{sqltest.FailTrigger("INSERT", "project_members")},
			func(ctx context.Context, s port.Store) error {
				return ps(
					s,
				).PutMember(ctx, model.ProjectMember{ProjectKey: "p1", Type: model.PrincipalGroup, Name: "g"})
			},
		},
		{
			"put assignment",
			[]string{sqltest.FailTrigger("INSERT", "project_repositories")},
			func(ctx context.Context, s port.Store) error {
				return ps(
					s,
				).PutAssignment(ctx, model.RepositoryAssignment{RepoKey: "repo-b", ProjectKey: "p1"})
			},
		},
		{
			"put assignment clear shares",
			[]string{sqltest.FailTrigger("DELETE", "project_repository_shares")},
			func(ctx context.Context, s port.Store) error {
				return ps(
					s,
				).PutAssignment(ctx, model.RepositoryAssignment{RepoKey: "repo-a", ProjectKey: "p1"})
			},
		},
		{
			"put assignment share",
			[]string{sqltest.FailTrigger("INSERT", "project_repository_shares")},
			func(ctx context.Context, s port.Store) error {
				return ps(s).PutAssignment(ctx,
					model.RepositoryAssignment{
						RepoKey:    "repo-b",
						ProjectKey: "p1",
						SharedWith: []string{"p2"},
					})
			},
		},
		{
			"get assignment shares",
			[]string{sqltest.DropTable("project_repository_shares")},
			func(ctx context.Context, s port.Store) error {
				_, err := ps(s).Assignment(ctx, "repo-a")
				return err
			},
		},
		{
			"delete assignment shares",
			[]string{sqltest.DropTable("project_repository_shares")},
			func(ctx context.Context, s port.Store) error { return ps(s).DeleteAssignment(ctx, "repo-a") },
		},
		{
			"delete assignment",
			[]string{sqltest.FailTrigger("DELETE", "project_repositories")},
			func(ctx context.Context, s port.Store) error { return ps(s).DeleteAssignment(ctx, "repo-a") },
		},
		{
			"corrupt role document",
			[]string{"UPDATE roles SET environments = 'not json'"},
			func(ctx context.Context, s port.Store) error {
				_, err := ps(s).Role(ctx, "p1", "qa")
				return err
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			s := projectStore(t)
			sqltest.Exec(t, s.DB(), c.inject...)
			if err := c.op(ctx, s); err == nil || errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("want an injected failure, got %v", err)
			}
		})
	}
}

// TestAccProjectsUnmigrated runs every project store method against a
// database without the schema: each must surface the driver error, never a
// nil error or a misleading ErrNotFound.
func TestAccProjectsUnmigrated(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.OpenDB(filepath.Join(t.TempDir(), "raw.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ps := accsql.New(dialect.New(db, sqlite.Dialect, nil)).Projects()
	errOf := func(_ any, err error) error { return err }
	ops := map[string]error{
		"project create":     ps.Create(ctx, model.Project{Key: "p"}),
		"project get":        errOf(ps.Get(ctx, "p")),
		"project update":     ps.Update(ctx, model.Project{Key: "p"}),
		"project delete":     ps.Delete(ctx, "p"),
		"project list":       errOf(ps.List(ctx)),
		"put role":           ps.PutRole(ctx, model.Role{Name: "r"}),
		"put project role":   ps.PutRole(ctx, model.Role{ProjectKey: "p", Name: "r"}),
		"role":               errOf(ps.Role(ctx, "", "r")),
		"roles":              errOf(ps.Roles(ctx, "")),
		"delete role":        ps.DeleteRole(ctx, "", "r"),
		"create environment": ps.CreateEnvironment(ctx, model.Environment{Name: "E"}),
		"environments":       errOf(ps.Environments(ctx, "")),
		"rename environment": ps.RenameEnvironment(ctx, "", "E", "F"),
		"delete environment": ps.DeleteEnvironment(ctx, "", "E"),
		"put member":         ps.PutMember(ctx, model.ProjectMember{Name: "m"}),
		"member":             errOf(ps.Member(ctx, "", model.PrincipalUser, "m")),
		"members":            errOf(ps.Members(ctx, "", model.PrincipalUser)),
		"delete member":      ps.DeleteMember(ctx, "", model.PrincipalUser, "m"),
		"put assignment":     ps.PutAssignment(ctx, model.RepositoryAssignment{RepoKey: "r"}),
		"assignment":         errOf(ps.Assignment(ctx, "r")),
		"assignments":        errOf(ps.Assignments(ctx, "p")),
		"delete assignment":  ps.DeleteAssignment(ctx, "r"),
	}
	for name, err := range ops {
		if err == nil || errors.Is(err, kernel.ErrNotFound) {
			t.Errorf("%s: want a driver error, got %v", name, err)
		}
	}
}
