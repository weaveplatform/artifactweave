package dialect

import "strings"

// EntRow is a *sql.Row or *sql.Rows, so that one scan function reads a row
// of either.
type EntRow interface{ Scan(dest ...any) error }

// EntBytes stores an empty byte slice as NULL.
func EntBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// PeersScanner is a *sql.Row or *sql.Rows, as EntRow is; the peer and outbox
// stores scan with it.
type PeersScanner interface{ Scan(dest ...any) error }

// OpsCols splits a column list written as "a, b, c".
func OpsCols(list string) []string { return strings.Split(list, ", ") }
