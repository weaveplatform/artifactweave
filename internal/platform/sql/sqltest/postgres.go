package sqltest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/weaveplatform/artifactweave/internal/platform/sql/postgres"
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

// PostgresDSN returns the DSN of the PostgreSQL server that every test of the
// process shares: ARTIFACTWEAVE_TEST_POSTGRES_DSN when it is set, or else that
// of a postgres:17-alpine container, database artifactweave with user and
// password aw, which the first call starts and later calls reuse. Without the
// variable it skips t in a -short run and where Docker cannot run Linux
// containers, and it fails t when the container does not start; a failed
// start is not retried, so every later call fails too. Tables a test creates
// in the server's public schema are seen by every other test of the process;
// ScratchDSN gives a test a schema of its own.
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

// ScratchDSN creates an empty schema of its own on the server that dsn
// names and returns dsn with that schema as its search_path. pgx passes an
// unknown DSN parameter such as search_path to the server as a run-time
// parameter, so every connection of a pool opened with the result starts in
// the schema: unqualified tables are created and read there, and
// current_schema(), which the migrator's legacy-history check reads, names
// it. dsn is a postgres:// or postgresql:// URL or key=value pairs. The
// schema is dropped when t ends, after the cleanups registered later, such
// as closing a pool opened with the result, because one server serves every
// test of the process and may serve other processes too.
func ScratchDSN(t *testing.T, dsn string) string {
	t.Helper()
	admin := postgresDB(t, dsn)
	name := "scratch_" + strings.ToLower(rand.Text())
	if _, err := admin.ExecContext(t.Context(), "CREATE SCHEMA "+name); err != nil {
		t.Fatalf("create schema %s: %v", name, err)
	}
	t.Cleanup(func() {
		// Registered after admin's own cleanup, so it runs before admin closes;
		// t's context is already cancelled here.
		_, err := admin.ExecContext(context.Background(), "DROP SCHEMA "+name+" CASCADE")
		if err != nil {
			t.Errorf("drop schema %s: %v", name, err)
		}
	})
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		return dsn + " search_path=" + name
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", name)
	u.RawQuery = q.Encode()
	return u.String()
}

func postgresDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := postgres.OpenDB(dsn, postgres.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
