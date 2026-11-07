package sqltest

import (
	"context"
	"database/sql"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
)

// Reset rolls every stream of mods back to zero and migrates them again,
// leaving their tables empty; tables of other streams are untouched.
// Running it before each test also exercises every down migration of mods.
func Reset(t *testing.T, db *sql.DB, d dialect.Dialect, mods []dialect.Module) {
	t.Helper()
	ctx := context.Background()
	if _, err := dialect.Rollback(ctx, db, d, mods, "", 0); err != nil {
		t.Fatalf("reset rollback: %v", err)
	}
	if _, err := dialect.Migrate(ctx, db, d, mods, ""); err != nil {
		t.Fatalf("reset migrate: %v", err)
	}
}
