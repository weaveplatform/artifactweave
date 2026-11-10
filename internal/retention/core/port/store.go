package port

import "github.com/weaveplatform/artifactweave/internal/platform/tx"

// Store is the part of the metadata store the retention module uses: its
// cleanup policies and runs, and its archive policies and executions, with
// the shared unit of work (tx.Runner). The archived items and the cold
// indexes those executions fill are artifacts' records, reached through the
// metadata store's own accessors (decision 0024).
type Store interface {
	tx.Runner
	Cleanup() CleanupStore
	Archive() ArchiveStore
}
