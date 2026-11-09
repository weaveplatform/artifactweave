// Bridge (P9d): declared in migration/releases/core/port; PRO-02 deletes it.

package store

import relport "github.com/weaveplatform/artifactweave/migration/releases/core/port"

type (
	BundleFilter      = relport.BundleFilter
	BundleStore       = relport.BundleStore
	DistributionStore = relport.DistributionStore
)
