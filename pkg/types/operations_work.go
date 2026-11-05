package types

import "time"

// Lease is a named lock on background work (cleanup runs, replication, GC,
// archive runs). The scheduler takes one before a run so a job never
// overlaps itself, and the locks endpoint reports the table.
type Lease struct {
	Name     string
	Holder   string
	Task     string
	Acquired time.Time
	Expires  time.Time
}

// Held reports whether the lease is still in force at now.
func (l Lease) Held(now time.Time) bool { return now.Before(l.Expires) }

// WorkState is where an item of the persistent work queue stands (decision
// 0033).
type WorkState string

// Work item states.
const (
	// WorkPending waits for a worker, from NotBefore on.
	WorkPending WorkState = "pending"
	// WorkLeased is being run by LeasedBy until LeaseUntil.
	WorkLeased WorkState = "leased"
	// WorkParked failed too often and waits for a fresh enqueue.
	WorkParked WorkState = "parked"
)

// WorkItem is one unit of asynchronous derived work (decision 0033). There
// is one item per Kind and Key, so work asked for again while an item waits
// coalesces into the run it is waiting for.
type WorkItem struct {
	Kind string
	Key  string
	// Payload is a small JSON document from the latest enqueue.
	Payload []byte
	State   WorkState
	// Attempts counts the runs started since the item was last enqueued
	// afresh; it is what parks a key that keeps failing.
	Attempts int
	// Generation increases with every lease, so a worker whose lease ran
	// out cannot complete or fail the run another worker took over.
	Generation int64
	// NotBefore is when a pending item becomes due.
	NotBefore  time.Time
	LeasedBy   string
	LeaseUntil time.Time
	// DirtySince is when work was asked for again during the current run,
	// which then runs once more; zero when it was not.
	DirtySince time.Time
	LastError  string
	// Enqueued is when the work the item now owes was first asked for.
	Enqueued time.Time
	Updated  time.Time
}

// WorkStats summarises the queue for one kind, for metrics.
type WorkStats struct {
	Kind                    string
	Pending, Leased, Parked int64
	// OldestPending is the earliest Enqueued of a pending item; zero when
	// none is pending.
	OldestPending time.Time
}
