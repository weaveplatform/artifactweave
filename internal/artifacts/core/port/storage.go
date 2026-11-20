package port

import "context"

// StorageUsage reads logical bytes from existing metadata, without opening
// repository credentials or enumerating blob contents. Item-backed cache
// entries contribute through their items; raw cache bodies contribute once.
// Shares do not transfer ownership. These descriptive totals grant no write
// authority and reserve no capacity; the caller's project guard and transaction
// keep initialization and subsequent accounting mutations consistent.
type StorageUsage interface {
	// ProjectStorageBytes sums current canonical repository ownership. The
	// empty project key describes unassigned repositories, not every project.
	ProjectStorageBytes(context.Context, string) (int64, error)
	// RepositoryStorageBytes includes existing rows at this key even if its
	// configuration is absent, so delete/recreate cannot hide orphaned items.
	RepositoryStorageBytes(context.Context, string) (int64, error)
}
