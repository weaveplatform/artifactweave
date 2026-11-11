package postgres_test

import (
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/weaveplatform/artifactweave/internal/platform/sql/postgres"
)

func TestAccIsUniqueViolation(t *testing.T) {
	if !postgres.IsUniqueViolation(&pgconn.PgError{Code: "23505"}) {
		t.Fatal("23505 is a unique violation")
	}
	if postgres.IsUniqueViolation(&pgconn.PgError{Code: "23503"}) ||
		postgres.IsUniqueViolation(errors.New("x")) {
		t.Fatal("other errors are not")
	}
}

func TestUnitOpenDBRefusesAnEmptyDSN(t *testing.T) {
	db, err := postgres.OpenDB("", postgres.Options{})
	if db != nil || !errors.Is(err, postgres.ErrDSNRequired) {
		t.Fatalf("OpenDB(\"\") = %v, %v; want ErrDSNRequired", db, err)
	}
	if got := err.Error(); got != "postgres: dsn is required" {
		t.Errorf("Error() = %q", got)
	}
}

// The parse error reaches operators as "open store: postgres: parse dsn:
// ...", so the prefix is pinned, and pgx's own error stays wrapped beneath
// it for errors.As.
func TestUnitOpenDBRejectsAMalformedDSN(t *testing.T) {
	db, err := postgres.OpenDB("postgres://%zz", postgres.Options{})
	if db != nil || err == nil {
		t.Fatalf("OpenDB of a malformed DSN = %v, %v; want an error", db, err)
	}
	if !strings.HasPrefix(err.Error(), "postgres: parse dsn: ") {
		t.Errorf("Error() = %q, want the postgres: parse dsn: prefix", err)
	}
	var parse *pgconn.ParseConfigError
	if !errors.As(err, &parse) || errors.Is(err, postgres.ErrDSNRequired) {
		t.Errorf("%v does not wrap pgx's parse error alone", err)
	}
}

// OpenDB does not connect, so the pool's settings are read without a
// server: the size through Stats, and the lifetime, which database/sql
// exposes no getter for, from the pool's field. Observing the lifetime
// instead needs a connection to recycle, which these tests do not start.
func TestUnitOpenDBSetsThePool(t *testing.T) {
	for _, c := range []struct {
		name string
		o    postgres.Options
	}{
		{"database/sql defaults", postgres.Options{}},
		{"set", postgres.Options{MaxOpenConns: 8, ConnMaxLifetime: time.Minute}},
	} {
		t.Run(c.name, func(t *testing.T) {
			db, err := postgres.OpenDB("postgres://aw@127.0.0.1:1/aw?connect_timeout=2", c.o)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			stats := db.Stats()
			if got, want := stats.MaxOpenConnections, c.o.MaxOpenConns; got != want {
				t.Errorf("MaxOpenConnections = %d, want %d", got, want)
			}
			if stats.OpenConnections != 0 {
				t.Errorf("OpenDB opened %d connections, want none", stats.OpenConnections)
			}
			if got := maxLifetime(t, db); got != c.o.ConnMaxLifetime {
				t.Errorf("connection lifetime = %v, want %v", got, c.o.ConnMaxLifetime)
			}
		})
	}
}

// maxLifetime reads the lifetime that SetConnMaxLifetime stored in db.
func maxLifetime(t *testing.T, db *sql.DB) time.Duration {
	t.Helper()
	f := reflect.ValueOf(db).Elem().FieldByName("maxLifetime")
	if f.Kind() != reflect.Int64 {
		t.Fatalf("sql.DB has no maxLifetime duration field (%v); read it another way", f.Kind())
	}
	return time.Duration(f.Int())
}
