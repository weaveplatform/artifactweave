package port

import "github.com/weaveplatform/artifactweave/internal/platform/tx"

// Store is the part of the metadata store the workers module uses: worker
// definitions, secrets and executions, with the shared unit of work
// (tx.Runner).
type Store interface {
	tx.Runner
	Workers() WorkerStore
}
