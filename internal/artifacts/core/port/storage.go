package port

import "github.com/weaveplatform/artifactweave/internal/artifacts/api"

// StorageUsage reads logical bytes from existing metadata, without opening
// repository credentials or enumerating blob contents. Item-backed cache
// entries contribute through their items; raw cache bodies contribute once.
// Shares do not transfer ownership. These descriptive totals grant no write
// authority and reserve no capacity; the caller's project guard and transaction
// keep initialization and subsequent accounting mutations consistent.
type StorageUsage = api.StorageUsage
