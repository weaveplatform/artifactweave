package sql_test

import (
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

// A test file holds the check so that no production package imports the SQL
// store for it.
var _ tx.Runner = (*appsql.Store)(nil)
