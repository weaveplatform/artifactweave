package tx

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrBeforeCommit reports registration outside an open transaction or an
// attempt to add work while that transaction is finalizing.
var ErrBeforeCommit = errors.New("tx: before-commit registration unavailable")

type finalizersKey struct{}

// BeforeCommit defers fn until the owning transaction has finished its
// business writes, but before it commits. An error rolls back the transaction.
// Unlike AfterCommit, it never runs fn outside a transaction. Capture immutable
// facts before registering: fn must not acquire domain locks after another
// finalizer may have acquired the event stream lock.
//
// Registration during finalization fails the transaction even if the caller
// drops the error. All work must finish before Tx returns; callbacks must not
// retain the transaction context for later background work.
func BeforeCommit(ctx context.Context, fn func(context.Context) error) error {
	f, _ := ctx.Value(finalizersKey{}).(*Finalizers)
	if f == nil {
		return ErrBeforeCommit
	}
	return f.add(fn)
}

// Finalizers owns one concrete transaction's ordered before-commit work.
// Its zero value is ready for use. Transaction backends bind it on every Tx
// entry, including joins, Run it only after successful business work, and
// defer Close on every exit. Runner stays small so other implementations need
// not support before-commit work; BeforeCommit fails explicitly on those.
type Finalizers struct {
	mu      sync.Mutex
	started bool
	closed  bool
	err     error
	before  []func(context.Context) error
}

// Bind returns a context whose before-commit work belongs to f. A joined
// transaction rebinds its own queue because a nested different store may
// have shadowed the nearest registrar.
func (f *Finalizers) Bind(ctx context.Context) context.Context {
	return context.WithValue(ctx, finalizersKey{}, f)
}

func (f *Finalizers) add(fn func(context.Context) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var reason string
	switch {
	case f.closed:
		reason = "transaction closed"
	case f.started:
		reason = "transaction finalizing"
	case fn == nil:
		reason = "nil callback"
	default:
		f.before = append(f.before, fn)
		return nil
	}
	err := fmt.Errorf("%w: %s", ErrBeforeCommit, reason)
	// A callback must not hide invalid reentrant registration and then commit.
	f.err = errors.Join(f.err, err)
	return err
}

// Run executes the registered callbacks in order, stopping at the first
// failure. Callbacks use the owning transaction's context and run without
// the queue mutex. A callback may join Tx but cannot register more finalizers.
func (f *Finalizers) Run(ctx context.Context) error {
	f.mu.Lock()
	if f.started || f.closed {
		f.mu.Unlock()
		return fmt.Errorf("%w: finalization already ended or started", ErrBeforeCommit)
	}
	f.started = true
	callbacks := f.before
	f.before = nil
	err := f.err
	f.mu.Unlock()
	defer f.Close()
	if err != nil {
		return err
	}
	ctx = f.Bind(ctx)
	for _, fn := range callbacks {
		err = fn(ctx)
		f.mu.Lock()
		err = errors.Join(err, f.err)
		f.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

// Close rejects registrations and releases queued callbacks after commit,
// rollback or panic. It is safe to call more than once.
func (f *Finalizers) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	f.before = nil
}
