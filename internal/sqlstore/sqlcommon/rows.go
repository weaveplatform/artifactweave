package sqlcommon

import "strings"

// entRow is a *sql.Row or *sql.Rows.
type entRow interface{ Scan(dest ...any) error }

// entBytes stores an empty byte slice as NULL.
func entBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// peersScanner is a *sql.Row or *sql.Rows.
type peersScanner interface{ Scan(dest ...any) error }

// opsCols splits a column list written as "a, b, c".
func opsCols(list string) []string { return strings.Split(list, ", ") }
