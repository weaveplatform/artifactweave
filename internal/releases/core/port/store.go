package port

import "github.com/weaveplatform/artifactweave/internal/platform/tx"

// Store is the part of the metadata store the releases module uses: release
// bundles and their distribution, with the shared unit of work (tx.Runner).
type Store interface {
	tx.Runner
	Bundles() BundleStore
	Distribution() DistributionStore
}
