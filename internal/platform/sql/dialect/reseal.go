package dialect

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"strings"
)

// SealedColumn is one column that holds sealed values (decision 0017): the
// Table and Column, the Purpose its values are sealed under, the Keys
// columns that identify a row, and RowID, which builds a row's id from the
// values of those columns, read as strings. Purpose and RowID must give
// exactly what the module's store passes to Seal for the same row, because
// both are authenticated with the value: Reseal fails on a sealed value
// that does not open under them, and would seal a plain value under a
// binding the store cannot open. A module registers every column it seals,
// because Reseal reaches only registered columns: `artifactweave keys
// rotate` would leave the values of a missing one under the old key, and
// once that key is removed from the configuration they no longer open.
type SealedColumn struct {
	Table, Column, Purpose string
	Keys                   []string
	RowID                  func(key []string) string
}

// FirstKey is the RowID of a column whose row id is the value of its first
// key column, such as a table keyed by name alone.
func FirstKey(k []string) string { return k[0] }

// Module is one module's registration with the SQL layer. Name is one of
// the canonical module names, and keys the module's rows in the migration
// history. Migrations is the module's migration stream: a directory per
// dialect, named like the dialect ("sqlite", "postgres"), of NNNN_name.sql
// files, or nil for a module with no tables. Sealed lists the sealed columns
// its store writes. BeforeReseal, when it is set, runs before the module's
// columns in Reseal, for work such as a backfill that seals values first;
// the count it returns is added to Reseal's. AfterMigrate, when it is set,
// runs after Migrate each time the store opens with migrations, for work DDL
// cannot do: a conversion that seals values with the configured keyring,
// because DDL never sees key material (decision 0017), or one that needs Go
// rather than the engine to fold text (decision 0043). The module SQL
// adapters register each module once, in the canonical module order
// (decision 0044, "Persistence"): platform, artifacts, access,
// notifications, retention, federation, releases, workers. Migrate,
// Rollback, Version and Reseal take the modules in that order.
type Module struct {
	Name         string
	Migrations   fs.FS
	Sealed       []SealedColumn
	BeforeReseal func(ctx context.Context, s *Store) (int, error)
	AfterMigrate func(ctx context.Context, s *Store) error
}

// sealedRow is one value read for re-sealing, with its key values as the
// driver returned them so the UPDATE binds them with their own types.
type sealedRow struct {
	key   []any
	value []byte
}

// Reseal re-seals the sealed columns of mods under the keyring's active
// version and returns how many values changed. It takes the modules in
// order and, for each, runs BeforeReseal, when it is set, and then its
// columns in order. Values already under the active version are left
// alone, and values stored before a keyring was configured are sealed for
// the first time, so running it again is harmless. Each value is replaced
// only if it still holds the bytes that were read, so a concurrent write is
// never overwritten with a stale secret. Without a keyring it returns
// ErrNoKeyring and runs nothing. Otherwise it stops at the first error,
// from a hook, a statement or a value the keyring cannot open, and returns
// it with the number of values changed before it. Reseal opens no
// transaction of its own: its statements run in the one ctx carries, if
// any. It is a function, not a method of Store, because a store that
// embeds *Store and declares a Reseal method would hide one of that name
// without a compile error.
func Reseal(ctx context.Context, s *Store, mods []Module) (int, error) {
	if s.keys == nil {
		return 0, ErrNoKeyring
	}
	changed := 0
	for _, m := range mods {
		if m.BeforeReseal != nil {
			n, err := m.BeforeReseal(ctx, s)
			changed += n
			if err != nil {
				return changed, err
			}
		}
		for _, c := range m.Sealed {
			n, err := s.resealColumn(ctx, c)
			changed += n
			if err != nil {
				return changed, err
			}
		}
	}
	return changed, nil
}

func (s *Store) resealColumn(ctx context.Context, c SealedColumn) (int, error) {
	op := "reseal " + c.Table + "." + c.Column
	// Read everything first: an in-memory SQLite database has one
	// connection, which an open result set would hold while updating.
	var rows []sealedRow
	err := s.Rows(ctx, op,
		"SELECT "+strings.Join(c.Keys, ", ")+", "+c.Column+" FROM "+c.Table+
			" WHERE "+c.Column+" IS NOT NULL",
		nil,
		func(r *sql.Rows) error {
			row := sealedRow{key: make([]any, len(c.Keys))}
			dest := make([]any, 0, len(c.Keys)+1)
			for i := range row.key {
				dest = append(dest, &row.key[i])
			}
			if err := r.Scan(append(dest, &row.value)...); err != nil {
				return err //nolint:wrapcheck // wrapped by Rows
			}
			rows = append(rows, row)
			return nil
		})
	if err != nil {
		return 0, err
	}
	where := make([]string, 0, len(c.Keys)+1)
	for _, k := range c.Keys {
		where = append(where, k+" = ?")
	}
	update := "UPDATE " + c.Table + " SET " + c.Column + " = ? WHERE " +
		strings.Join(append(where, c.Column+" = ?"), " AND ")
	changed := 0
	for _, row := range rows {
		id := c.RowID(keyStrings(row.key))
		next, did, err := s.resealValue(c.Purpose, id, row.value)
		if err != nil {
			return changed, fmt.Errorf("%s %q: %w", op, id, err)
		}
		if !did {
			continue
		}
		args := append(append([]any{next}, row.key...), row.value)
		n, err := s.Exec(ctx, op, update, args...)
		if err != nil {
			return changed, err
		}
		changed += int(n)
	}
	return changed, nil
}

// keyStrings renders scanned key values as the strings row ids are built
// from.
func keyStrings(key []any) []string {
	out := make([]string, len(key))
	for i, v := range key {
		if b, ok := v.([]byte); ok {
			out[i] = string(b)
			continue
		}
		out[i] = fmt.Sprint(v)
	}
	return out
}
