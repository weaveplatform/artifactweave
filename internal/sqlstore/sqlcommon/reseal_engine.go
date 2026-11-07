package sqlcommon

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// sealedColumn is one column holding sealed values: the purpose and the row
// id it is sealed with (decision 0017), and the key columns that identify
// its rows. Every column a store seals must be listed here, or
// `artifactweave keys rotate` leaves its values under the old key and they
// become unreadable once that key is removed; TestAccSealedColumnsComplete
// fails when a sealed column is missing.
type sealedColumn struct {
	table, column, purpose string
	keys                   []string
	rowID                  func(key []string) string
}

func firstKey(k []string) string { return k[0] }

// sealedRow is one value read for re-sealing, with its key values as the
// driver returned them so the UPDATE binds them with their own types.
type sealedRow struct {
	key   []any
	value []byte
}

func (s *Store) resealColumn(ctx context.Context, c sealedColumn) (int, error) {
	op := "reseal " + c.table + "." + c.column
	// Read everything first: an in-memory SQLite database has one
	// connection, which an open result set would hold while updating.
	var rows []sealedRow
	err := s.Rows(ctx, op,
		"SELECT "+strings.Join(c.keys, ", ")+", "+c.column+" FROM "+c.table+
			" WHERE "+c.column+" IS NOT NULL",
		nil,
		func(r *sql.Rows) error {
			row := sealedRow{key: make([]any, len(c.keys))}
			dest := make([]any, 0, len(c.keys)+1)
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
	where := make([]string, 0, len(c.keys)+1)
	for _, k := range c.keys {
		where = append(where, k+" = ?")
	}
	update := "UPDATE " + c.table + " SET " + c.column + " = ? WHERE " +
		strings.Join(append(where, c.column+" = ?"), " AND ")
	changed := 0
	for _, row := range rows {
		id := c.rowID(keyStrings(row.key))
		next, did, err := s.ResealValue(c.purpose, id, row.value)
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
