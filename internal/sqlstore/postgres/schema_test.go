package postgres_test

import (
	"flag"
	"path/filepath"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/sqlstore/postgres"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqltest"
)

var update = flag.Bool("update", false, "rewrite the schema golden")

// TestAccSchemaGolden pins the schema the PostgreSQL migrations build. A
// change to the golden is a schema change: regenerate it with -update only
// when a migration means to change the schema, against the server version
// that postgresImage in sqltest pins.
func TestAccSchemaGolden(t *testing.T) {
	s, err := postgres.Open(t.Context(), sqltest.PostgresDSN(t), postgres.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	// Earlier tests of the binary leave rows behind on the shared server.
	sqltest.Reset(t, s.DB(), postgres.Dialect)
	sqltest.SchemaGolden(t, s.DB(), postgres.Dialect.Name,
		filepath.Join("testdata", "schema.golden"), *update)
}
