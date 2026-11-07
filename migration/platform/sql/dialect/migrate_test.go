package dialect_test

import (
	"context"
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

// MustSub runs in the initialiser of a backend's package variable, where no
// error can be returned, so a directory name fs.Sub refuses panics.
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

// Migrate, Rollback and Version all load the migrations first, so migrations
// that cannot be read fail each of them before it touches the database.
func TestUnitMigratorReportsUnreadableMigrations(t *testing.T) {
	ctx := context.Background()
	db := faultDB(t, faults{exec: true}) // a statement would fail differently
	d := dialect.Dialect{Name: "fake", Migrations: unreadableDir{}}
	_, up := dialect.Migrate(ctx, db, d)
	_, down := dialect.Rollback(ctx, db, d, 0)
	_, _, version := dialect.Version(ctx, db, d)
	const want = "sqlcommon: migration: read dir: open .: permission denied"
	for name, err := range map[string]error{"Migrate": up, "Rollback": down, "Version": version} {
		if !errors.Is(err, dialect.ErrMigration) || err.Error() != want {
			t.Errorf("%s = %v; want %s", name, err, want)
		}
	}
}

// A failure of the version table or of a step's transaction wraps
// ErrMigration, and Migrate reports no step applied. SQLite cannot be made to
// fail these on demand, so the fault driver does.
func TestUnitMigratorReportsDriverFaults(t *testing.T) {
	d := dialect.Dialect{
		Name:       "fake",
		Migrations: fstest.MapFS{"0001_a.sql": file("-- +up\nCREATE TABLE a (x INTEGER);\n")},
	}
	for _, c := range []struct {
		name string
		f    faults
		want string
	}{
		{"create the version table", faults{exec: true}, "create schema_migrations: fault"},
		{"read the versions", faults{next: true}, "read versions: fault"},
		{"begin a step", faults{begin: true}, "1_a: begin: fault"},
		{"commit a step", faults{commit: true}, "1_a: commit: fault"},
	} {
		t.Run(c.name, func(t *testing.T) {
			done, err := dialect.Migrate(context.Background(), faultDB(t, c.f), d)
			want := "sqlcommon: migration: " + c.want
			if !errors.Is(err, dialect.ErrMigration) || !errors.Is(err, errFault) ||
				err.Error() != want {
				t.Errorf("Migrate = %v; want %s", err, want)
			}
			if len(done) != 0 {
				t.Errorf("Migrate applied %v", done)
			}
		})
	}
}

// Each step runs in its own transaction, so a statement that fails stops the
// migrator with ErrMigration, names the migration and the statement's first
// line, and leaves the schema and the version table as the last step that
// succeeded left them. The same holds for a down step.
func TestAccMigratorStopsAtAFailingStep(t *testing.T) {
	ctx := context.Background()
	db := openSQLite(t)
	d := dialect.Dialect{Name: "sqlite", Migrations: fstest.MapFS{
		"0001_a.sql": file("-- +up\nCREATE TABLE a (x INTEGER);\n-- +down\nDROP TABLE missing;\n"),
		"0002_b.sql": file("-- +up\nCREATE TABLE b (x INTEGER);\n" +
			"CREATE TABLE a (\n\tx INTEGER\n);\n-- +down\nDROP TABLE b;\n"),
	}}
	done, err := dialect.Migrate(ctx, db, d)
	wantMigrationError(t, "Migrate", err, `sqlcommon: migration: 2_b: "CREATE TABLE a (": `)
	if !slices.Equal(done, []int{1}) {
		t.Errorf("Migrate applied %v; want [1]", done)
	}
	if hasTable(t, db, "b") {
		t.Error("the failed step's table b survived its rollback")
	}
	done, err = dialect.Rollback(ctx, db, d, 0)
	wantMigrationError(t, "Rollback", err, `sqlcommon: migration: 1_a down: "DROP TABLE missing": `)
	if len(done) != 0 {
		t.Errorf("Rollback reverted %v", done)
	}
	if cur, latest, err := dialect.Version(ctx, db, d); cur != 1 || latest != 2 || err != nil {
		t.Errorf("Version = %d, %d, %v; want 1, 2", cur, latest, err)
	}
}

// The version is recorded in the step's own transaction, so a step whose
// statements leave nowhere to record it fails, and its statements roll back
// with it.
func TestAccMigratorRecordsEachStepInItsTransaction(t *testing.T) {
	ctx := context.Background()
	db := openSQLite(t)
	d := dialect.Dialect{Name: "sqlite", Migrations: fstest.MapFS{
		"0001_a.sql": file("-- +up\nDROP TABLE schema_migrations;\n"),
	}}
	done, err := dialect.Migrate(ctx, db, d)
	wantMigrationError(t, "Migrate", err, "sqlcommon: migration: 1_a: record: ")
	if len(done) != 0 {
		t.Errorf("Migrate applied %v", done)
	}
	if !hasTable(t, db, "schema_migrations") {
		t.Error("the failed step's DROP TABLE survived its rollback")
	}
}

// The migrator creates the version table only when it is absent, so a table
// of that name that it cannot read stops it rather than being replaced.
func TestAccMigratorReportsAnUnreadableVersionTable(t *testing.T) {
	d := dialect.Dialect{Name: "sqlite", Migrations: fstest.MapFS{}}
	for _, c := range []struct {
		name  string
		table []string
		want  string
	}{
		{
			"no version column",
			[]string{"CREATE TABLE schema_migrations (id INTEGER)"},
			"sqlcommon: migration: read versions: ",
		},
		{
			"a version that is not a number",
			[]string{
				"CREATE TABLE schema_migrations (version TEXT, name TEXT, applied_at INTEGER)",
				"INSERT INTO schema_migrations VALUES ('one', 'a', 0)",
			},
			"sqlcommon: migration: scan version: ",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			db := openSQLite(t)
			for _, q := range c.table {
				mustExec(t, db, q)
			}
			_, _, err := dialect.Version(context.Background(), db, d)
			wantMigrationError(t, "Version", err, c.want)
		})
	}
}
