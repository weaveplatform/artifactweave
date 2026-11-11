package api

// RunStatus is the status of a cleanup or smart-archiving run.
type RunStatus string

// Run statuses as JFrog reports them.
const (
	RunInProgress           RunStatus = "IN_PROGRESS"
	RunInProgressWithErrors RunStatus = "IN_PROGRESS_WITH_ERRORS"
	RunCompleted            RunStatus = "COMPLETED"
	RunCompletedWithErrors  RunStatus = "COMPLETED_WITH_ERRORS"
	RunStopped              RunStatus = "STOPPED"
	RunStopping             RunStatus = "STOPPING"
	RunFailed               RunStatus = "FAILED"
	RunPendingWarm          RunStatus = "PENDING_WARM"
	RunPartialWarmed        RunStatus = "PARTIAL_WARMED"
	RunWarmed               RunStatus = "WARMED"
)

// Terminal reports whether a run in this status will not change again.
func (s RunStatus) Terminal() bool {
	switch s {
	case RunCompleted, RunCompletedWithErrors, RunStopped, RunFailed:
		return true
	default:
		return false
	}
}
