package sql_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	accsql "github.com/weaveplatform/artifactweave/internal/access/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/access/adapters/storetest"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

func TestAccProjectRolePageContract(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{accsql.Module}, accsql.New,
		func(t *testing.T, newStore func(*testing.T) *accsql.Store) {
			storetest.RunProjectRolePages(t, func(t *testing.T) port.Store { return newStore(t) })
		}, sqltest.WithPostgres())
}

func TestAccProjectRolePageLegacyEmptyRoles(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{accsql.Module}, accsql.New,
		func(t *testing.T, newStore func(*testing.T) *accsql.Store) {
			db, ctx := newStore(t), t.Context()
			for i, raw := range []string{"", "null", " null ", "[]", `["Viewer"]`} {
				key := fmt.Sprintf("p%d", i)
				must(t, db.Projects().Create(ctx, model.Project{Key: key, DisplayName: key}))
				must(t, db.Projects().PutMember(ctx, model.ProjectMember{ProjectKey: key, Type: model.PrincipalUser, Name: "alice"}))
				_, err := db.Exec(ctx, "seed legacy roles", "UPDATE project_members SET roles = ? WHERE project_key = ?", raw, key)
				must(t, err)
			}
			rows, more, err := db.Projects().RoleProjects(ctx, port.ProjectRoleQuery{RoleName: "Viewer", Limit: 10})
			must(t, err)
			if len(rows) != 1 || rows[0].Key != "p4" || more {
				t.Fatalf("legacy empty roles changed project usage: %+v, more=%t", rows, more)
			}
		}, sqltest.WithPostgres())
}

func TestAccProjectRolePageIndexedSeeks(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{accsql.Module}, accsql.New,
		func(t *testing.T, newStore func(*testing.T) *accsql.Store) {
			db, ctx := newStore(t), t.Context()
			must(t, db.Tx(ctx, func(ctx context.Context) error {
				for i := range listScale {
					key := fmt.Sprintf("p%05d", i)
					if err := db.Projects().Create(ctx, model.Project{Key: key, DisplayName: fmt.Sprintf("Project %05d", i)}); err != nil {
						return err
					}
					if err := db.Projects().PutMember(ctx, model.ProjectMember{ProjectKey: key, Type: model.PrincipalUser, Name: "alice", Roles: []string{"Viewer"}}); err != nil {
						return err
					}
				}
				return nil
			}))
			sqltest.Exec(t, db.DB(), "ANALYZE")
			for _, descending := range []bool{false, true} {
				for _, late := range []bool{false, true} {
					q := port.ProjectRoleQuery{RoleName: "Viewer", Descending: descending, Limit: 10}
					want := "p00000"
					if descending {
						want = "p04999"
					}
					if late {
						q.AfterName, q.AfterKey = "Project 04000", "p04000"
						want = "p04001"
						if descending {
							want = "p03999"
						}
					}
					rows, more, err := db.Projects().RoleProjects(ctx, q)
					must(t, err)
					if len(rows) != 10 || !more || rows[0].Key != want {
						t.Fatalf("role page after %q descending=%t: %+v, more=%t", q.AfterKey, descending, rows, more)
					}
					query, args := accsql.ProjectRolePageQuery(q, db.Dialect().Dollar)
					plan := explainAccess(t, db, query, args...)
					t.Logf("late=%t descending=%t:\n%s", late, descending, plan)
					if !strings.Contains(plan, "projects_by_name") {
						t.Fatalf("project page does not use its ordering index:\n%s", plan)
					}
					if late && !strings.Contains(strings.ToLower(plan), "index cond") && !strings.Contains(plan, "SEARCH p USING INDEX") {
						t.Fatalf("late role page scans without an indexed name/key seek:\n%s", plan)
					}
				}
			}
		}, sqltest.WithPostgres())
}
