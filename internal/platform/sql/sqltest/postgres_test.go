package sqltest_test

import (
	"database/sql"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/sql/postgres"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

func openPostgres(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := postgres.OpenDB(dsn, postgres.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// keyValue rewrites a postgres:// URL in the key=value form of a DSN.
func keyValue(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	kv := []string{
		"host=" + u.Hostname(), "port=" + u.Port(), "user=" + u.User.Username(),
		"password=" + password, "dbname=" + strings.TrimPrefix(u.Path, "/"),
	}
	for k, vs := range u.Query() {
		kv = append(kv, k+"="+vs[0])
	}
	return strings.Join(kv, " ")
}

// TestAccPostgresDSN checks that every call in the process returns the one
// shared server, which answers.
func TestAccPostgresDSN(t *testing.T) {
	dsn := sqltest.PostgresDSN(t)
	if again := sqltest.PostgresDSN(t); again != dsn {
		t.Fatalf("a second call returned %q; want the shared server %q", again, dsn)
	}
	if err := openPostgres(t, dsn).PingContext(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestAccScratchDSN checks, for a DSN in URL and in key=value form, that
// each call gives an empty schema of its own, which every connection of a
// pool opened with the result starts in, and that the schema is gone once
// the test that asked for it has ended.
func TestAccScratchDSN(t *testing.T) {
	dsn := sqltest.PostgresDSN(t)
	admin := openPostgres(t, dsn)
	forms := []struct{ name, dsn string }{{"url", dsn}, {"key-value", keyValue(t, dsn)}}
	for _, form := range forms {
		var schemas []string
		t.Run(form.name, func(t *testing.T) {
			ctx := t.Context()
			for range 2 {
				db := openPostgres(t, sqltest.ScratchDSN(t, form.dsn))
				var seen []string
				for range 2 {
					// Both connections stay held, so the pool opens a second.
					c, err := db.Conn(ctx)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = c.Close() }()
					var schema string
					row := c.QueryRowContext(ctx, "SELECT current_schema()")
					if err := row.Scan(&schema); err != nil {
						t.Fatal(err)
					}
					seen = append(seen, schema)
				}
				fresh := !slices.Contains(schemas, seen[0])
				if seen[0] != seen[1] || !fresh || !strings.HasPrefix(seen[0], "scratch_") {
					t.Fatalf("schemas %v after %v; want one new scratch schema", seen, schemas)
				}
				schemas = append(schemas, seen[0])
				var n int
				err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables "+
					"WHERE table_schema = current_schema()").Scan(&n)
				if err != nil || n != 0 {
					t.Fatalf("the scratch schema holds %d tables (%v); want none", n, err)
				}
			}
		})
		for _, schema := range schemas {
			var n int
			q := "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = $1"
			err := admin.QueryRowContext(t.Context(), q, schema).Scan(&n)
			if err != nil || n != 0 {
				t.Fatalf("schema %s outlived its test (%d, %v)", schema, n, err)
			}
		}
	}
}
