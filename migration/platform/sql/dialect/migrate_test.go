package dialect_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
)

// unreadableDir is a file system whose root cannot be listed.
type unreadableDir struct{}

func (unreadableDir) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
}

// unreadableFiles lists its files but cannot read them.
type unreadableFiles struct{ fstest.MapFS }

func (unreadableFiles) ReadFile(name string) ([]byte, error) {
	return nil, &fs.PathError{Op: "read", Path: name, Err: fs.ErrPermission}
}

func file(text string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(text)} }

// wantMigrationError fails t unless err wraps ErrMigration and its text
// starts with prefix; the rest is the engine's own message.
func wantMigrationError(t *testing.T, call string, err error, prefix string) {
	t.Helper()
	if !errors.Is(err, dialect.ErrMigration) || !strings.HasPrefix(fmt.Sprint(err), prefix) {
		t.Errorf("%s = %v; want ErrMigration and an error starting %s", call, err, prefix)
	}
}

// Only NNNN_name.sql files at the top of the directory are migrations, and
// they sort by version, not by name. Comment and blank lines are dropped, a
// statement ends at a semicolon that ends a line, and the last statement of a
// section may leave it out.
func TestUnitLoadMigrationsReadsSections(t *testing.T) {
	got, err := dialect.LoadMigrations(fstest.MapFS{
		"0010_b.sql": file("-- before any section\n-- +up\nCREATE TABLE b (\n\tx INTEGER\n);\n\n" +
			"-- a comment\nINSERT INTO b (x) VALUES (1)\n-- +down\nDROP TABLE b;\n"),
		"2_a.sql":        file("-- +up\nCREATE TABLE a (x INTEGER);\n"),
		"README.md":      file("not a migration"),
		"old/0001_z.sql": file("-- +up\nSELECT 1;\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []dialect.Migration{
		{Version: 2, Name: "a", Up: []string{"CREATE TABLE a (x INTEGER)"}},
		{
			Version: 10, Name: "b",
			Up:   []string{"CREATE TABLE b (\n\tx INTEGER\n)", "INSERT INTO b (x) VALUES (1)"},
			Down: []string{"DROP TABLE b"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LoadMigrations = %#v\nwant %#v", got, want)
	}
}

// A malformed or unreadable migration stops the load, so a backend never
// starts on part of its schema.
func TestUnitLoadMigrationsRejectsMalformedFiles(t *testing.T) {
	up := file("-- +up\nCREATE TABLE a (x INTEGER);\n")
	for _, c := range []struct {
		name string
		fsys fs.FS
		want string
	}{
		{"no name", fstest.MapFS{"0001.sql": up}, "0001.sql: want NNNN_name.sql"},
		{"a version that is not a number", fstest.MapFS{"v1_a.sql": up}, `v1_a.sql: bad version "v1"`},
		{"version zero", fstest.MapFS{"0000_a.sql": up}, `0000_a.sql: bad version "0000"`},
		{
			"no up statements",
			fstest.MapFS{"0001_a.sql": file("-- +up\n-- +down\nDROP TABLE a;\n")},
			"0001_a.sql: no statements under -- +up",
		},
		{
			"one version twice",
			fstest.MapFS{"0001_a.sql": up, "1_b.sql": up},
			"version 1 defined by 0001_a.sql and 1_b.sql",
		},
		{"an unreadable directory", unreadableDir{}, "read dir: open .: permission denied"},
		{
			"an unreadable file",
			unreadableFiles{fstest.MapFS{"0001_a.sql": up}},
			"0001_a.sql: read 0001_a.sql: permission denied",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			ms, err := dialect.LoadMigrations(c.fsys)
			want := "sqlcommon: migration: " + c.want
			if !errors.Is(err, dialect.ErrMigration) || err.Error() != want {
				t.Errorf("LoadMigrations = %v, %v; want %s", ms, err, want)
			}
		})
	}
}

// The migrator reads every stream through MustSub with the dialect's name,
// which is fixed when a driver is built, so a name fs.Sub refuses panics
// rather than failing at run time.
func TestUnitMustSubPanicsOnInvalidDirectory(t *testing.T) {
	defer func() {
		got := fmt.Sprint(recover())
		want := `sqlcommon: migrations directory "../migrations": `
		if !strings.HasPrefix(got, want) {
			t.Errorf("MustSub panicked with %q; want a panic starting %q", got, want)
		}
	}()
	dialect.MustSub(fstest.MapFS{}, "../migrations")
}

// fakeDialect runs on the fault driver; its Columns query is the statement
// the faults name to fail the legacy probe alone.
var fakeDialect = dialect.Dialect{Name: "fake", Columns: "SELECT name FROM columns(?)"}

// stream builds a module whose stream holds files, keyed by file name, in
// the directory of the dialect called dir.
func stream(name, dir string, files map[string]string) dialect.Module {
	fsys := fstest.MapFS{}
	for f, text := range files {
		fsys[dir+"/"+f] = file(text)
	}
	return dialect.Module{Name: name, Migrations: fsys}
}

// creates is a migration that creates table and drops it again.
func creates(table string) string {
	return "-- +up\nCREATE TABLE " + table + " (x INTEGER);\n-- +down\nDROP TABLE " + table + ";\n"
}

// Migrate, Rollback and Version all load the streams first, so migrations
// that cannot be read fail each of them before it touches the database.
func TestUnitMigratorReportsUnreadableMigrations(t *testing.T) {
	ctx := context.Background()
	db := faultDB(t, faults{exec: true, query: true}) // a statement would fail differently
	mods := []dialect.Module{{Name: "m", Migrations: unreadableDir{}}}
	_, up := dialect.Migrate(ctx, db, fakeDialect, mods, "")
	_, down := dialect.Rollback(ctx, db, fakeDialect, mods, "", 0)
	_, version := dialect.Version(ctx, db, fakeDialect, mods)
	const want = "sqlcommon: migration: read dir: open .: permission denied"
	for name, err := range map[string]error{"Migrate": up, "Rollback": down, "Version": version} {
		if !errors.Is(err, dialect.ErrMigration) || err.Error() != want {
			t.Errorf("%s = %v; want %s", name, err, want)
		}
	}
}

// The arguments are checked before the database is touched: a module the
// list does not have, a rollback target without a module, a module list
// with a name missing or repeated, and a dialect with no Columns query.
func TestUnitMigratorChecksItsArguments(t *testing.T) {
	ctx := context.Background()
	db := faultDB(t, faults{exec: true, query: true, begin: true}) // any use fails differently
	mods := []dialect.Module{stream("a", "fake", map[string]string{"0001_x.sql": creates("a")})}
	for _, c := range []struct {
		name string
		call func() error
		is   error
		want string
	}{
		{
			"Migrate with an unknown module",
			func() error { _, err := dialect.Migrate(ctx, db, fakeDialect, mods, "b"); return err },
			dialect.ErrUnknownModule, `sqlcommon: migration: unknown module "b"`,
		},
		{
			"Rollback with an unknown module",
			func() error { _, err := dialect.Rollback(ctx, db, fakeDialect, mods, "b", 0); return err },
			dialect.ErrUnknownModule, `sqlcommon: migration: unknown module "b"`,
		},
		{
			"Rollback to a version without a module",
			func() error { _, err := dialect.Rollback(ctx, db, fakeDialect, mods, "", 2); return err },
			dialect.ErrModuleRequired,
			"sqlcommon: migration: a target version other than 0 needs a module",
		},
		{
			"a module without a name",
			func() error {
				_, err := dialect.Version(ctx, db, fakeDialect, append(mods, dialect.Module{}))
				return err
			},
			dialect.ErrMigration, "sqlcommon: migration: a module has no name",
		},
		{
			"a module registered twice",
			func() error {
				_, err := dialect.Migrate(ctx, db, fakeDialect, append(mods, mods[0]), "")
				return err
			},
			dialect.ErrMigration, `sqlcommon: migration: module "a" registered twice`,
		},
		{
			"a dialect without a Columns query",
			func() error {
				_, err := dialect.Version(ctx, db, dialect.Dialect{Name: "fake"}, mods)
				return err
			},
			dialect.ErrMigration, "sqlcommon: migration: dialect fake has no Columns query",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := c.call()
			if !errors.Is(err, c.is) || !errors.Is(err, dialect.ErrMigration) ||
				err.Error() != c.want {
				t.Errorf("got %v; want %s", err, c.want)
			}
		})
	}
}

// A failure of the legacy probe, of the history table or of a step's
// transaction wraps ErrMigration, and Migrate reports no step applied.
// SQLite cannot be made to fail these on demand, so the fault driver does.
func TestUnitMigratorReportsDriverFaults(t *testing.T) {
	mods := []dialect.Module{stream("m", "fake", map[string]string{"0001_a.sql": creates("a")})}
	for _, c := range []struct {
		name  string
		f     faults
		fault bool
		want  string
	}{
		{
			name: "query the history columns", f: faults{query: true, on: "columns("},
			fault: true, want: "read schema_migrations columns: fault",
		},
		{
			name: "read the history columns", f: faults{next: true, on: "columns("},
			fault: true, want: "read schema_migrations columns: fault",
		},
		{
			name: "scan a history column", f: faults{oneRow: true, null: true, on: "columns("},
			want: `scan schema_migrations column: sql: Scan error on column index 0, ` +
				`name "version": converting NULL to string is unsupported`,
		},
		{
			name: "create the history table", f: faults{exec: true, on: "CREATE"},
			fault: true, want: "create schema_migrations: fault",
		},
		{
			name: "query the versions", f: faults{query: true, on: "SELECT module"},
			fault: true, want: "read versions: fault",
		},
		{
			name: "read the versions", f: faults{next: true, on: "SELECT module"},
			fault: true, want: "read versions: fault",
		},
		{
			name: "scan a version", f: faults{oneRow: true, on: "SELECT module"},
			want: "scan version: sql: expected 1 destination arguments in Scan, not 2",
		},
		{name: "begin a step", f: faults{begin: true}, fault: true, want: "m 1_a: begin: fault"},
		{name: "commit a step", f: faults{commit: true}, fault: true, want: "m 1_a: commit: fault"},
	} {
		t.Run(c.name, func(t *testing.T) {
			db := faultDB(t, c.f)
			done, err := dialect.Migrate(context.Background(), db, fakeDialect, mods, "")
			want := "sqlcommon: migration: " + c.want
			if !errors.Is(err, dialect.ErrMigration) || errors.Is(err, errFault) != c.fault ||
				err.Error() != want {
				t.Errorf("Migrate = %v; want %s", err, want)
			}
			if len(done) != 0 {
				t.Errorf("Migrate applied %v", done)
			}
		})
	}
}

// Applied prints as the migrate command lists it.
func TestUnitAppliedString(t *testing.T) {
	if got := (dialect.Applied{Module: "platform", Version: 1}).String(); got != "platform:1" {
		t.Errorf("String = %q; want platform:1", got)
	}
}

// Each step runs in its own transaction, so a statement that fails stops the
// migrator with ErrMigration, names the module, the migration and the
// statement's first line, and leaves the schema and the history as the last
// step that succeeded left them. The same holds for a down step.
func TestAccMigratorStopsAtAFailingStep(t *testing.T) {
	ctx := context.Background()
	db := openSQLite(t)
	mods := []dialect.Module{stream("m", "sqlite", map[string]string{
		"0001_a.sql": "-- +up\nCREATE TABLE a (x INTEGER);\n-- +down\nDROP TABLE missing;\n",
		"0002_b.sql": "-- +up\nCREATE TABLE b (x INTEGER);\n" +
			"CREATE TABLE a (\n\tx INTEGER\n);\n-- +down\nDROP TABLE b;\n",
	})}
	done, err := dialect.Migrate(ctx, db, sqliteDialect, mods, "")
	wantMigrationError(t, "Migrate", err, `sqlcommon: migration: m 2_b: "CREATE TABLE a (": `)
	if fmt.Sprint(done) != "[m:1]" {
		t.Errorf("Migrate applied %v; want [m:1]", done)
	}
	if hasTable(t, db, "b") {
		t.Error("the failed step's table b survived its rollback")
	}
	done, err = dialect.Rollback(ctx, db, sqliteDialect, mods, "", 0)
	const down = `sqlcommon: migration: m 1_a down: "DROP TABLE missing": `
	wantMigrationError(t, "Rollback", err, down)
	if len(done) != 0 {
		t.Errorf("Rollback reverted %v", done)
	}
	wantVersions(t, db, mods, "[{m 1 2}]")
}

// The history row is written in the step's own transaction, so a step whose
// statements leave nowhere to write it fails, and its statements roll back
// with it.
func TestAccMigratorRecordsEachStepInItsTransaction(t *testing.T) {
	ctx := context.Background()
	db := openSQLite(t)
	mods := []dialect.Module{stream("m", "sqlite", map[string]string{
		"0001_a.sql": "-- +up\nDROP TABLE schema_migrations;\n",
	})}
	done, err := dialect.Migrate(ctx, db, sqliteDialect, mods, "")
	wantMigrationError(t, "Migrate", err, "sqlcommon: migration: m 1_a: record: ")
	if len(done) != 0 {
		t.Errorf("Migrate applied %v", done)
	}
	if !hasTable(t, db, "schema_migrations") {
		t.Error("the failed step's DROP TABLE survived its rollback")
	}
}

// The migrator creates the history table only when it is absent, so a
// table of that name with a module column that it cannot read stops it
// rather than being replaced.
func TestAccMigratorReportsAnUnreadableVersionTable(t *testing.T) {
	for _, c := range []struct {
		name  string
		table []string
		want  string
	}{
		{
			"no version column",
			[]string{"CREATE TABLE schema_migrations (module TEXT)"},
			"sqlcommon: migration: read versions: ",
		},
		{
			"a version that is not a number",
			[]string{
				"CREATE TABLE schema_migrations (module TEXT, version TEXT, name TEXT, applied_at INTEGER)",
				"INSERT INTO schema_migrations VALUES ('m', 'one', 'a', 0)",
			},
			"sqlcommon: migration: scan version: ",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			db := openSQLite(t)
			for _, q := range c.table {
				mustExec(t, db, q)
			}
			_, err := dialect.Version(context.Background(), db, sqliteDialect, nil)
			wantMigrationError(t, "Version", err, c.want)
		})
	}
}

// Migrate applies the streams in the order of the modules and each stream
// by version, recording every migration under its module, and Rollback
// with no module reverts them all in the reverse order, newest first.
func TestAccMigratorFollowsTheModuleOrder(t *testing.T) {
	ctx := context.Background()
	db := openSQLite(t)
	b := map[string]string{"0002_y.sql": creates("b2"), "0001_x.sql": creates("b1")}
	mods := []dialect.Module{
		stream("b", "sqlite", b),
		stream("a", "sqlite", map[string]string{"0001_x.sql": creates("a1")}),
		{Name: "none"},
	}
	done, err := dialect.Migrate(ctx, db, sqliteDialect, mods, "")
	if err != nil || fmt.Sprint(done) != "[b:1 b:2 a:1]" {
		t.Fatalf("Migrate = %v, %v; want [b:1 b:2 a:1]", done, err)
	}
	if got := history(t, db); !slices.Equal(got, []string{"a 1 x", "b 1 x", "b 2 y"}) {
		t.Errorf("history = %q; want a 1 x, b 1 x and b 2 y", got)
	}
	wantVersions(t, db, mods, "[{b 2 2} {a 1 1} {none 0 0}]")
	if done, err := dialect.Migrate(ctx, db, sqliteDialect, mods, ""); err != nil || done != nil {
		t.Errorf("Migrate again = %v, %v; want nothing applied", done, err)
	}
	done, err = dialect.Rollback(ctx, db, sqliteDialect, mods, "", 0)
	if err != nil || fmt.Sprint(done) != "[a:1 b:2 b:1]" {
		t.Fatalf("Rollback = %v, %v; want [a:1 b:2 b:1]", done, err)
	}
	for _, table := range []string{"a1", "b1", "b2"} {
		if hasTable(t, db, table) {
			t.Errorf("table %s survived the rollback", table)
		}
	}
	if n := count(t, db, "schema_migrations"); n != 0 {
		t.Errorf("the history keeps %d rows after the rollback", n)
	}
}

// One stream can move on its own: only limits Migrate to it, Rollback with
// a module and a target reverts that stream alone, and a later Migrate
// brings a stream that is behind up to date without running again what the
// history records. A stream that gains a migration gets just that one.
func TestAccMigratorMovesOneStream(t *testing.T) {
	ctx := context.Background()
	db := openSQLite(t)
	a := map[string]string{"0001_x.sql": creates("a1")}
	b := map[string]string{"0001_x.sql": creates("b1"), "0002_y.sql": creates("b2")}
	mods := []dialect.Module{stream("a", "sqlite", a), stream("b", "sqlite", b)}
	done, err := dialect.Migrate(ctx, db, sqliteDialect, mods, "b")
	if err != nil || fmt.Sprint(done) != "[b:1 b:2]" || hasTable(t, db, "a1") {
		t.Fatalf("Migrate b = %v, %v; want [b:1 b:2] alone", done, err)
	}
	done, err = dialect.Rollback(ctx, db, sqliteDialect, mods, "b", 1)
	if err != nil || fmt.Sprint(done) != "[b:2]" {
		t.Fatalf("Rollback b to 1 = %v, %v; want [b:2]", done, err)
	}
	done, err = dialect.Migrate(ctx, db, sqliteDialect, mods, "")
	if err != nil || fmt.Sprint(done) != "[a:1 b:2]" {
		t.Fatalf("Migrate = %v, %v; want [a:1 b:2]", done, err)
	}
	a["0002_y.sql"] = creates("a2")
	mods[0] = stream("a", "sqlite", a)
	done, err = dialect.Migrate(ctx, db, sqliteDialect, mods, "")
	if err != nil || fmt.Sprint(done) != "[a:2]" {
		t.Fatalf("Migrate after a new migration = %v, %v; want [a:2]", done, err)
	}
	if got := history(t, db); !slices.Equal(got, []string{"a 1 x", "a 2 y", "b 1 x", "b 2 y"}) {
		t.Errorf("history = %q", got)
	}
}

// The history rows of a module that the list leaves out stay as they are:
// Version does not report the module, and Rollback of every stream leaves
// its migrations applied.
func TestAccMigratorLeavesOtherModulesAlone(t *testing.T) {
	ctx := context.Background()
	db := openSQLite(t)
	a := stream("a", "sqlite", map[string]string{"0001_x.sql": creates("a1")})
	b := stream("b", "sqlite", map[string]string{"0001_x.sql": creates("b1")})
	if _, err := dialect.Migrate(ctx, db, sqliteDialect, []dialect.Module{a, b}, ""); err != nil {
		t.Fatal(err)
	}
	wantVersions(t, db, []dialect.Module{a}, "[{a 1 1}]")
	done, err := dialect.Rollback(ctx, db, sqliteDialect, []dialect.Module{a}, "", 0)
	if err != nil || fmt.Sprint(done) != "[a:1]" {
		t.Fatalf("Rollback = %v, %v; want [a:1]", done, err)
	}
	if got := history(t, db); !slices.Equal(got, []string{"b 1 x"}) || !hasTable(t, db, "b1") {
		t.Errorf("history = %q; want b's migration kept, with its table", got)
	}
}

// A history table without a module column predates the streams. Every
// entry point refuses it with ErrLegacyHistory and writes nothing: the
// table and its row stay as they were, and no stream is applied.
func TestAccMigratorRefusesALegacyHistory(t *testing.T) {
	ctx := context.Background()
	db := openSQLite(t)
	mustExec(t, db, "CREATE TABLE schema_migrations (version INTEGER NOT NULL PRIMARY KEY, "+
		"name VARCHAR(255) NOT NULL, applied_at BIGINT NOT NULL)")
	mustExec(t, db, "INSERT INTO schema_migrations VALUES (22, 'bower_registrations', 7)")
	mods := []dialect.Module{stream("a", "sqlite", map[string]string{"0001_x.sql": creates("a1")})}
	_, up := dialect.Migrate(ctx, db, sqliteDialect, mods, "")
	_, down := dialect.Rollback(ctx, db, sqliteDialect, mods, "", 0)
	_, current := dialect.Version(ctx, db, sqliteDialect, mods)
	const want = "sqlcommon: migration: database history predates per-module migrations; " +
		"not upgraded"
	for name, err := range map[string]error{"Migrate": up, "Rollback": down, "Version": current} {
		if !errors.Is(err, dialect.ErrLegacyHistory) || !errors.Is(err, dialect.ErrMigration) ||
			err.Error() != want {
			t.Errorf("%s = %v; want %s", name, err, want)
		}
	}
	var objects, rows int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master").
		Scan(&objects); err != nil || objects != 1 {
		t.Errorf("the catalog holds %d objects, %v; want the legacy history alone", objects, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations "+
		"WHERE version = 22 AND name = 'bower_registrations' AND applied_at = 7").
		Scan(&rows); err != nil || rows != 1 || count(t, db, "schema_migrations") != 1 {
		t.Errorf("legacy rows = %d, %v; want the one row unchanged", rows, err)
	}
}

// wantVersions fails t unless Version reports want, printed with %v, for
// mods.
func wantVersions(t *testing.T, db *sql.DB, mods []dialect.Module, want string) {
	t.Helper()
	got, err := dialect.Version(context.Background(), db, sqliteDialect, mods)
	if err != nil || fmt.Sprint(got) != want {
		t.Errorf("Version = %v, %v; want %s", got, err, want)
	}
}

// history returns the history's rows as "module version name", in order.
func history(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		"SELECT module, version, name FROM schema_migrations ORDER BY module, version")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var module, name string
		var version int
		if err := rows.Scan(&module, &version, &name); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s %d %s", module, version, name))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
