package port_test

import (
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/migration/access/core/port"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

// The metadata store's in-memory store and its SQL store, the composite the
// SQLite and PostgreSQL stores embed, must satisfy Store, so the access
// module can take either as its own until its adapters move (STO-16). The
// assertions live here rather than beside those stores, as the other
// modules' do, because a test outside access may not import access core
// (TestUnitTestsAvoidAccessInternals).
var (
	_ port.Store = (*inmem.Store)(nil)
	_ port.Store = (*sqlcommon.Store)(nil)
)
