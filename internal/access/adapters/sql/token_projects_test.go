package sql_test

import (
	"errors"
	"io/fs"
	"maps"
	"strings"
	"testing"
	"testing/fstest"

	accsql "github.com/weaveplatform/artifactweave/internal/access/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/access/adapters/sql/migrations"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func TestAccTokenProjectLifetimesUpgradeDoesNotBindOldScopes(t *testing.T) {
	before := accsql.Module
	files := fstest.MapFS{}
	for _, driver := range []string{"sqlite", "postgres"} {
		names, err := fs.Glob(migrations.FS, driver+"/*.sql")
		storekit.Must(t, err)
		for _, name := range names {
			if strings.Contains(name, "/0005_") {
				continue
			}
			data, err := migrations.FS.ReadFile(name)
			storekit.Must(t, err)
			files[name] = &fstest.MapFile{Data: data}
		}
	}
	before.Migrations = files
	sqltest.RunModule(t, []dialect.Module{before}, accsql.New,
		func(t *testing.T, newDB func(*testing.T) *accsql.Store) {
			st, ctx := newDB(t), t.Context()
			storekit.Must(t, st.Projects().Create(ctx, model.Project{Key: "alpha"}))
			for _, old := range []struct{ id, scope, key string }{
				{"ordinary", "applied-permissions/user", ""},
				{"boundary", "applied-permissions/user", "alpha"},
				{"role", "applied-permissions/roles:alpha:Viewer", ""},
				{"definitions", "project:alpha/roles:r", ""},
			} {
				_, err := st.Exec(ctx, "seed pre-binding token",
					"INSERT INTO tokens (id, scope, project_key) VALUES (?, ?, ?)", old.id, old.scope, old.key)
				storekit.Must(t, err)
			}
			_, err := dialect.Migrate(ctx, st.DB(), st.Dialect(), []dialect.Module{accsql.Module}, "access")
			storekit.Must(t, err)
			for _, id := range []string{"ordinary", "boundary", "role", "definitions"} {
				record, err := st.Tokens().Get(ctx, id)
				storekit.Must(t, err)
				if len(record.ProjectIncarnations) != 0 {
					t.Fatal("migration manufactured a current lifetime for an old delegation")
				}
				current, err := project.TokenCurrent(ctx, st.Projects(), record)
				storekit.Must(t, err)
				if current != (id == "ordinary") {
					t.Fatalf("upgraded %s current=%t", id, current)
				}
			}
			_, err = st.Exec(ctx, "corrupt binding document", "UPDATE tokens SET project_incarnations = ? WHERE id = ?", "not-json", "role")
			storekit.Must(t, err)
			if _, err := st.Tokens().Get(ctx, "role"); !errors.Is(err, kernel.ErrStore) {
				t.Fatalf("corrupt binding document was accepted: %v", err)
			}
			authority, err := st.Projects().Authority(ctx, "alpha")
			storekit.Must(t, err)
			bound := model.Token{
				ID: "new-bound", ProjectKey: "alpha", AccessDigest: []byte("binding-proof"),
				ProjectIncarnations: map[string]string{"alpha": string(authority.Incarnation)},
			}
			storekit.Must(t, st.Tokens().Create(ctx, bound))
			read, err := st.Tokens().Get(ctx, bound.ID)
			storekit.Must(t, err)
			if !maps.Equal(read.ProjectIncarnations, bound.ProjectIncarnations) {
				t.Fatal("stored project lifetime did not round-trip")
			}
			read.ProjectIncarnations["alpha"] = "mutated"
			byDigest, err := st.Tokens().ByDigest(ctx, port.DigestAccess, bound.AccessDigest)
			storekit.Must(t, err)
			if !maps.Equal(byDigest.ProjectIncarnations, bound.ProjectIncarnations) {
				t.Fatal("mutating a token result changed the stored binding")
			}
			current, err := project.TokenCurrent(ctx, st.Projects(), byDigest)
			storekit.Must(t, err)
			if !current {
				t.Fatal("current bound token was refused")
			}
			storekit.Must(t, st.Projects().Delete(ctx, "alpha"))
			storekit.Must(t, st.Projects().Create(ctx, model.Project{Key: "alpha"}))
			current, err = project.TokenCurrent(ctx, st.Projects(), byDigest)
			storekit.Must(t, err)
			if current {
				t.Fatal("SQL project recreation revived a stored delegation")
			}
		}, sqltest.WithPostgres())
}
