package sqlcommon_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	"github.com/weaveplatform/artifactweave/migration/platform/crypt"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// opsFail makes every row-level event (INSERT, UPDATE or DELETE) on a table
// fail, so a test reaches the statement after a successful one.
func opsFail(event, table string) string {
	return "CREATE TRIGGER ops_fail_" + strings.ToLower(event) + "_" + table + " BEFORE " + event +
		" ON " + table + " BEGIN SELECT RAISE(ABORT, 'injected'); END"
}

// opsDrop removes a table so the statements that touch it fail.
func opsDrop(table string) string { return "DROP TABLE " + table }

// opsStore opens a sealed SQLite store at a fresh path, loaded with one of
// every project record.
func opsStore(t *testing.T) *sqlcommon.Store {
	t.Helper()
	ctx := context.Background()
	k, err := crypt.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "aw.db")
	s, err := sqlite.Open(ctx, path, sqlite.Options{Options: sqlcommon.Options{Keyring: k}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ps := s.Projects()
	for _, err := range []error{
		ps.Create(ctx, types.Project{Key: "p1"}),
		ps.Create(ctx, types.Project{Key: "p2"}),
		ps.PutRole(ctx, types.Role{ProjectKey: "p1", Name: "qa", Environments: []string{"p1-E"}}),
		ps.CreateEnvironment(ctx, types.Environment{ProjectKey: "p1", Name: "p1-E"}),
		ps.PutMember(ctx, types.ProjectMember{ProjectKey: "p1", Type: types.PrincipalUser, Name: "ann"}),
		ps.PutAssignment(ctx, types.RepositoryAssignment{
			RepoKey: "repo-a", ProjectKey: "p1", Environments: []string{"p1-E"}, SharedWith: []string{"p2"},
		}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// TestAccOperationsFaults injects a failure into the statement after a
// successful one and checks every multi-statement write and read reports
// it instead of half-applying or swallowing it.
func TestAccOperationsFaults(t *testing.T) {
	ps := func(s store.Store) store.ProjectStore { return s.Projects() }
	cases := []struct {
		name   string
		inject []string
		op     func(context.Context, store.Store) error
	}{
		{
			"project delete",
			[]string{opsDrop("roles")},
			func(ctx context.Context, s store.Store) error {
				return ps(s).Delete(ctx, "p1")
			},
		},
		{"put role", []string{opsDrop("roles")}, func(ctx context.Context, s store.Store) error {
			return ps(s).PutRole(ctx, types.Role{ProjectKey: "p1", Name: "x"})
		}},
		{
			"create environment",
			[]string{opsFail("INSERT", "environments")},
			func(ctx context.Context, s store.Store) error {
				return ps(
					s,
				).CreateEnvironment(ctx, types.Environment{ProjectKey: "p1", Name: "p1-F"})
			},
		},
		{
			"rename environment roles read",
			[]string{opsDrop("roles")},
			func(ctx context.Context, s store.Store) error {
				return ps(s).RenameEnvironment(ctx, "p1", "p1-E", "p1-G")
			},
		},
		{
			"rename environment role update",
			[]string{opsFail("UPDATE", "roles")},
			func(ctx context.Context, s store.Store) error {
				return ps(s).RenameEnvironment(ctx, "p1", "p1-E", "p1-G")
			},
		},
		{
			"rename environment assignments read",
			[]string{opsDrop("project_repository_shares")},
			func(ctx context.Context, s store.Store) error {
				return ps(s).RenameEnvironment(ctx, "p1", "p1-E", "p1-G")
			},
		},
		{
			"rename environment assignment update",
			[]string{opsFail("UPDATE", "project_repositories")},
			func(ctx context.Context, s store.Store) error {
				return ps(s).RenameEnvironment(ctx, "p1", "p1-E", "p1-G")
			},
		},
		{
			"put member",
			[]string{opsFail("INSERT", "project_members")},
			func(ctx context.Context, s store.Store) error {
				return ps(
					s,
				).PutMember(ctx, types.ProjectMember{ProjectKey: "p1", Type: types.PrincipalGroup, Name: "g"})
			},
		},
		{
			"put assignment",
			[]string{opsFail("INSERT", "project_repositories")},
			func(ctx context.Context, s store.Store) error {
				return ps(
					s,
				).PutAssignment(ctx, types.RepositoryAssignment{RepoKey: "repo-b", ProjectKey: "p1"})
			},
		},
		{
			"put assignment clear shares",
			[]string{opsFail("DELETE", "project_repository_shares")},
			func(ctx context.Context, s store.Store) error {
				return ps(
					s,
				).PutAssignment(ctx, types.RepositoryAssignment{RepoKey: "repo-a", ProjectKey: "p1"})
			},
		},
		{
			"put assignment share",
			[]string{opsFail("INSERT", "project_repository_shares")},
			func(ctx context.Context, s store.Store) error {
				return ps(s).PutAssignment(ctx,
					types.RepositoryAssignment{
						RepoKey:    "repo-b",
						ProjectKey: "p1",
						SharedWith: []string{"p2"},
					})
			},
		},
		{
			"get assignment shares",
			[]string{opsDrop("project_repository_shares")},
			func(ctx context.Context, s store.Store) error {
				_, err := ps(s).Assignment(ctx, "repo-a")
				return err
			},
		},
		{
			"delete assignment shares",
			[]string{opsDrop("project_repository_shares")},
			func(ctx context.Context, s store.Store) error { return ps(s).DeleteAssignment(ctx, "repo-a") },
		},
		{
			"delete assignment",
			[]string{opsFail("DELETE", "project_repositories")},
			func(ctx context.Context, s store.Store) error { return ps(s).DeleteAssignment(ctx, "repo-a") },
		},
		{
			"corrupt role document",
			[]string{"UPDATE roles SET environments = 'not json'"},
			func(ctx context.Context, s store.Store) error {
				_, err := ps(s).Role(ctx, "p1", "qa")
				return err
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			s := opsStore(t)
			for _, stmt := range c.inject {
				if _, err := s.DB().ExecContext(ctx, stmt); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.op(ctx, s); err == nil || errors.Is(err, store.ErrNotFound) {
				t.Fatalf("want an injected failure, got %v", err)
			}
		})
	}
}

// TestAccOperationsUnmigrated runs every operations method against a database
// without the schema: each must surface the driver error, never a nil error
// or a misleading ErrNotFound.
func TestAccOperationsUnmigrated(t *testing.T) {
	ctx := context.Background()
	s, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "raw.db"),
		sqlite.Options{Options: sqlcommon.Options{SkipMigrate: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ps := s.Projects()
	errOf := func(_ any, err error) error { return err }
	ops := map[string]error{
		"project create":     ps.Create(ctx, types.Project{Key: "p"}),
		"project get":        errOf(ps.Get(ctx, "p")),
		"project update":     ps.Update(ctx, types.Project{Key: "p"}),
		"project delete":     ps.Delete(ctx, "p"),
		"project list":       errOf(ps.List(ctx)),
		"put role":           ps.PutRole(ctx, types.Role{Name: "r"}),
		"put project role":   ps.PutRole(ctx, types.Role{ProjectKey: "p", Name: "r"}),
		"role":               errOf(ps.Role(ctx, "", "r")),
		"roles":              errOf(ps.Roles(ctx, "")),
		"delete role":        ps.DeleteRole(ctx, "", "r"),
		"create environment": ps.CreateEnvironment(ctx, types.Environment{Name: "E"}),
		"environments":       errOf(ps.Environments(ctx, "")),
		"rename environment": ps.RenameEnvironment(ctx, "", "E", "F"),
		"delete environment": ps.DeleteEnvironment(ctx, "", "E"),
		"put member":         ps.PutMember(ctx, types.ProjectMember{Name: "m"}),
		"member":             errOf(ps.Member(ctx, "", types.PrincipalUser, "m")),
		"members":            errOf(ps.Members(ctx, "", types.PrincipalUser)),
		"delete member":      ps.DeleteMember(ctx, "", types.PrincipalUser, "m"),
		"put assignment":     ps.PutAssignment(ctx, types.RepositoryAssignment{RepoKey: "r"}),
		"assignment":         errOf(ps.Assignment(ctx, "r")),
		"assignments":        errOf(ps.Assignments(ctx, "p")),
		"delete assignment":  ps.DeleteAssignment(ctx, "r"),
	}
	for name, err := range ops {
		if err == nil || errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: want a driver error, got %v", name, err)
		}
	}
}
