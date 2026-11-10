package storetest

import (
	"database/sql"
	"errors"
	"slices"
	"testing"

	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	platsqltest "github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

// legacyHistory is a history table that predates the per-module migration
// streams, with versions and no module, and one row.
var legacyHistory = []string{
	"CREATE TABLE schema_migrations (version INTEGER NOT NULL PRIMARY KEY, " +
		"name VARCHAR(255) NOT NULL, applied_at BIGINT NOT NULL)",
	"INSERT INTO schema_migrations (version, name, applied_at) " +
		"VALUES (22, 'bower_registrations', 1767225600000000)",
}

// LegacyHistoryRefused writes a history table that predates the per-module
// streams, with one row, into the empty database db, and checks that open,
// which opens a store over the same database with migrations, and the
// migrator's Migrate, Rollback and Version all fail with ErrLegacyHistory,
// which wraps ErrMigration. Afterwards the database must hold that table
// alone, with its columns and its row unchanged: the refusal writes nothing.
func LegacyHistoryRefused(t *testing.T, db *sql.DB, d dialect.Dialect, open func() error) {
	t.Helper()
	ctx := t.Context()
	for _, q := range legacyHistory {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	opened := open()
	mods := appsql.Modules()
	_, up := dialect.Migrate(ctx, db, d, mods, "")
	_, down := dialect.Rollback(ctx, db, d, mods, "", 0)
	_, version := dialect.Version(ctx, db, d, mods)
	for _, c := range []struct {
		name string
		err  error
	}{{"Open", opened}, {"Migrate", up}, {"Rollback", down}, {"Version", version}} {
		if !errors.Is(c.err, dialect.ErrLegacyHistory) ||
			!errors.Is(c.err, dialect.ErrMigration) {
			t.Errorf("%s = %v; want ErrLegacyHistory", c.name, c.err)
		}
	}
	// Schema leaves out the history table, so any other object shows here.
	if got, err := platsqltest.Schema(ctx, db, d.Name); err != nil || got != "\n" {
		t.Errorf("the refused database holds more than its history (%v):\n%s", err, got)
	}
	var columns []string
	rows, err := db.QueryContext(ctx, d.Rebind(d.Columns), "schema_migrations")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	slices.Sort(columns)
	if want := []string{"applied_at", "name", "version"}; !slices.Equal(columns, want) {
		t.Errorf("history columns = %q; want %q", columns, want)
	}
	var n, version22 int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*), MAX(version) FROM schema_migrations "+
		"WHERE name = 'bower_registrations' AND applied_at = 1767225600000000").
		Scan(&n, &version22); err != nil || n != 1 || version22 != 22 {
		t.Errorf("history row = %d rows at version %d, %v; want the one row unchanged",
			n, version22, err)
	}
}
