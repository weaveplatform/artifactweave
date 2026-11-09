// Bridge (P9d): declared in migration/retention/api; PRO-02 deletes it.

package types

import retentionapi "github.com/weaveplatform/artifactweave/migration/retention/api"

type RunStatus = retentionapi.RunStatus

const (
	RunInProgress           = retentionapi.RunInProgress
	RunInProgressWithErrors = retentionapi.RunInProgressWithErrors
	RunCompleted            = retentionapi.RunCompleted
	RunCompletedWithErrors  = retentionapi.RunCompletedWithErrors
	RunStopped              = retentionapi.RunStopped
	RunStopping             = retentionapi.RunStopping
	RunFailed               = retentionapi.RunFailed
	RunPendingWarm          = retentionapi.RunPendingWarm
	RunPartialWarmed        = retentionapi.RunPartialWarmed
	RunWarmed               = retentionapi.RunWarmed
)
