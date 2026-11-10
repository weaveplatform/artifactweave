package sqlite_test

import (
	"flag"
	"path/filepath"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/app/adapters/sql/sqlite"
	sqlitedrv "github.com/weaveplatform/artifactweave/migration/platform/sql/sqlite"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/sqltest"
)

var update = flag.Bool("update", false, "rewrite the schema golden")

// TestAccSchemaGolden pins the schema the SQLite migrations build. A change
// to the golden is a schema change: regenerate it with -update only when a
// migration means to change the schema.
func TestAccSchemaGolden(t *testing.T) {
	s, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "schema.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	sqltest.SchemaGolden(t, s.DB(), sqlitedrv.Dialect.Name,
		filepath.Join("testdata", "schema.golden"), *update)
}
