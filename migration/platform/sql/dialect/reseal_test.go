package dialect_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/crypt"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
)

const (
	secretsTable = "CREATE TABLE secrets (name TEXT PRIMARY KEY, value BLOB)"
	othersTable  = "CREATE TABLE others (id INTEGER PRIMARY KEY, value BLOB)"
)

// secrets is a sealed column keyed by name, the shape of most of the SQL
// stores' columns, and others one keyed by an integer, whose row id the
// column builds from the key's digits.
var (
	secrets = dialect.SealedColumn{
		Table: "secrets", Column: "value", Purpose: "secrets.value",
		Keys: []string{"name"}, RowID: dialect.FirstKey,
	}
	others = dialect.SealedColumn{
		Table: "others", Column: "value", Purpose: "others.value",
		Keys:  []string{"id"},
		RowID: func(k []string) string { return "others/" + k[0] },
	}
)

// keyring returns a keyring holding a master key for each version, whose
// material is the version's digit repeated; the highest version seals.
func keyring(t *testing.T, versions ...uint32) *crypt.Keyring {
	t.Helper()
	var keys []crypt.MasterKey
	for _, v := range versions {
		material := []byte(strings.Repeat(strconv.FormatUint(uint64(v), 10), 32))
		keys = append(keys, crypt.MasterKey{Version: v, Material: material})
	}
	k, err := crypt.NewKeyring(keys...)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// sealFor seals plain for the row rowID of column c under k, as the store
// that writes the column seals it.
func sealFor(t *testing.T, k *crypt.Keyring, c dialect.SealedColumn, rowID, plain string) []byte {
	t.Helper()
	b, err := dialect.New(nil, sqliteDialect, k).Seal(c.Purpose, rowID, plain)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// put stores value under key in table, outside any transaction.
func put(t *testing.T, db *sql.DB, table string, key any, value []byte) {
	t.Helper()
	cols := map[string]string{"secrets": "name", "others": "id"}
	if _, err := db.ExecContext(context.Background(),
		"INSERT INTO "+table+" ("+cols[table]+", value) VALUES (?, ?)", key, value); err != nil {
		t.Fatal(err)
	}
}

// stored returns the value stored under name in the secrets table.
func stored(t *testing.T, db *sql.DB, name string) []byte {
	t.Helper()
	var v []byte
	err := db.QueryRowContext(context.Background(),
		"SELECT value FROM secrets WHERE name = ?", name).Scan(&v)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Without a keyring there is nothing to re-seal under, so Reseal refuses
// before it runs a hook or reads a column.
func TestUnitResealNeedsAKeyring(t *testing.T) {
	ran := false
	mods := []dialect.Module{{
		Name: "m",
		BeforeReseal: func(context.Context, *dialect.Store) (int, error) {
			ran = true
			return 1, nil
		},
		Sealed: []dialect.SealedColumn{secrets},
	}}
	s := dialect.New(nil, sqliteDialect, nil) // the refusal uses no pool
	n, err := dialect.Reseal(context.Background(), s, mods)
	if !errors.Is(err, dialect.ErrNoKeyring) || n != 0 || ran {
		t.Errorf("Reseal = %d, %v, hook ran %t; want 0 and %v before any hook",
			n, err, ran, dialect.ErrNoKeyring)
	}
}

// Reseal takes the modules in order, each one's hook before its columns, and
// adds what the hooks count to the values it changed. A value under an older
// key is re-sealed, a plain one is sealed for the first time, and one under
// the active key is left as it is, so a second run changes nothing.
func TestAccResealFollowsTheModuleOrder(t *testing.T) {
	ctx := context.Background()
	db := openSQLite(t)
	mustExec(t, db, secretsTable)
	mustExec(t, db, othersTable)
	v1, active := keyring(t, 1), keyring(t, 1, 2)
	put(t, db, "secrets", "old", sealFor(t, v1, secrets, "old", "old secret"))
	put(t, db, "secrets", "plain", []byte("plain secret"))
	current := sealFor(t, active, secrets, "current", "current secret")
	put(t, db, "secrets", "current", current)
	mustExec(t, db, "INSERT INTO secrets (name) VALUES ('none')") // NULL: nothing to seal
	put(t, db, "others", 7, sealFor(t, v1, others, "others/7", "other secret"))

	var order []string
	note := func(step string) {
		if len(order) == 0 || order[len(order)-1] != step {
			order = append(order, step)
		}
	}
	hook := func(name string) func(context.Context, *dialect.Store) (int, error) {
		return func(context.Context, *dialect.Store) (int, error) {
			note(name + " hook")
			return 2, nil
		}
	}
	noted := func(c dialect.SealedColumn) dialect.SealedColumn {
		rowID := c.RowID
		c.RowID = func(k []string) string {
			note(c.Table)
			return rowID(k)
		}
		return c
	}
	first, last := []dialect.SealedColumn{noted(secrets)}, []dialect.SealedColumn{noted(others)}
	mods := []dialect.Module{
		{Name: "first", BeforeReseal: hook("first"), Sealed: first},
		{Name: "empty"},
		{Name: "last", BeforeReseal: hook("last"), Sealed: last},
	}
	s := dialect.New(db, sqliteDialect, active)
	if n, err := dialect.Reseal(ctx, s, mods); err != nil || n != 2+2+2+1 {
		t.Fatalf("Reseal = %d, %v; want the hooks' 4 and 3 values", n, err)
	}
	want := []string{"first hook", "secrets", "last hook", "others"}
	if !slices.Equal(order, want) {
		t.Errorf("Reseal ran %v; want %v", order, want)
	}
	only2 := dialect.New(db, sqliteDialect, keyring(t, 2))
	for name, plain := range map[string]string{
		"old": "old secret", "plain": "plain secret", "current": "current secret",
	} {
		if got, err := only2.Unseal(secrets.Purpose, name, stored(t, db, name)); err != nil ||
			got != plain {
			t.Errorf("%s under version 2 alone = %q, %v; want %q", name, got, err, plain)
		}
	}
	if !bytes.Equal(stored(t, db, "current"), current) {
		t.Error("a value already under the active key was sealed again")
	}
	if n, err := dialect.Reseal(ctx, s, mods); err != nil || n != 2+2 {
		t.Errorf("second Reseal = %d, %v; want only the hooks' 4", n, err)
	}
}

// Reseal stops at the first failure, whatever its step, and returns it with
// the number of values changed before it: here the one value of the module
// before, plus what a failing hook counted. The module after is not reached.
func TestAccResealStopsAtTheFirstError(t *testing.T) {
	errHook := errors.New("hook failed")
	missing := dialect.SealedColumn{
		Table: "missing", Column: "value", Purpose: "missing.value",
		Keys: []string{"id"}, RowID: dialect.FirstKey,
	}
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, db *sql.DB)
		mod   dialect.Module
		n     int
		check func(err error) bool
	}{
		{
			name: "a hook",
			mod: dialect.Module{Name: "failing", BeforeReseal: func(context.Context, *dialect.Store) (int, error) {
				return 2, errHook
			}},
			n:     3,
			check: func(err error) bool { return errors.Is(err, errHook) },
		},
		{
			name: "a column that cannot be read",
			mod:  dialect.Module{Name: "failing", Sealed: []dialect.SealedColumn{missing}},
			n:    1,
			check: func(err error) bool {
				return err != nil && strings.HasPrefix(err.Error(), "sqlite: reseal missing.value: ")
			},
		},
		{
			name: "a value under a key the keyring lacks",
			setup: func(t *testing.T, db *sql.DB) {
				t.Helper()
				put(t, db, "others", 7, sealFor(t, keyring(t, 3), others, "others/7", "secret"))
			},
			mod: dialect.Module{Name: "failing", Sealed: []dialect.SealedColumn{others}},
			n:   1,
			check: func(err error) bool {
				return errors.Is(err, crypt.ErrUnknownKey) &&
					strings.HasPrefix(err.Error(), `reseal others.value "others/7": `)
			},
		},
		{
			name: "an update the database refuses",
			setup: func(t *testing.T, db *sql.DB) {
				t.Helper()
				put(t, db, "others", 7, sealFor(t, keyring(t, 1), others, "others/7", "secret"))
				mustExec(t, db, "CREATE TRIGGER refuse BEFORE UPDATE ON others "+
					"BEGIN SELECT RAISE(ABORT, 'refused'); END")
			},
			mod: dialect.Module{Name: "failing", Sealed: []dialect.SealedColumn{others}},
			n:   1,
			check: func(err error) bool {
				return err != nil && strings.HasPrefix(err.Error(), "sqlite: reseal others.value: ")
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			db := openSQLite(t)
			mustExec(t, db, secretsTable)
			mustExec(t, db, othersTable)
			put(t, db, "secrets", "a", sealFor(t, keyring(t, 1), secrets, "a", "secret"))
			if c.setup != nil {
				c.setup(t, db)
			}
			reached := false
			mods := []dialect.Module{
				{Name: "before", Sealed: []dialect.SealedColumn{secrets}},
				c.mod,
				{Name: "after", BeforeReseal: func(context.Context, *dialect.Store) (int, error) {
					reached = true
					return 0, nil
				}},
			}
			s := dialect.New(db, sqliteDialect, keyring(t, 1, 2))
			n, err := dialect.Reseal(context.Background(), s, mods)
			if n != c.n || !c.check(err) {
				t.Errorf("Reseal = %d, %v; want %d and the failure", n, err, c.n)
			}
			if reached {
				t.Error("Reseal went on past the failure")
			}
		})
	}
}

// A row that does not scan fails the column's read, named with the dialect
// and the operation. SQLite returns every stored value in a form that scans
// into bytes, so the fault driver returns a row with too few columns.
func TestUnitResealReportsAnUnscannableRow(t *testing.T) {
	s := dialect.New(faultDB(t, faults{oneRow: true}), dialect.Dialect{Name: "fake"}, keyring(t, 1))
	mods := []dialect.Module{{Name: "m", Sealed: []dialect.SealedColumn{secrets}}}
	n, err := dialect.Reseal(context.Background(), s, mods)
	if n != 0 || err == nil || !strings.HasPrefix(err.Error(), "fake: reseal secrets.value: ") {
		t.Errorf("Reseal = %d, %v; want 0 and the scan's error", n, err)
	}
}

// A value written between the read and the update is not overwritten: the
// update matches the bytes that were read, so it changes no row, and the
// count leaves the value out. The row id is built between the two steps,
// which is where the test makes the concurrent write.
func TestAccResealKeepsAValueChangedSinceItsRead(t *testing.T) {
	ctx := context.Background()
	db := openSQLite(t)
	mustExec(t, db, secretsTable)
	active := keyring(t, 1, 2)
	put(t, db, "secrets", "a", sealFor(t, keyring(t, 1), secrets, "a", "old"))
	fresh := sealFor(t, active, secrets, "a", "new")
	racing := secrets
	racing.RowID = func(k []string) string {
		if _, err := db.ExecContext(ctx,
			"UPDATE secrets SET value = ? WHERE name = ?", fresh, k[0]); err != nil {
			t.Error(err)
		}
		return k[0]
	}
	mods := []dialect.Module{{Name: "m", Sealed: []dialect.SealedColumn{racing}}}
	s := dialect.New(db, sqliteDialect, active)
	if n, err := dialect.Reseal(ctx, s, mods); err != nil || n != 0 {
		t.Errorf("Reseal = %d, %v; want 0 values changed", n, err)
	}
	if !bytes.Equal(stored(t, db, "a"), fresh) {
		t.Error("Reseal overwrote a value written after its read")
	}
}

// A key the driver returns as bytes, as SQLite returns a BLOB, makes the row
// id its bytes spell, as the store that sealed the value built it: any other
// rendering would bind another row id, and the value would not open.
func TestAccResealReadsByteKeysAsText(t *testing.T) {
	ctx := context.Background()
	db := openSQLite(t)
	mustExec(t, db, "CREATE TABLE blobs (k BLOB PRIMARY KEY, value BLOB)")
	blobs := dialect.SealedColumn{
		Table: "blobs", Column: "value", Purpose: "blobs.value",
		Keys: []string{"k"}, RowID: dialect.FirstKey,
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO blobs (k, value) VALUES (?, ?)",
		[]byte("k1"), sealFor(t, keyring(t, 1), blobs, "k1", "secret")); err != nil {
		t.Fatal(err)
	}
	mods := []dialect.Module{{Name: "m", Sealed: []dialect.SealedColumn{blobs}}}
	s := dialect.New(db, sqliteDialect, keyring(t, 1, 2))
	if n, err := dialect.Reseal(ctx, s, mods); err != nil || n != 1 {
		t.Fatalf("Reseal = %d, %v; want 1", n, err)
	}
	var v []byte
	if err := db.QueryRowContext(ctx, "SELECT value FROM blobs").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if got, err := dialect.New(db, sqliteDialect, keyring(t, 2)).
		Unseal(blobs.Purpose, "k1", v); err != nil || got != "secret" {
		t.Errorf("value under version 2 alone = %q, %v", got, err)
	}
}
