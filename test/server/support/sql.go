package support

import (
	"testing"

	accsql "github.com/weaveplatform/artifactweave/internal/access/adapters/sql"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	artsql "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

// AccessArtifactsSQLStore is the real composite store used by cross-module
// access/artifact transaction tests through this permitted test-support surface.
type AccessArtifactsSQLStore = appsql.Store

// RunAccessArtifactsSQL applies only the access/artifacts streams and runs a
// suite on SQLite memory/file and PostgreSQL. Each factory call gets its own
// database or scratch schema, as sqltest.RunModule documents. The suite must
// use only those two stores; other migration streams are not applied.
func RunAccessArtifactsSQL(t *testing.T, suite func(*testing.T, func(*testing.T) *AccessArtifactsSQLStore)) {
	t.Helper()
	sqltest.RunModule(t, []dialect.Module{accsql.Module, artsql.Module}, func(db *dialect.Store) *AccessArtifactsSQLStore {
		return appsql.New(db.DB(), db.Dialect(), nil)
	}, suite, sqltest.WithPostgres())
}
