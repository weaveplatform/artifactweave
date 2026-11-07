package dialect

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/crypt"
)

// ErrNoKeyring reports a sealed value read by a store opened without a
// keyring: the server was started without the master key that sealed it.
var ErrNoKeyring = errors.New("sqlcommon: value is sealed but no keyring is configured")

// Store implements store.Store over database/sql for one Dialect.
type Store struct {
	db   *sql.DB
	d    Dialect
	keys *crypt.Keyring
}

// New wraps an open, migrated pool for one dialect. keys seals secret
// columns and may be nil.
func New(db *sql.DB, d Dialect, keys *crypt.Keyring) *Store {
	return &Store{db: db, d: d, keys: keys}
}

// DB returns the pool, for tests and the migrate command.
func (s *Store) DB() *sql.DB { return s.db }

// Dialect returns the store's dialect.
func (s *Store) Dialect() Dialect { return s.d }

// Tx implements store.Store.
func (s *Store) Tx(ctx context.Context, fn func(context.Context) error) error {
	return runTx(ctx, s.db, s.d, fn)
}

// AfterCommit implements store.Store.
func (*Store) AfterCommit(ctx context.Context, fn func()) { afterCommit(ctx, fn) }

// InTx reports whether ctx carries a transaction on the store's pool.
func (s *Store) InTx(ctx context.Context) bool { return inTx(ctx, s.db) }

// Close implements store.Store.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("%s: close: %w", s.d.Name, err)
	}
	return nil
}

// Q returns the transaction ctx carries, or the pool.
func (s *Store) Q(ctx context.Context) Queryer { return query(ctx, s.db) }

// Wrap maps driver errors onto the store sentinels and names the operation.
func (s *Store) Wrap(op string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%s: %w", op, kernel.ErrNotFound)
	case s.d.uniqueViolation(err):
		return fmt.Errorf("%s: %w", op, kernel.ErrConflict)
	default:
		return fmt.Errorf("%s: %s: %w", s.d.Name, op, err)
	}
}

// Exec runs a statement written with "?" placeholders and returns the
// number of rows it changed.
func (s *Store) Exec(ctx context.Context, op, q string, args ...any) (int64, error) {
	res, err := s.Q(ctx).ExecContext(ctx, s.d.Rebind(q), args...)
	if err != nil {
		return 0, s.Wrap(op, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, s.Wrap(op, err)
	}
	return n, nil
}

// ExecOne runs a statement that must change exactly one row; ErrNotFound
// when it changed none.
func (s *Store) ExecOne(ctx context.Context, op, q string, args ...any) error {
	n, err := s.Exec(ctx, op, q, args...)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%s: %w", op, kernel.ErrNotFound)
	}
	return nil
}

// InsertID runs an INSERT ... RETURNING id and returns the new id.
func (s *Store) InsertID(ctx context.Context, op, q string, args ...any) (int64, error) {
	var id int64
	if err := s.Q(ctx).
		QueryRowContext(ctx, s.d.Rebind(q+" RETURNING id"), args...).
		Scan(&id); err != nil {
		return 0, s.Wrap(op, err)
	}
	return id, nil
}

// Exists reports whether q (a SELECT 1 ...) returns a row.
func (s *Store) Exists(ctx context.Context, op, q string, args ...any) (bool, error) {
	var one int
	err := s.Q(ctx).QueryRowContext(ctx, s.d.Rebind(q), args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, s.Wrap(op, err)
	}
	return true, nil
}

// Rows runs a query and calls scan for each row.
func (s *Store) Rows(
	ctx context.Context,
	op, q string,
	args []any,
	scan func(*sql.Rows) error,
) error {
	rs, err := s.Q(ctx).QueryContext(ctx, s.d.Rebind(q), args...)
	if err != nil {
		return s.Wrap(op, err)
	}
	defer func() { _ = rs.Close() }()
	for rs.Next() {
		if err := scan(rs); err != nil {
			return s.Wrap(op, err)
		}
	}
	return s.Wrap(op, rs.Err())
}

// LockClause returns the row-lock suffix for a SELECT inside a transaction,
// or "" outside one and on engines that serialise writers.
func (s *Store) LockClause(ctx context.Context) string {
	if s.d.ForUpdate == "" || !inTx(ctx, s.db) {
		return ""
	}
	return " " + s.d.ForUpdate
}

// Seal seals a secret for one column of one row; "" is stored as NULL.
func (s *Store) Seal(purpose, rowID, plain string) ([]byte, error) {
	if plain == "" {
		return nil, nil
	}
	if s.keys == nil {
		return []byte(plain), nil
	}
	b, err := s.keys.Seal(purpose, rowID, []byte(plain))
	if err != nil {
		return nil, fmt.Errorf("seal %s: %w", purpose, err)
	}
	return b, nil
}

