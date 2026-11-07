// Bridge (P9d): declared in migration/platform/sql/postgres; PRO-02 deletes it.

package postgres

import pgdrv "github.com/weaveplatform/artifactweave/migration/platform/sql/postgres"

var (
	Dialect           = pgdrv.Dialect
	ErrDSNRequired    = pgdrv.ErrDSNRequired
	IsUniqueViolation = pgdrv.IsUniqueViolation
)
