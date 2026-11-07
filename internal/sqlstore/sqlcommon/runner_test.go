package sqlcommon_test

import (
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/migration/platform/tx"
)

// A test file holds the check so that no production package imports the SQL
// store for it. The store.Store assertion in store.go implies this one only
// while the aggregate embeds tx.Runner; this one outlasts the aggregate.
var _ tx.Runner = (*sqlcommon.Store)(nil)
