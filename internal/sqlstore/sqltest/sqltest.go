// Package sqltest provides PostgreSQL for the store contract runs. It
// starts one container per test binary with testcontainers-go and skips the
// calling test where Docker is unavailable (decision 0015), so the suite
// runs everywhere and the PostgreSQL half runs in the Linux CI jobs. Set
// ARTIFACTWEAVE_TEST_POSTGRES_DSN to use an existing server instead.
package sqltest

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/weaveplatform/weaveplatform-artifact-registry/internal/sqlstore/sqlcommon"
)

// postgresImage is pinned so a PostgreSQL release cannot change behaviour
// under the suite; bump it deliberately.
const postgresImage = "postgres:17-alpine"

var (
	pgOnce sync.Once
	pgDSN  string
	pgErr  error
)

// PostgresDSN returns a DSN for the PostgreSQL server shared by the test
// binary, or skips t when there is none and Docker is unavailable.
func PostgresDSN(t *testing.T) string {
	t.Helper()
	if dsn := os.Getenv("ARTIFACTWEAVE_TEST_POSTGRES_DSN"); dsn != "" {
		return dsn
	}
	if testing.Short() {
		t.Skip("short mode")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	pgOnce.Do(func() {
		ctx := context.Background()
		c, err := tcpostgres.Run(ctx, postgresImage,
			tcpostgres.WithDatabase("artifactweave"),
			tcpostgres.WithUsername("aw"),
			tcpostgres.WithPassword("aw"),
			tcpostgres.BasicWaitStrategies(),
		)
		if err != nil {
			pgErr = err
			return
		}
		pgDSN, pgErr = c.ConnectionString(ctx, "sslmode=disable")
	})
	if pgErr != nil {
		t.Fatalf("postgres container: %v", pgErr)
	}
	return pgDSN
}

// Reset rolls every migration back and applies them again, leaving an empty
// schema. Running it before each contract test also exercises every down
// migration on every run.
func Reset(t *testing.T, db *sql.DB, d sqlcommon.Dialect) {
	t.Helper()
	ctx := context.Background()
	if _, err := sqlcommon.Rollback(ctx, db, d, 0); err != nil {
		t.Fatalf("reset rollback: %v", err)
	}
	if _, err := sqlcommon.Migrate(ctx, db, d); err != nil {
		t.Fatalf("reset migrate: %v", err)
	}
}
