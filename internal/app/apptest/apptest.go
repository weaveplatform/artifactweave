package apptest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/sql/postgres"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/sql/sqlite"
	appstoretest "github.com/weaveplatform/artifactweave/internal/app/storetest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/formattest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	platsqltest "github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

// FormatBackends returns the stores the format contract runs on, in this
// order: the in-memory composite ("inmem"), the SQLite composite in a file
// of the test's temporary directory ("sqlite") and the PostgreSQL composite
// on the server the test binary shares ("postgres"), emptied by Reset
// first. The PostgreSQL backend skips in -short runs and where Docker
// cannot run Linux containers, unless ARTIFACTWEAVE_TEST_POSTGRES_DSN
// names a server. Each Open fails its test when the store cannot be
// opened, and closes the store when the test ends.
func FormatBackends() []formattest.Backend {
	return []formattest.Backend{
		{Name: "inmem", Open: func(*testing.T) item.Store { return inmem.New() }},
		{Name: "sqlite", Open: func(t *testing.T) item.Store {
			st, err := sqlite.Open(context.Background(),
				filepath.Join(t.TempDir(), "aw.db"), sqlite.Options{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			return st
		}},
		{Name: "postgres", Open: func(t *testing.T) item.Store {
			dsn := platsqltest.PostgresDSN(t)
			st, err := postgres.Open(context.Background(), dsn, postgres.Options{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			// The test binary shares one server, so each scenario starts
			// empty.
			appstoretest.Reset(t, st.DB(), st.Dialect())
			return st
		}},
	}
}
