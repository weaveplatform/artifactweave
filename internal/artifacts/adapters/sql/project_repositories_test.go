package sql_test

import (
	"errors"
	"slices"
	"testing"

	artsql "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/storetest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

func TestAccProjectRepositoryContexts(t *testing.T) {
	sqltest.RunModule(t, mods, artsql.New, func(t *testing.T, newStore func(*testing.T) *artsql.Store) {
		storetest.RunProjectRepositories(t, newStore(t).Repos())
	}, sqltest.WithPostgres())
}

func TestAccProjectRepositoryContextsDoNotOpenSealedCredentials(t *testing.T) {
	sqltest.RunModule(t, mods, artsql.New, func(t *testing.T, newStore func(*testing.T) *artsql.Store) {
		db := newStore(t)
		writer := artsql.New(dialect.New(db.DB(), db.Dialect(), testKeys(t)))
		c := *repo.Defaults(&repo.Config{Key: "alpha-remote", Class: repo.Remote, ProjectKey: "alpha", Environments: []string{"DEV"}, URL: "https://private.example", Password: "sealed-value"})
		if err := writer.Repos().Create(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		reader := artsql.New(dialect.New(db.DB(), db.Dialect(), nil))
		if _, err := reader.Repos().Get(t.Context(), c.Key); !errors.Is(err, dialect.ErrNoKeyring) {
			t.Fatalf("credential control=%v", err)
		}
		all, next, err := reader.Repos().ProjectRepositories(t.Context(), "alpha", kernel.Page{})
		if err != nil || next != "" || len(all) != 1 || all[0].Key != c.Key || all[0].ProjectKey != "alpha" || !slices.Equal(all[0].Environments, []string{"DEV"}) {
			t.Fatalf("secret-free project context=%+v next=%q err=%v", all, next, err)
		}
		one, err := reader.Repos().ProjectRepository(t.Context(), c.Key)
		if err != nil || one.Key != c.Key || one.ProjectKey != "alpha" || one.Class != repo.Remote || !slices.Equal(one.Environments, []string{"DEV"}) {
			t.Fatalf("secret-free single context=%+v err=%v", one, err)
		}
	}, sqltest.WithPostgres())
}
