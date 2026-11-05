package types

import "time"

// CleanupItemType is what a cleanup policy deletes.
type CleanupItemType string

// Cleanup item types.
const (
	CleanupPackage       CleanupItemType = "package"
	CleanupBuild         CleanupItemType = "build"
	CleanupReleaseBundle CleanupItemType = "releaseBundle"
)

// CleanupCriteria select what a package cleanup policy removes. Stored as the
// policy's document; the legacy month fields are kept so a policy written by
// an old client round-trips.
type CleanupCriteria struct {
	PackageTypes                 []string            `json:"package_types,omitempty"`
	Repos                        []string            `json:"repos,omitempty"`
	ExcludedRepos                []string            `json:"excluded_repos,omitempty"`
	IncludedPackages             []string            `json:"included_packages,omitempty"`
	ExcludedPackages             []string            `json:"excluded_packages,omitempty"`
	IncludeAllProjects           bool                `json:"include_all_projects,omitempty"`
	IncludedProjects             []string            `json:"included_projects,omitempty"`
	CreatedBeforeInDays          int                 `json:"created_before_in_days,omitempty"`
	CreatedBeforeInMonths        int                 `json:"created_before_in_months,omitempty"`
	LastDownloadedBeforeInDays   int                 `json:"last_downloaded_before_in_days,omitempty"`
	LastDownloadedBeforeInMonths int                 `json:"last_downloaded_before_in_months,omitempty"`
	KeepLastNVersions            int                 `json:"keep_last_n_versions,omitempty"`
	IncludedProperties           map[string][]string `json:"included_properties,omitempty"`
	ExcludedProperties           map[string][]string `json:"excluded_properties,omitempty"`
	IncludedPackageVersions      []string            `json:"included_package_versions,omitempty"`
	ExcludedPackageVersions      []string            `json:"excluded_package_versions,omitempty"`
}

// CleanupPolicy is a scheduled retention rule (decision 0010).
type CleanupPolicy struct {
	Key             string
	Description     string
	CronExp         string
	ProjectKey      string
	ItemType        CleanupItemType
	DurationMinutes int
	Enabled         bool
	SkipTrashcan    bool
	Criteria        CleanupCriteria
	Created         time.Time
	CreatedBy       string
	Updated         time.Time
	UpdatedBy       string
}

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

// CleanupRun is one execution of a cleanup policy.
type CleanupRun struct {
	ID           int64
	PolicyKey    string
	TriggerID    int64
	ItemType     CleanupItemType
	Status       RunStatus
	DryRun       bool
	Manual       bool
	TriggeredBy  string
	Started      time.Time
	Ended        time.Time
	TotalItems   int64
	CleanedItems int64
	CleanedSize  int64
	Report       []byte
}
