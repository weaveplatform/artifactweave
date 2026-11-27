package api

import "context"

// CleanupReservation identifies a prepared per-file destructive operation.
// The consumer projects its own local artifact binding from the immutable
// evidence. GuardReservation still checks the durable intent in the same
// transaction as all native companion effects; evidence alone grants no delete.
type CleanupReservation struct {
	ID       string
	Evidence CleanupEvidence
}

// CleanupCoordination extends observation with durable exclusion. Reserve and
// Commit join the caller's guarded metadata transaction. Prepare and Finish own
// their short transactions and make peer calls only between them. Abort records
// an irreversible fenced refusal; it cannot undo or relabel a committed effect.
// A caller persists the reservation ID before Prepare so lease replacement and
// crash recovery can settle it before advancing the same cleanup entry.
type CleanupCoordination interface {
	Cleanup
	Reserve(context.Context, CleanupEvidence, int64, int64) (string, error)
	Prepare(context.Context, string) (CleanupReservation, error)
	GuardReservation(context.Context, string) error
	Commit(context.Context, string) error
	Abort(context.Context, string) error
	Finish(context.Context, string) error
}
