package tx

import "context"

// Runner runs a unit of work. Store calls made with the context fn receives
// take part; an error from fn rolls all of them back. A nested Tx joins the
// outer transaction.
type Runner interface {
	Tx(ctx context.Context, fn func(ctx context.Context) error) error
	// AfterCommit defers fn until the enclosing transaction commits, or runs
	// it at once outside a transaction. Nothing runs after a rollback.
	AfterCommit(ctx context.Context, fn func())
}
