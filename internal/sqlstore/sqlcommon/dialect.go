// Package sqlcommon implements pkg/store over database/sql for any SQL
// dialect (decision 0004), following hostweave's dialect layer: one store
// implementation, a Dialect describing what differs between engines, and
// embedded per-dialect migrations.
//
// Schema conventions shared by every table (research 15 section 4):
//
//   - Times are BIGINT Unix microseconds, NULL for a zero time; durations
//     are BIGINT milliseconds. Integers compare and sort identically on
//     every engine, which keeps keyset pagination exact.
//   - JSON documents are TEXT, so they round-trip byte for byte.
//   - Sealed values are BLOB (BYTEA) columns of their own, never inside a
//     document, sealed with the purpose and row id of decision 0017.
//   - Text columns that lists sort by are byte-ordered (COLLATE "C" on
//     PostgreSQL, SQLite's default), so every backend pages in the order
//     inmem does.
package sqlcommon

import (
	"io/fs"
	"strconv"
	"strings"
)

// Dialect describes what differs between SQL engines.
type Dialect struct {
	// Name is used in errors.
	Name string
	// Dollar selects $1-style placeholders (PostgreSQL); otherwise "?".
	Dollar bool
	// ForUpdate is appended to row-locking SELECTs inside transactions, or
	// empty when the engine serialises writers (SQLite).
	ForUpdate string
	// Migrations holds NNNN_name.sql files with "-- +up" and "-- +down"
	// sections.
	Migrations fs.FS
	// IsUniqueViolation reports whether err is the engine's unique or
	// primary-key constraint error, so the store returns store.ErrConflict
	// instead of a driver error when two writers race for one key.
	IsUniqueViolation func(error) bool
}

// uniqueViolation recognises the engine's unique-constraint failure.
func (d Dialect) uniqueViolation(err error) bool {
	return err != nil && d.IsUniqueViolation != nil && d.IsUniqueViolation(err)
}

// Rebind converts "?" placeholders to the dialect's form. A "?" inside a
// single-quoted literal is left alone.
func (d Dialect) Rebind(query string) string {
	if !d.Dollar {
		return query
	}
	var sb strings.Builder
	sb.Grow(len(query) + 8)
	n := 0
	quoted := false
	for _, r := range query {
		switch {
		case r == '\'':
			quoted = !quoted
		case r == '?' && !quoted:
			n++
			sb.WriteByte('$')
			sb.WriteString(strconv.Itoa(n))
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// Upsert renders INSERT ... ON CONFLICT (key) DO UPDATE SET col =
// excluded.col for every non-key column; SQLite and PostgreSQL share the
// syntax.
func Upsert(table string, cols, key []string) string {
	skip := make(map[string]bool, len(key))
	for _, k := range key {
		skip[k] = true
	}
	sets := make([]string, 0, len(cols))
	for _, c := range cols {
		if !skip[c] {
			sets = append(sets, c+" = excluded."+c)
		}
	}
	conflict := " ON CONFLICT (" + strings.Join(key, ", ") + ") DO "
	if len(sets) == 0 {
		return Insert(table, cols) + conflict + "NOTHING"
	}
	return Insert(table, cols) + conflict + "UPDATE SET " + strings.Join(sets, ", ")
}

// InsertIgnore renders INSERT ... ON CONFLICT (key) DO NOTHING.
func InsertIgnore(table string, cols, key []string) string {
	return Insert(table, cols) + " ON CONFLICT (" + strings.Join(key, ", ") + ") DO NOTHING"
}

// Insert renders an INSERT with one placeholder per column.
func Insert(table string, cols []string) string {
	return "INSERT INTO " + table + " (" + strings.Join(cols, ", ") + ") VALUES (" +
		Placeholders(len(cols)) + ")"
}

// Placeholders returns "?, ?, ?" for n values.
func Placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}
