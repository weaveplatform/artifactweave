package dialect

import (
	"io/fs"
	"strconv"
	"strings"
)

// Dialect describes what differs between SQL engines. Each driver declares
// one value and passes it to New and to the migrator.
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
	// primary-key constraint error, so the store returns kernel.ErrConflict
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
// syntax. When every column is a key column it renders DO NOTHING instead,
// because neither engine accepts a SET with nothing to set.
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

// InsertIgnore renders INSERT ... ON CONFLICT (key) DO NOTHING, or ON
// CONFLICT DO NOTHING for any unique index when key is empty: a target must
// name a unique index's exact columns, and the property tables' differ
// between dialects (PostgreSQL migration 0013).
func InsertIgnore(table string, cols, key []string) string {
	if len(key) == 0 {
		return Insert(table, cols) + " ON CONFLICT DO NOTHING"
	}
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
