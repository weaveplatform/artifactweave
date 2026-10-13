package inmem

import (
	"cmp"
	"fmt"
	"strconv"

	"github.com/weaveplatform/artifactweave/pkg/store"
)

// paginate returns one page of rows, which must already be sorted by key
// (ascending, or descending when desc). key returns a row's sort key as
// string and int64 parts, the same parts the SQL backends page by, so a
// cursor from one backend means the same position in the other.
func paginate[V any](rows []V, p store.Page, desc bool, key func(V) []any) ([]V, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	if len(rows) == 0 {
		return nil, "", nil
	}
	cursor, err := p.Key(len(key(rows[0])))
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	start := 0
	if cursor != nil {
		start = len(rows)
		for i, r := range rows {
			c, err := compareKey(key(r), cursor)
			if err != nil {
				return nil, "", err
			}
			if (!desc && c > 0) || (desc && c < 0) {
				start = i
				break
			}
		}
	}
	rows = rows[start:]
	if len(rows) > p.Limit+1 {
		rows = rows[:p.Limit+1]
	}
	page, next := store.Trim(rows, p.Limit, func(v V) []string { return keyStrings(key(v)) })
	return page, next, nil
}

// keyStrings renders sort-key parts as cursor parts.
func keyStrings(parts []any) []string {
	out := make([]string, len(parts))
	for i, p := range parts {
		switch v := p.(type) {
		case int64:
			out[i] = strconv.FormatInt(v, 10)
		default:
			out[i] = fmt.Sprint(v)
		}
	}
	return out
}

// compareKey compares a row's sort key with a decoded cursor, part by part,
// integers numerically and everything else as strings.
func compareKey(row []any, cursor []string) (int, error) {
	for i, part := range row {
		var c int
		switch v := part.(type) {
		case int64:
			n, err := strconv.ParseInt(cursor[i], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("%w: %q is not an integer", store.ErrInvalidCursor, cursor[i])
			}
			c = cmp.Compare(v, n)
		default:
			c = cmp.Compare(fmt.Sprint(v), cursor[i])
		}
		if c != 0 {
			return c, nil
		}
	}
	return 0, nil
}
