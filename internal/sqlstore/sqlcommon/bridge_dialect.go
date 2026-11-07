// Bridge (P9d): declared in migration/platform/sql/dialect; PRO-02 deletes it.

package sqlcommon

import "github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"

type (
	Dialect       = dialect.Dialect
	Queryer       = dialect.Queryer
	Migration     = dialect.Migration
	Applied       = dialect.Applied
	StreamVersion = dialect.StreamVersion
)

var (
	ErrTransaction    = dialect.ErrTransaction
	ErrMigration      = dialect.ErrMigration
	ErrNoKeyring      = dialect.ErrNoKeyring
	ErrLegacyHistory  = dialect.ErrLegacyHistory
	ErrUnknownModule  = dialect.ErrUnknownModule
	ErrModuleRequired = dialect.ErrModuleRequired
)

var (
	Upsert         = dialect.Upsert
	InsertIgnore   = dialect.InsertIgnore
	Insert         = dialect.Insert
	Placeholders   = dialect.Placeholders
	LoadMigrations = dialect.LoadMigrations
	Migrate        = dialect.Migrate
	Rollback       = dialect.Rollback
	Version        = dialect.Version
	MustSub        = dialect.MustSub
)
