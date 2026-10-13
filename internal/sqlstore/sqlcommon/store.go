package sqlcommon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/weaveplatform/weaveplatform-artifact-registry/internal/crypt"
	"github.com/weaveplatform/weaveplatform-artifact-registry/pkg/store"
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

var _ store.Store = (*Store)(nil)

// Options configures Open.
type Options struct {
	// SkipMigrate opens without applying migrations.
	SkipMigrate bool
	// Keyring seals secret columns. Without one they are stored in plain
	// text, which only tests should do; the server always configures one.
	Keyring *crypt.Keyring
}

// Open pings db, applies pending migrations unless o.SkipMigrate, and wraps
// it. It closes db on failure. Each backend opens its pool and calls it.
func Open(ctx context.Context, db *sql.DB, d Dialect, o Options) (*Store, error) {
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect: %w", err)
	}
	if !o.SkipMigrate {
		if _, err := Migrate(ctx, db, d); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return New(db, d, o.Keyring), nil
}

// New wraps an open, migrated pool.
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

// Close implements store.Store.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("%s: close: %w", s.d.Name, err)
	}
	return nil
}

// q returns the transaction ctx carries, or the pool.
func (s *Store) q(ctx context.Context) Queryer { return query(ctx, s.db) }

// wrap maps driver errors onto the store sentinels and names the operation.
func (s *Store) wrap(op string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%s: %w", op, store.ErrNotFound)
	case s.d.uniqueViolation(err):
		return fmt.Errorf("%s: %w", op, store.ErrConflict)
	default:
		return fmt.Errorf("%s: %s: %w", s.d.Name, op, err)
	}
}

// exec runs a statement written with "?" placeholders and returns the
// number of rows it changed.
func (s *Store) exec(ctx context.Context, op, q string, args ...any) (int64, error) {
	res, err := s.q(ctx).ExecContext(ctx, s.d.Rebind(q), args...)
	if err != nil {
		return 0, s.wrap(op, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, s.wrap(op, err)
	}
	return n, nil
}

// execOne runs a statement that must change exactly one row; ErrNotFound
// when it changed none.
func (s *Store) execOne(ctx context.Context, op, q string, args ...any) error {
	n, err := s.exec(ctx, op, q, args...)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%s: %w", op, store.ErrNotFound)
	}
	return nil
}

// insertID runs an INSERT ... RETURNING id and returns the new id.
func (s *Store) insertID(ctx context.Context, op, q string, args ...any) (int64, error) {
	var id int64
	if err := s.q(ctx).
		QueryRowContext(ctx, s.d.Rebind(q+" RETURNING id"), args...).
		Scan(&id); err != nil {
		return 0, s.wrap(op, err)
	}
	return id, nil
}

// exists reports whether q (a SELECT 1 ...) returns a row.
func (s *Store) exists(ctx context.Context, op, q string, args ...any) (bool, error) {
	var one int
	err := s.q(ctx).QueryRowContext(ctx, s.d.Rebind(q), args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, s.wrap(op, err)
	}
	return true, nil
}

// rows runs a query and calls scan for each row.
func (s *Store) rows(
	ctx context.Context,
	op, q string,
	args []any,
	scan func(*sql.Rows) error,
) error {
	rs, err := s.q(ctx).QueryContext(ctx, s.d.Rebind(q), args...)
	if err != nil {
		return s.wrap(op, err)
	}
	defer func() { _ = rs.Close() }()
	for rs.Next() {
		if err := scan(rs); err != nil {
			return s.wrap(op, err)
		}
	}
	return s.wrap(op, rs.Err())
}

// lockClause returns the row-lock suffix for a SELECT inside a transaction,
// or "" outside one and on engines that serialise writers.
func (s *Store) lockClause(ctx context.Context) string {
	if s.d.ForUpdate == "" || !inTx(ctx, s.db) {
		return ""
	}
	return " " + s.d.ForUpdate
}

// seal seals a secret for one column of one row; "" is stored as NULL.
func (s *Store) seal(purpose, rowID, plain string) ([]byte, error) {
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

// unseal opens a value seal stored; NULL reads as "". A value stored before
// a keyring was configured is returned as it is.
func (s *Store) unseal(purpose, rowID string, stored []byte) (string, error) {
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

// micros is a time column value: Unix microseconds, NULL for a zero time.
func micros(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMicro()
}

// fromMicros reads a time column.
func fromMicros(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.UnixMicro(v.Int64).UTC()
}

// millis is a duration column value in milliseconds.
func millis(d time.Duration) int64 { return d.Milliseconds() }

// fromMillis reads a duration column.
func fromMillis(ms int64) time.Duration { return time.Duration(ms) * time.Millisecond }

// toJSON encodes a document column; nil and empty values store as NULL.
func toJSON(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode document: %w", err)
	}
	if s := string(b); s == "null" || s == "{}" || s == "[]" || s == `""` {
		return nil, nil //nolint:nilnil // a nil value binds as SQL NULL
	}
	return string(b), nil
}

// mustJSON is toJSON for documents built from the domain types, which are
// structs, slices and string-keyed maps of plain values that encoding/json
// cannot fail to encode. An error therefore means a field of an unsupported
// kind was added to a type, a programming mistake that must fail loudly:
// storing NULL instead would lose the document without a trace.
func mustJSON(v any) any {
	doc, err := toJSON(v)
	if err != nil {
		panic(fmt.Sprintf("sqlcommon: %v", err))
	}
	return doc
}

// fromJSON decodes a document column; NULL leaves into untouched.
func fromJSON(doc sql.NullString, into any) error {
	if !doc.Valid || doc.String == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(doc.String), into); err != nil {
		return fmt.Errorf("decode document: %w", err)
	}
	return nil
}

// rawJSON stores an opaque JSON (or other) document given as bytes; empty
// stores as NULL.
func rawJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

// fromRaw reads an opaque document column.
func fromRaw(doc sql.NullString) []byte {
	if !doc.Valid || doc.String == "" {
		return nil
	}
	return []byte(doc.String)
}

// nullString stores "" as NULL, for optional foreign keys and unique
// columns where an empty value must not collide.
func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// keyset is the condition that continues a list after the cursor in p, for
// a sort over cols (ascending, or descending when desc). Columns sorted as
// integers must be listed in ints so their cursor parts bind as integers.
// It returns "1 = 1" for the first page.
func keyset(p store.Page, cols []string, ints map[string]bool, desc bool) (string, []any, error) {
	parts, err := p.Key(len(cols))
	if err != nil || parts == nil {
		return "1 = 1", nil, err //nolint:wrapcheck // already a store sentinel
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
			return "", nil, fmt.Errorf("%w: %q is not an integer", store.ErrInvalidCursor, part)
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

// orderBy renders ORDER BY cols in one direction.
func orderBy(cols []string, desc bool) string {
	dir := ""
	if desc {
		dir = " DESC"
	}
	return " ORDER BY " + strings.Join(cols, dir+", ") + dir
}

// itoa formats an integer cursor part.
func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// limitOr applies the default limit for "at most limit" queries.
func limitOr(limit int) int {
	if limit <= 0 {
		return store.DefaultLimit
	}
	return min(limit, store.MaxLimit)
}
