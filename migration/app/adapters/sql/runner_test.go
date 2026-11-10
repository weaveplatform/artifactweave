package sql_test

import (
	appsql "github.com/weaveplatform/artifactweave/migration/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/migration/platform/tx"
)

// A test file holds the check so that no production package imports the SQL
// store for it.
var _ tx.Runner = (*appsql.Store)(nil)
