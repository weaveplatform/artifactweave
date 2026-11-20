package tx

import (
	"context"
	"errors"
	"fmt"
)

// Middleware surrounds ordinary business work in a new root transaction, after
// backend binding/rollback setup and before before-commit finalizers. It may
// refuse before next; otherwise it calls next exactly once with the root's
// context. Nested joins do not invoke it again. It must perform no network I/O.
type Middleware func(context.Context, func(context.Context) error) error

// ErrMiddleware reports a joined SQL transaction whose root did not install
// this backend's configured middleware. A separately wrapped pool must not
// silently bypass a composition's transaction-wide publication invariants.
var ErrMiddleware = errors.New("transaction middleware was not installed by the root")

// SetPublicationCheck binds ordinary-work settlement for this transaction.
// The root composition supplies a repeatable check that does nothing while
// clean. Registration belongs to the same owning queue that Tx rebinds on a
// nested join, so an independent inner store cannot shadow the outer check.
// A second registration is refused. This is not permission to publish.
func SetPublicationCheck(ctx context.Context, check func(context.Context) error) error {
	f, _ := ctx.Value(finalizersKey{}).(*Finalizers)
	if f == nil {
		return ErrBeforeCommit
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.started || f.closed || check == nil || f.publication != nil {
		err := fmt.Errorf("%w: publication check registration unavailable", ErrBeforeCommit)
		f.err = errors.Join(f.err, err)
		return err
	}
	f.publication = check
	return nil
}

// CheckPublication settles pending local work before a final publication fence.
// It runs no callback when no composition installed one. A callback can enqueue
// durable work and register before-commit facts here, while ordinary work is
// still open; failures must propagate to the owning transaction's rollback.
func CheckPublication(ctx context.Context) error {
	f, _ := ctx.Value(finalizersKey{}).(*Finalizers)
	if f == nil {
		return nil
	}
	f.mu.Lock()
	check := f.publication
	f.mu.Unlock()
	if check == nil {
		return nil
	}
	if err := check(ctx); err != nil {
		return fmt.Errorf("transaction publication check: %w", err)
	}
	return nil
}
