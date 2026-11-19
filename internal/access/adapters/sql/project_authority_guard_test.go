package sql_test

import (
	"context"
	"testing"

	accsql "github.com/weaveplatform/artifactweave/internal/access/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func authorityReadError[V any](_ V, err error) error { return err }

func TestAccProjectAuthorityGuardRequired(t *testing.T) {
	cases := []struct {
		name string
		op   func(context.Context, port.ProjectStore) error
	}{
		{"authority", func(ctx context.Context, ps port.ProjectStore) error {
			return authorityReadError(ps.Authority(ctx, "p1"))
		}},
		{"sharing authority", func(ctx context.Context, ps port.ProjectStore) error {
			return authorityReadError(ps.SharingAuthority(ctx, "repo-a"))
		}},
		{"get", func(ctx context.Context, ps port.ProjectStore) error { return authorityReadError(ps.Get(ctx, "p1")) }},
		{"list", func(ctx context.Context, ps port.ProjectStore) error { return authorityReadError(ps.List(ctx)) }},
		{"role", func(ctx context.Context, ps port.ProjectStore) error {
			return authorityReadError(ps.Role(ctx, "p1", "qa"))
		}},
		{"roles", func(ctx context.Context, ps port.ProjectStore) error { return authorityReadError(ps.Roles(ctx, "p1")) }},
		{"environments", func(ctx context.Context, ps port.ProjectStore) error {
			return authorityReadError(ps.Environments(ctx, "p1"))
		}},
		{"member", func(ctx context.Context, ps port.ProjectStore) error {
			return authorityReadError(ps.Member(ctx, "p1", model.PrincipalUser, "ann"))
		}},
		{"members", func(ctx context.Context, ps port.ProjectStore) error {
			return authorityReadError(ps.Members(ctx, "p1", model.PrincipalUser))
		}},
		{"assignment", func(ctx context.Context, ps port.ProjectStore) error {
			return authorityReadError(ps.Assignment(ctx, "repo-a"))
		}},
		{"assignments", func(ctx context.Context, ps port.ProjectStore) error {
			return authorityReadError(ps.Assignments(ctx, "p1"))
		}},
		{"create", func(ctx context.Context, ps port.ProjectStore) error {
			return ps.Create(ctx, model.Project{Key: "new"})
		}},
		{"update", func(ctx context.Context, ps port.ProjectStore) error {
			return ps.Update(ctx, model.Project{Key: "p1", Description: "changed"})
		}},
		{"delete", func(ctx context.Context, ps port.ProjectStore) error { return ps.Delete(ctx, "p1") }},
		{"put role", func(ctx context.Context, ps port.ProjectStore) error {
			return ps.PutRole(ctx, model.Role{ProjectKey: "p1", Name: "new"})
		}},
		{"delete role", func(ctx context.Context, ps port.ProjectStore) error { return ps.DeleteRole(ctx, "p1", "qa") }},
		{"create environment", func(ctx context.Context, ps port.ProjectStore) error {
			return ps.CreateEnvironment(ctx, model.Environment{ProjectKey: "p1", Name: "new"})
		}},
		{"rename environment", func(ctx context.Context, ps port.ProjectStore) error {
			return ps.RenameEnvironment(ctx, "p1", "p1-E", "new")
		}},
		{"delete environment", func(ctx context.Context, ps port.ProjectStore) error { return ps.DeleteEnvironment(ctx, "p1", "p1-E") }},
		{"put member", func(ctx context.Context, ps port.ProjectStore) error {
			return ps.PutMember(ctx, model.ProjectMember{ProjectKey: "p1", Type: model.PrincipalUser, Name: "new"})
		}},
		{"delete member", func(ctx context.Context, ps port.ProjectStore) error {
			return ps.DeleteMember(ctx, "p1", model.PrincipalUser, "ann")
		}},
		{"put assignment", func(ctx context.Context, ps port.ProjectStore) error {
			return ps.PutAssignment(ctx, model.RepositoryAssignment{RepoKey: "repo-a", ProjectKey: "p1"})
		}},
		{"delete assignment", func(ctx context.Context, ps port.ProjectStore) error { return ps.DeleteAssignment(ctx, "repo-a") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := projectStore(t)
			before := projectValueSnapshot(t, s)
			sqltest.Exec(t, s.DB(), "DELETE FROM project_authority_guard")
			err := s.Tx(t.Context(), func(ctx context.Context) error { return tc.op(ctx, s.Projects()) })
			storekit.WantErr(t, err, kernel.ErrStore)
			storekit.Same(t, "missing guard leaves project values and authority unchanged", before, projectValueSnapshot(t, s))
		})
	}
	t.Run("startup backfill", func(t *testing.T) {
		s := projectStore(t)
		before := projectValueSnapshot(t, s)
		sqltest.Exec(t, s.DB(), "DELETE FROM project_authority_guard")
		storekit.WantErr(t, accsql.Module.AfterMigrate(t.Context(), s.Store), kernel.ErrStore)
		storekit.Same(t, "missing guard cannot silently bootstrap", before, projectValueSnapshot(t, s))
	})
}

func TestAccProjectAuthorityGuardSingleton(t *testing.T) {
	for _, id := range []struct {
		name  string
		value any
	}{{"null", nil}, {"another row", int64(2)}} {
		t.Run(id.name, func(t *testing.T) {
			s := projectStore(t)
			_, err := s.Exec(t.Context(), "insert invalid authority guard", "INSERT INTO project_authority_guard (id) VALUES (?)", id.value)
			if err == nil {
				t.Fatal("project authority guard accepted another row")
			}
			var count int
			storekit.Must(t, s.DB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM project_authority_guard WHERE id = 1").Scan(&count))
			if count != 1 {
				t.Fatal("guard singleton changed after refused insert")
			}
		})
	}
}
