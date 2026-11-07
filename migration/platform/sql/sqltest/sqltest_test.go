package sqltest_test

import (
	"context"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/weaveplatform/artifactweave/migration/platform/crypt"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/sqltest"
)

// testKey is the master key of the keyring the harness seals with by default.
const testKey = "0123456789abcdef0123456789abcdef"

// stream is a module's migration stream of one migration per dialect, which
// creates table.
func stream(table string) fstest.MapFS {
	m := &fstest.MapFile{Data: []byte("-- +up\nCREATE TABLE " + table +
		" (name VARCHAR(64) PRIMARY KEY);\n-- +down\nDROP TABLE " + table + ";\n")}
	return fstest.MapFS{"sqlite/0001_" + table + ".sql": m, "postgres/0001_" + table + ".sql": m}
}

var things = dialect.Module{Name: "things", Migrations: stream("things")}

// newStores is the newStore that RunModule passes a suite of these tests.
type newStores = func(*testing.T) *dialect.Store

// asIs is the open function of a suite that uses the dialect store itself.
func asIs(s *dialect.Store) *dialect.Store { return s }

func keyring(t *testing.T, key string) *crypt.Keyring {
	t.Helper()
	k, err := crypt.New([]byte(key))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// tables lists the tables of the store's database; on PostgreSQL those of
// the current schema, the one the store creates its tables in.
func tables(t *testing.T, s *dialect.Store) []string {
	t.Helper()
	q := "SELECT name FROM sqlite_master WHERE type = 'table'"
	if s.Dialect().Name == "postgres" {
		q = "SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema()"
	}
	rows, err := s.DB().QueryContext(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

func count(t *testing.T, s *dialect.Store, table string) int {
	t.Helper()
	var n int
	row := s.DB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table)
	if err := row.Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func insert(t *testing.T, s *dialect.Store, table, name string) {
	t.Helper()
	q := "INSERT INTO " + table + " (name) VALUES (?)"
	if _, err := s.Exec(t.Context(), "insert", q, name); err != nil {
		t.Fatal(err)
	}
}

// sqliteFile is the file behind a SQLite store's main database: "" for
// one in memory.
func sqliteFile(t *testing.T, s *dialect.Store) string {
	t.Helper()
	var file string
	q := "SELECT file FROM pragma_database_list WHERE name = 'main'"
	if err := s.DB().QueryRowContext(t.Context(), q).Scan(&file); err != nil {
		t.Fatal(err)
	}
	return file
}

// TestAccRunModule runs a module of one table on every engine. Each
// newStore call returns a database of its own, migrated and empty, which
// holds the history and the module's table alone, so a row written through
// one store never reaches another. The subtests are named after the
// engines, in the order memory, file, postgres; postgres skips where there
// is no server.
func TestAccRunModule(t *testing.T) {
	dialects := map[string]string{"memory": "sqlite", "file": "sqlite", "postgres": "postgres"}
	var ran []string
	sqltest.RunModule(t, []dialect.Module{things}, asIs, func(t *testing.T, newStore newStores) {
		engine := path.Base(t.Name())
		ran = append(ran, engine)
		first, second := newStore(t), newStore(t)
		for _, s := range []*dialect.Store{first, second} {
			got := tables(t, s)
			if !slices.Equal(got, []string{"schema_migrations", "things"}) {
				t.Fatalf("tables %v; want the history and the module's table alone", got)
			}
			v, err := dialect.Version(t.Context(), s.DB(), s.Dialect(), []dialect.Module{things})
			if err != nil || len(v) != 1 || v[0].Current != 1 || v[0].Latest != 1 {
				t.Fatalf("Version = %v, %v; want things at 1 of 1", v, err)
			}
			if n := count(t, s, "things"); n != 0 {
				t.Fatalf("a fresh store holds %d rows", n)
			}
		}
		insert(t, first, "things", "a")
		if n := count(t, second, "things"); n != 0 {
			t.Fatal("a row written through one store reached another's database")
		}
		if got := first.Dialect().Name; got != dialects[engine] {
			t.Fatalf("dialect %q; want %q", got, dialects[engine])
		}
		if engine == "postgres" {
			return
		}
		file, other := sqliteFile(t, first), sqliteFile(t, second)
		if (engine == "memory") != (file == "") || (file != "" && file == other) {
			t.Fatalf("database files %q and %q", file, other)
		}
	}, sqltest.WithPostgres())
	want := []string{"memory", "file", "postgres"}
	if len(ran) < 2 || !slices.Equal(ran, want[:len(ran)]) {
		t.Fatalf("subtests %v; want %v, postgres where a server runs", ran, want)
	}
}

// TestAccRunModuleWithoutPostgres checks that PostgreSQL runs only when
// asked for.
func TestAccRunModuleWithoutPostgres(t *testing.T) {
	var ran []string
	sqltest.RunModule(t, []dialect.Module{things}, asIs, func(t *testing.T, newStore newStores) {
		ran = append(ran, path.Base(t.Name()))
		if got := newStore(t).Dialect().Name; got != "sqlite" {
			t.Fatalf("dialect %q; want sqlite", got)
		}
	})
	if !slices.Equal(ran, []string{"memory", "file"}) {
		t.Fatalf("subtests %v; want memory and file", ran)
	}
}

// TestAccRunModuleKeyrings checks the keyring the stores seal with: the
// test keyring of the composite store's runs by default, the one
// WithKeyring gives, and none for WithKeyring(nil).
func TestAccRunModuleKeyrings(t *testing.T) {
	testKeys := keyring(t, testKey)
	other := keyring(t, "fedcba9876543210fedcba9876543210")
	for _, c := range []struct {
		name           string
		opts           []sqltest.Option
		opens, refuses *crypt.Keyring
	}{
		{"default", nil, testKeys, other},
		{"WithKeyring", []sqltest.Option{sqltest.WithKeyring(other)}, other, testKeys},
		{"WithKeyring nil", []sqltest.Option{sqltest.WithKeyring(nil)}, nil, nil},
	} {
		suite := func(t *testing.T, newStore newStores) {
			sealed, err := newStore(t).Seal("thing", "a", "secret")
			if err != nil {
				t.Fatal(err)
			}
			if c.opens == nil {
				if string(sealed) != "secret" {
					t.Fatalf("Seal without a keyring stored %q; want the value as given", sealed)
				}
				return
			}
			got, err := c.opens.Open("thing", "a", sealed)
			if err != nil || string(got) != "secret" {
				t.Fatalf("Open = %q, %v; want the value under the expected keyring", got, err)
			}
			if _, err := c.refuses.Open("thing", "a", sealed); err == nil {
				t.Fatal("another keyring opened the value")
			}
		}
		t.Run(c.name, func(t *testing.T) {
			sqltest.RunModule(t, []dialect.Module{things}, asIs, suite, c.opts...)
		})
	}
}

// TestAccRunModuleRunsAfterMigrate checks that newStore runs each module's
// AfterMigrate once every stream is migrated, in the order of the modules,
// with the store it returns. Each hook writes to the other module's table,
// which the first hook could not do if it ran before the second stream
// migrated.
func TestAccRunModuleRunsAfterMigrate(t *testing.T) {
	var calls []string
	var hooked []*dialect.Store
	hook := func(name, table string) func(context.Context, *dialect.Store) error {
		return func(ctx context.Context, s *dialect.Store) error {
			calls = append(calls, name)
			hooked = append(hooked, s)
			_, err := s.Exec(ctx, "seed", "INSERT INTO "+table+" (name) VALUES (?)", name)
			return err
		}
	}
	mods := []dialect.Module{
		{Name: "first", Migrations: stream("firsts"), AfterMigrate: hook("first", "seconds")},
		{Name: "second", Migrations: stream("seconds"), AfterMigrate: hook("second", "firsts")},
	}
	check := func(t *testing.T, open func() *dialect.Store) {
		t.Helper()
		calls, hooked = nil, nil
		s := open()
		if !slices.Equal(calls, []string{"first", "second"}) {
			t.Fatalf("hooks ran %v; want first, second", calls)
		}
		if hooked[0] != s || hooked[1] != s {
			t.Fatal("a hook got another store than the one returned")
		}
		for _, table := range []string{"firsts", "seconds"} {
			if n := count(t, s, table); n != 1 {
				t.Fatalf("%s holds %d rows; want the hook's one", table, n)
			}
		}
	}
	sqltest.RunModule(t, mods, asIs, func(t *testing.T, newStore newStores) {
		check(t, func() *dialect.Store { return newStore(t) })
	})
	t.Run("OpenSQLite", func(t *testing.T) {
		check(t, func() *dialect.Store { return sqltest.OpenSQLite(t, mods...) })
	})
}

// TestAccOpenSQLite checks that each call returns a database of its own in
// memory, migrated with the modules and sealed with the test keyring.
func TestAccOpenSQLite(t *testing.T) {
	first, second := sqltest.OpenSQLite(t, things), sqltest.OpenSQLite(t, things)
	insert(t, first, "things", "a")
	if n := count(t, second, "things"); n != 0 {
		t.Fatal("a row written through one store reached another's database")
	}
	if file := sqliteFile(t, first); file != "" {
		t.Fatalf("database file %q; want one in memory", file)
	}
	sealed, err := first.Seal("thing", "a", "secret")
	if err != nil {
		t.Fatal(err)
	}
	got, err := keyring(t, testKey).Open("thing", "a", sealed)
	if err != nil || string(got) != "secret" {
		t.Fatalf("Open = %q, %v; want the value under the test keyring", got, err)
	}
}

// TestAccReset checks that Reset empties the tables of the modules it is
// given and leaves the rows of the others alone.
func TestAccReset(t *testing.T) {
	kept := dialect.Module{Name: "kept", Migrations: stream("kept")}
	s := sqltest.OpenSQLite(t, things, kept)
	insert(t, s, "things", "a")
	insert(t, s, "kept", "b")
	sqltest.Reset(t, s.DB(), s.Dialect(), []dialect.Module{things})
	if n := count(t, s, "things"); n != 0 {
		t.Fatalf("things holds %d rows after Reset", n)
	}
	if n := count(t, s, "kept"); n != 1 {
		t.Fatalf("kept holds %d rows after a Reset of things; want 1", n)
	}
}

// failCase names, in a child run of TestAccHarnessFailsTheTest, the
// failure to provoke.
const failCase = "ARTIFACTWEAVE_SQLTEST_FAILURE"

// broken is a module whose migration cannot run.
var broken = dialect.Module{Name: "broken", Migrations: fstest.MapFS{
	"sqlite/0001_broken.sql":   {Data: []byte("-- +up\nNOT SQL;\n")},
	"postgres/0001_broken.sql": {Data: []byte("-- +up\nNOT SQL;\n")},
}}

var hookFails = dialect.Module{
	Name:         "hooked",
	AfterMigrate: func(context.Context, *dialect.Store) error { return errors.New("hook failed") },
}

const brokenMigration = `migrate: sqlcommon: migration: broken 1_broken: "NOT SQL"`

// failures are the ways the harness fails the test that calls it, each with
// the lines its child run must print.
var failures = map[string]struct {
	provoke func(t *testing.T)
	want    []string
}{
	"OpenSQLite migration": {
		func(t *testing.T) { sqltest.OpenSQLite(t, broken) },
		[]string{brokenMigration},
	},
	"RunModule migration": {
		func(t *testing.T) {
			sqltest.RunModule(t, []dialect.Module{broken}, asIs,
				func(t *testing.T, newStore newStores) { newStore(t) })
		},
		[]string{
			"--- FAIL: TestAccHarnessFailsTheTest/memory",
			"--- FAIL: TestAccHarnessFailsTheTest/file",
			brokenMigration,
		},
	},
	"AfterMigrate": {
		func(t *testing.T) { sqltest.OpenSQLite(t, hookFails) },
		[]string{"hooked after migrate: hook failed"},
	},
	"Exec": {
		func(t *testing.T) { sqltest.Exec(t, sqltest.OpenSQLite(t).DB(), "SELECT 1", "NOT SQL") },
		[]string{"NOT SQL: "},
	},
	"Fails": {
		func(t *testing.T) {
			sqltest.Fails(t, map[string]error{"the call": nil, "a failing call": errors.New("x")})
		},
		[]string{"the call succeeded"},
	},
}

// TestAccHarnessFailsTheTest checks that the harness fails the test that
// calls it, with its message, when a migration, a hook or a statement fails
// and when a call that should have failed did not. A test cannot watch its
// own *testing.T fail and still pass, so each failure runs in a child run
// of this test, and the parent checks that the child failed and printed
// what it should.
func TestAccHarnessFailsTheTest(t *testing.T) {
	if name := os.Getenv(failCase); name != "" {
		failures[name].provoke(t)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range slices.Sorted(maps.Keys(failures)) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestAccHarnessFailsTheTest$")
			cmd.Env = append(os.Environ(), failCase+"="+name)
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("child run = %v; want it to fail:\n%s", err, out)
			}
			for _, want := range failures[name].want {
				if !strings.Contains(string(out), want) {
					t.Errorf("child run printed no %q:\n%s", want, out)
				}
			}
		})
	}
}
