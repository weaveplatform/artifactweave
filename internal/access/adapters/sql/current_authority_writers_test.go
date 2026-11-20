package sql_test

import (
	"context"
	"testing"
	"time"

	accsql "github.com/weaveplatform/artifactweave/internal/access/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func TestAccCurrentAuthorityWritersRequireGuard(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{accsql.Module}, accsql.New,
		func(t *testing.T, newStore func(*testing.T) *accsql.Store) {
			st, ctx := newStore(t), t.Context()
			now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
			user := model.User{Name: "ann", Groups: []string{"readers"}, Status: model.UserEnabled}
			group := model.Group{Name: "readers"}
			permission := model.Permission{Name: "read"}
			scope := storekit.Get[kernel.Scope](t)(kernel.ParseScope("applied-permissions/user"))
			token := model.Token{ID: "token", Subject: "jfac@test/users/ann", Scope: scope, ExpiresAt: now.Add(-time.Hour)}
			storekit.Must(t, st.Groups().Create(ctx, group))
			storekit.Must(t, st.Users().Create(ctx, user))
			storekit.Must(t, st.Permissions().Create(ctx, permission))
			storekit.Must(t, st.Tokens().Create(ctx, token))
			user = storekit.Get[model.User](t)(st.Users().Get(ctx, user.Name))
			group = storekit.Get[model.Group](t)(st.Groups().Get(ctx, group.Name))
			permission = storekit.Get[model.Permission](t)(st.Permissions().Get(ctx, permission.Name))
			token = storekit.Get[model.Token](t)(st.Tokens().Get(ctx, token.ID))
			sqltest.Exec(t, st.DB(), "DELETE FROM project_authority_guard")
			operations := map[string]func(context.Context) error{
				"create user": func(ctx context.Context) error { return st.Users().Create(ctx, model.User{Name: "other"}) },
				"update user": func(ctx context.Context) error {
					changed := user
					changed.Admin = true
					return st.Users().Update(ctx, changed)
				},
				"delete user":  func(ctx context.Context) error { return st.Users().Delete(ctx, user.Name) },
				"record login": func(ctx context.Context) error { return st.Users().RecordLogin(ctx, user.Name, now) },
				"failed login": func(ctx context.Context) error { _, err := st.Users().RecordFailedLogin(ctx, user.Name, 1); return err },
				"unlock":       func(ctx context.Context) error { return st.Users().Unlock(ctx, user.Name) },
				"create group": func(ctx context.Context) error { return st.Groups().Create(ctx, model.Group{Name: "other"}) },
				"update group": func(ctx context.Context) error {
					changed := group
					changed.AdminPrivileges = true
					return st.Groups().Update(ctx, changed)
				},
				"delete group":          func(ctx context.Context) error { return st.Groups().Delete(ctx, group.Name) },
				"add members":           func(ctx context.Context) error { return st.Groups().AddMembers(ctx, group.Name, user.Name) },
				"remove members":        func(ctx context.Context) error { return st.Groups().RemoveMembers(ctx, group.Name, user.Name) },
				"create permission":     func(ctx context.Context) error { return st.Permissions().Create(ctx, model.Permission{Name: "other"}) },
				"update permission":     func(ctx context.Context) error { return st.Permissions().Update(ctx, permission) },
				"delete permission":     func(ctx context.Context) error { return st.Permissions().Delete(ctx, permission.Name) },
				"revoke token":          func(ctx context.Context) error { return st.Tokens().Delete(ctx, token.ID) },
				"delete expired tokens": func(ctx context.Context) error { _, err := st.Tokens().DeleteExpired(ctx, now); return err },
				"API restriction": func(ctx context.Context) error {
					_, err := st.APIRestrictions().Put(ctx, port.APIRestriction{User: user.Name, Actor: "admin", Reason: "test", Changed: now, Until: now.Add(time.Hour)})
					return err
				},
			}
			for name, op := range operations {
				t.Run(name, func(t *testing.T) { storekit.WantErr(t, op(ctx), kernel.ErrStore) })
			}
			storekit.Same(t, "user unchanged", user, storekit.Get[model.User](t)(st.Users().Get(ctx, user.Name)))
			storekit.Same(t, "group unchanged", group, storekit.Get[model.Group](t)(st.Groups().Get(ctx, group.Name)))
			storekit.Same(t, "permission unchanged", permission, storekit.Get[model.Permission](t)(st.Permissions().Get(ctx, permission.Name)))
			storekit.Same(t, "token unchanged", token, storekit.Get[model.Token](t)(st.Tokens().Get(ctx, token.ID)))
			for _, table := range []string{"api_restrictions", "api_restriction_audit"} {
				var count int
				storekit.Must(t, st.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count))
				if count != 0 {
					t.Fatalf("refused restriction left %d rows in %s", count, table)
				}
			}
		}, sqltest.WithPostgres())
}
