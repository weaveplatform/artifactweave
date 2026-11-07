// Bridge (P9d): declared in migration/platform/sql/sqlite; PRO-02 deletes it.

package sqlite

import sqlitedrv "github.com/weaveplatform/artifactweave/migration/platform/sql/sqlite"

const Memory = sqlitedrv.Memory

var (
	Dialect           = sqlitedrv.Dialect
	ErrPathRequired   = sqlitedrv.ErrPathRequired
	IsUniqueViolation = sqlitedrv.IsUniqueViolation
)

// DSN is the driver's DSN with o's busy timeout. It wraps rather than
// aliases the driver's function, because this package's Options, which
// embeds the store's options, is not the driver's.
func DSN(path string, o Options) string {
	return sqlitedrv.DSN(path, sqlitedrv.Options{BusyTimeout: o.BusyTimeout})
}
