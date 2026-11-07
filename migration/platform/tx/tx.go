package tx

import "context"

// Runner runs a unit of work. Store calls made with the context fn receives
// take part; an error from fn rolls all of them back. A nested Tx joins the
// outer transaction.
//
// Tx returns fn's error, or an error that wraps it, after the rollback. A Tx
// called outside a transaction fails without calling fn when ctx has ended.
// A nested Tx is one called with a context that is already inside a
// transaction of the same Runner: fn runs in that transaction at once, and
// the outer commit or rollback decides for both. An inner error that the
// outer fn drops is the one case the implementations treat differently, on
// purpose: the SQL unit of work still fails the outer transaction, and
// platform/tx/memtx commits it. Callers therefore return inner errors, which
// is the case the metadata store's contract suite checks. Implementations are
// safe for concurrent use.
type Runner interface {
	// Tx runs fn as one transaction, as Runner describes.
	Tx(ctx context.Context, fn func(ctx context.Context) error) error
	// AfterCommit defers fn until the enclosing transaction commits, or runs
	// it at once outside a transaction. Nothing runs after a rollback.
	// Deferred functions run in the order they were deferred, after the
	// commit has finished.
	AfterCommit(ctx context.Context, fn func())
}
