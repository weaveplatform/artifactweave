package dialect_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
)

// errFault is what a failing step of the fault driver returns.
var errFault = errors.New("fault")

// faults names the steps of the fault driver that fail. The others succeed:
// a statement changes one row and a query returns no rows, or with oneRow
// one row of its one column, which null makes NULL. query fails the query
// itself. When on is set, exec, query, next and oneRow apply only to the
// statements that contain it, so one statement can fail while the ones
// before it succeed.
type faults struct {
	exec, begin, commit, next, rowsAffected, close bool
	oneRow, null, query                            bool
	on                                             string
}

// hits reports whether the faults that on limits apply to statement.
func (f faults) hits(statement string) bool {
	return f.on == "" || strings.Contains(statement, f.on)
}

// faultDB opens a pool over the fault driver, for the failures SQLite cannot
// be made to produce on demand.
func faultDB(t *testing.T, f faults) *sql.DB {
	t.Helper()
	db := sql.OpenDB(faultConnector{f})
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func failIf(fail bool) error {
	if fail {
		return errFault
	}
	return nil
}

type faultConnector struct{ f faults }

func (c faultConnector) Connect(context.Context) (driver.Conn, error) {
	return faultConn(c), nil
}

func (c faultConnector) Driver() driver.Driver { return faultDriver(c) }

// Close is called by sql.DB.Close, which returns its error.
func (c faultConnector) Close() error { return failIf(c.f.close) }

type faultDriver struct{ f faults }

func (d faultDriver) Open(string) (driver.Conn, error) { return faultConn(d), nil }

type faultConn struct{ f faults }

func (faultConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("the fault driver runs statements without preparing them")
}

func (faultConn) Close() error { return nil }

func (c faultConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c faultConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	if c.f.begin {
		return nil, errFault
	}
	return faultTx(c), nil
}

func (c faultConn) ExecContext(
	_ context.Context,
	statement string,
	_ []driver.NamedValue,
) (driver.Result, error) {
	if c.f.exec && c.f.hits(statement) {
		return nil, errFault
	}
	return faultResult(c), nil
}

func (c faultConn) QueryContext(
	_ context.Context,
	statement string,
	_ []driver.NamedValue,
) (driver.Rows, error) {
	if c.f.query && c.f.hits(statement) {
		return nil, errFault
	}
	return &faultRows{f: c.f, hit: c.f.hits(statement)}, nil
}

type faultTx struct{ f faults }

func (t faultTx) Commit() error { return failIf(t.f.commit) }

func (faultTx) Rollback() error { return nil }

type faultResult struct{ f faults }

func (faultResult) LastInsertId() (int64, error) { return 0, nil }

func (r faultResult) RowsAffected() (int64, error) {
	if r.f.rowsAffected {
		return 0, errFault
	}
	return 1, nil
}

type faultRows struct {
	f           faults
	hit, served bool
}

func (*faultRows) Columns() []string { return []string{"version"} }

func (*faultRows) Close() error { return nil }

func (r *faultRows) Next(dest []driver.Value) error {
	switch {
	case r.f.next && r.hit:
		return errFault
	case r.f.oneRow && r.hit && !r.served:
		r.served = true
		dest[0] = int64(1)
		if r.f.null {
			dest[0] = nil
		}
		return nil
	}
	return io.EOF
}
