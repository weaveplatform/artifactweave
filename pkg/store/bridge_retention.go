// Bridge (P9d): declared in migration/retention/core/port; PRO-02 deletes it.

package store

import retport "github.com/weaveplatform/artifactweave/migration/retention/core/port"

type (
	ExecutionFilter = retport.ExecutionFilter
	ArchiveStore    = retport.ArchiveStore
	PolicyFilter    = retport.PolicyFilter
	RunFilter       = retport.RunFilter
	CleanupStore    = retport.CleanupStore
)