// Unseal opens a value seal stored; NULL reads as "". A value stored before
// a keyring was configured is returned as it is.
func (s *Store) Unseal(purpose, rowID string, stored []byte) (string, error) {
	if len(stored) == 0 {
		return "", nil
	}
	if !crypt.IsSealed(stored) {
		return string(stored), nil
	}
	if s.keys == nil {
		return "", fmt.Errorf("open %s %q: %w", purpose, rowID, ErrNoKeyring)
	}
	b, err := s.keys.Open(purpose, rowID, stored)
	if err != nil {
		return "", fmt.Errorf("open %s %q: %w", purpose, rowID, err)
	}
	return string(b), nil
}

// Micros is a time column value: Unix microseconds, NULL for a zero time.
func Micros(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMicro()
}

// FromMicros reads a time column.
func FromMicros(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.UnixMicro(v.Int64).UTC()
}

// Millis is a duration column value in milliseconds.
func Millis(d time.Duration) int64 { return d.Milliseconds() }

// FromMillis reads a duration column.
func FromMillis(ms int64) time.Duration { return time.Duration(ms) * time.Millisecond }

// ToJSON encodes a document column; nil and empty values store as NULL.
func ToJSON(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode document: %w", err)
	}
	if s := string(b); s == "null" || s == "{}" || s == "[]" || s == `""` {
		return nil, nil //nolint:nilnil // a nil value binds as SQL NULL
	}
	return string(b), nil
}

// MustJSON is ToJSON for documents built from the domain types, which are
// structs, slices and string-keyed maps of plain values that encoding/json
// cannot fail to encode. An error therefore means a field of an unsupported
// kind was added to a type, a programming mistake that must fail loudly:
// storing NULL instead would lose the document without a trace.
func MustJSON(v any) any {
	doc, err := ToJSON(v)
	if err != nil {
		panic(fmt.Sprintf("sqlcommon: %v", err))
	}
	return doc
}

// FromJSON decodes a document column; NULL leaves into untouched.
func FromJSON(doc sql.NullString, into any) error {
	if !doc.Valid || doc.String == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(doc.String), into); err != nil {
		return fmt.Errorf("decode document: %w", err)
	}
	return nil
}

// RawJSON stores an opaque JSON (or other) document given as bytes; empty
// stores as NULL.
func RawJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

// FromRaw reads an opaque document column.
func FromRaw(doc sql.NullString) []byte {
	if !doc.Valid || doc.String == "" {
		return nil
	}
	return []byte(doc.String)
}

// NullString stores "" as NULL, for optional foreign keys and unique
// columns where an empty value must not collide.
func NullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// Keyset is the condition that continues a list after the cursor in p, for
// a sort over cols (ascending, or descending when desc). Columns sorted as
// integers must be listed in ints so their cursor parts bind as integers.
// It returns "1 = 1" for the first page.
func Keyset(p kernel.Page, cols []string, ints map[string]bool, desc bool) (string, []any, error) {
	parts, err := p.Key(len(cols))
	if err != nil || parts == nil {
		return "1 = 1", nil, err //nolint:wrapcheck // already a kernel sentinel
	}
	vals := make([]any, 0, len(parts))
	for i, col := range cols {
		part := parts[i] // Key returned exactly len(cols) parts
		if !ints[col] {
			vals = append(vals, part)
			continue
		}
		n, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return "", nil, fmt.Errorf("%w: %q is not an integer", kernel.ErrInvalidCursor, part)
		}
		vals = append(vals, n)
	}
	op := ">"
	if desc {
		op = "<"
	}
	// (a > ?) OR (a = ? AND b > ?) OR (a = ? AND b = ? AND c > ?) ...
	var ors []string
	var args []any
	for i, col := range cols {
		var ands []string
		for j, prev := range cols[:i] {
			ands = append(ands, prev+" = ?")
			args = append(args, vals[j])
		}
		ands = append(ands, col+" "+op+" ?")
		args = append(args, vals[i])
		ors = append(ors, "("+strings.Join(ands, " AND ")+")")
	}
	return "(" + strings.Join(ors, " OR ") + ")", args, nil
}

// OrderBy renders ORDER BY cols in one direction.
func OrderBy(cols []string, desc bool) string {
	dir := ""
	if desc {
		dir = " DESC"
	}
	return " ORDER BY " + strings.Join(cols, dir+", ") + dir
}

// Itoa formats an integer cursor part.
func Itoa(n int64) string { return strconv.FormatInt(n, 10) }

// LimitOr applies the default limit for "at most limit" queries.
func LimitOr(limit int) int {
	if limit <= 0 {
		return kernel.DefaultLimit
	}
	return min(limit, kernel.MaxLimit)
}

// HasKeyring reports whether the store was given a keyring.
func (s *Store) HasKeyring() bool { return s.keys != nil }

// ResealValue re-seals one value, sealing a plain one for the first time.
func (s *Store) ResealValue(purpose, rowID string, v []byte) ([]byte, bool, error) {
	if !crypt.IsSealed(v) {
		sealed, err := s.keys.Seal(purpose, rowID, v)
		return sealed, err == nil, err //nolint:wrapcheck // wrapped by the caller
	}
	return s.keys.Reseal(purpose, rowID, v) //nolint:wrapcheck // wrapped by the caller
}
