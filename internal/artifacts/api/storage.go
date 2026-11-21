package api

import "context"

// StorageUsage supplies metadata-only logical byte totals for guarded quota
// initialization and ownership changes. Item-backed cache is counted through
// items; raw positive cache bodies contribute separately, even when a later
// negative response retains them. Virtual-owned transformed copies contribute
// their own retained lengths. Shares do not transfer canonical ownership.
// These descriptions reserve no capacity and grant no publication authority.
type StorageUsage interface {
	// The empty project key describes unassigned repositories, not all projects.
	ProjectStorageBytes(context.Context, string) (int64, error)
	// Repository totals include retained item rows even without configuration,
	// so delete/recreate cannot hide orphaned content from accounting.
	RepositoryStorageBytes(context.Context, string) (int64, error)
}

// StorageChange records actual committed-row bytes inside the same metadata
// transaction as the mutation. Content changes pass a signed delta; ownership
// changes pass the nonnegative remaining repository total. Nil means no owner
// needs accounting. Recording is not the transaction's commit or authorization.
type StorageChange func(context.Context, int64) error

// StorageOwnership identifies a canonical assignment change without opening
// repository credentials. Empty project keys denote unassigned content.
type StorageOwnership struct {
	Repository, PreviousProject, Project string
}

// StorageWatch prepares transaction-local accounting before item/source locks.
// The composition supplies it over its shared root transaction middleware;
// preparation captures the current project lifetime and pre-mutation baseline.
// Returned changes record actual deltas for settlement after the whole business
// operation, so net-zero moves do not require temporary extra capacity. Calls
// are serial within the metadata transaction and perform no network I/O.
type StorageWatch interface {
	Content(context.Context, string) (StorageChange, error)
	Ownership(context.Context, StorageOwnership) (StorageChange, error)
}
