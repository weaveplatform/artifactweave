package sqlcommon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
)

// ErrTransaction reports a transaction that could not begin or commit.
var ErrTransaction = errors.New("sqlcommon: transaction failed")

// Queryer is a pool or the transaction a context carries.
type Queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type unitKey struct{}

// unit is one open transaction and what waits for its commit.
type unit struct {
	db     *sql.DB
	tx     *sql.Tx
	mu     sync.Mutex
	err    error
	commit []func()
}

func currentUnit(ctx context.Context) *unit {
	u, _ := ctx.Value(unitKey{}).(*unit)
	return u
}

// inTx reports whether ctx carries a transaction on db.
func inTx(ctx context.Context, db *sql.DB) bool {
	u := currentUnit(ctx)
	return u != nil && u.db == db
}

// query returns the transaction ctx carries for db, or db itself.
func query(ctx context.Context, db *sql.DB) Queryer {
	if u := currentUnit(ctx); u != nil && u.db == db {
		return u.tx
	}
	return db
}

// fail records err on the enclosing transaction so it rolls back even when
// an intermediate caller drops the error, and returns err.
func fail(ctx context.Context, err error) error {
	if u := currentUnit(ctx); u != nil && err != nil {
		u.mu.Lock()
		u.err = errors.Join(u.err, err)
		u.mu.Unlock()
	}
	return err
}

// afterCommit queues fn until the transaction ctx carries commits, or runs
// it at once outside one.
func afterCommit(ctx context.Context, fn func()) {
	if u := currentUnit(ctx); u != nil {
		u.mu.Lock()
		u.commit = append(u.commit, fn)
		u.mu.Unlock()
		return
	}
	fn()
}

// runTx runs fn in a transaction on db; a nested call joins the outer one,
// and its error poisons the outer transaction even if the caller drops it.
func runTx(ctx context.Context, db *sql.DB, d Dialect, fn func(context.Context) error) error {
	if u := currentUnit(ctx); u != nil && u.db == db {
		return fail(ctx, fn(ctx))
	}
	opts := &sql.TxOptions{Isolation: sql.LevelReadCommitted}
	if d.ForUpdate == "" {
		// SQLite has one writer and no isolation levels to choose; the
		// driver rejects any but the default.
		opts = nil
	}
	tx, err := db.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("%w: begin: %w", ErrTransaction, errors.Join(err, ctx.Err()))
	}
	u := &unit{db: db, tx: tx}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
			return
		}
		for _, f := range u.commit {
			f()
		}
	}()
	err = fn(context.WithValue(ctx, unitKey{}, u))
	u.mu.Lock()
	err = errors.Join(err, u.err)
	u.mu.Unlock()
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: commit: %w", ErrTransaction, errors.Join(err, ctx.Err()))
	}
	committed = true
	return nil
}
