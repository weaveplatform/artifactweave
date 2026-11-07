package sqltest

import (
	"context"
	"os"
	"sync"
	"testing"

	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/weaveplatform/artifactweave/pkg/containertest"
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
// binary, or skips t when there is none and Docker cannot run Linux
// containers.
func PostgresDSN(t *testing.T) string {
	t.Helper()
	if dsn := os.Getenv("ARTIFACTWEAVE_TEST_POSTGRES_DSN"); dsn != "" {
		return dsn
	}
	containertest.RequireLinux(t)
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
