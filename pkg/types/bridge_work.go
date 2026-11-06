// Bridge (P9d): declared in migration/platform/work; PRO-02 deletes it.

package types

import "github.com/weaveplatform/artifactweave/migration/platform/work"

type (
	Lease     = work.Lease
	WorkState = work.WorkState
	WorkItem  = work.WorkItem
	WorkStats = work.WorkStats
)

const (
	WorkPending = work.WorkPending
	WorkLeased  = work.WorkLeased
	WorkParked  = work.WorkParked
)
