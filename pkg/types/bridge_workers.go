// Bridge (P9d): declared in migration/workers/core/model; PRO-02 deletes it.

package types

import wrkmodel "github.com/weaveplatform/artifactweave/migration/workers/core/model"

var ErrInvalidWorker = wrkmodel.ErrInvalidWorker

var (
	ValidateWorkerSource = wrkmodel.ValidateWorkerSource
	ValidateWorkerState  = wrkmodel.ValidateWorkerState
)

type (
	WorkerAction    = wrkmodel.WorkerAction
	WorkerFilter    = wrkmodel.WorkerFilter
	Worker          = wrkmodel.Worker
	WorkerTrigger   = wrkmodel.WorkerTrigger
	WorkerExecution = wrkmodel.WorkerExecution
)

const (
	MaxWorkerSource     = wrkmodel.MaxWorkerSource
	MaxWorkerStateItems = wrkmodel.MaxWorkerStateItems
	MaxWorkerStateKey   = wrkmodel.MaxWorkerStateKey
	MaxWorkerStateValue = wrkmodel.MaxWorkerStateValue
	TriggerEvent        = wrkmodel.TriggerEvent
	TriggerHTTP         = wrkmodel.TriggerHTTP
	TriggerTest         = wrkmodel.TriggerTest
	TriggerRerun        = wrkmodel.TriggerRerun
)
