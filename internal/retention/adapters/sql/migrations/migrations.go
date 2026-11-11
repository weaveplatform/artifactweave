package migrations

import "embed"

// FS holds the stream's migration files under sqlite/ and postgres/, the
// directories the migrator reads for the dialect of that name. The retention
// module's registration with the SQL layer passes it as its Migrations.
//
//go:embed sqlite/*.sql postgres/*.sql
var FS embed.FS
