package kernel

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors. Implementations wrap them; callers test with errors.Is.
var (
	// ErrNotFound reports that a keyed record does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrConflict reports a create that collides with an existing key, or a
	// change that would break a uniqueness rule.
	ErrConflict = errors.New("store: conflict")
	// ErrInvalidCursor reports a malformed pagination cursor.
	ErrInvalidCursor = errors.New("store: invalid cursor")
	// ErrInvalid reports a record the store cannot hold as given, for
	// example an item path that does not split into parent and name.
	ErrInvalid = errors.New("store: invalid record")
)

// Page bounds.
const (
	DefaultLimit = 100
	MaxLimit     = 1000
)

// Page selects one page of a list. After is the next value returned with the
// previous page ("" for the first page). Limit bounds the page: DefaultLimit
// when zero or negative, capped at MaxLimit.
type Page struct {
	After string
	Limit int
}

// cursorSep separates the parts of a compound sort key. Unit separator: no
// key the stores sort by can contain it.
const cursorSep = "\x1f"

// Cursor returns the opaque cursor that continues a list after the record
// whose sort key is parts. Implementations call it for the last record of a
// page; callers treat the result as opaque.
func Cursor(parts ...string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join(parts, cursorSep)))
}

// Normalize applies the default and maximum limit and checks that After
// decodes.
func (p Page) Normalize() (Page, error) {
	if p.After != "" {
		if _, err := base64.RawURLEncoding.DecodeString(p.After); err != nil {
			return p, fmt.Errorf("%w: %w", ErrInvalidCursor, err)
		}
	}
	switch {
	case p.Limit <= 0:
		p.Limit = DefaultLimit
	case p.Limit > MaxLimit:
		p.Limit = MaxLimit
	}
	return p, nil
}

// Key decodes After into the n parts of the sort key it continues after. It
// returns nil for the first page and ErrInvalidCursor when After does not
// carry exactly n parts, which is what a cursor from a different list looks
// like.
func (p Page) Key(n int) ([]string, error) {
	if p.After == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(p.After)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidCursor, err)
	}
	parts := strings.Split(string(raw), cursorSep)
	if len(parts) != n {
		return nil, fmt.Errorf("%w: want %d parts, got %d", ErrInvalidCursor, n, len(parts))
	}
	return parts, nil
}

// Trim cuts rows, fetched with one more row than limit, to the page and
// returns the cursor of the next page ("" when rows was the last page). key
// returns a record's sort key, the parts Page.Key decodes.
func Trim[T any](rows []T, limit int, key func(T) []string) ([]T, string) {
	if len(rows) <= limit {
		return rows, ""
	}
	rows = rows[:limit]
	return rows, Cursor(key(rows[limit-1])...)
}
