package model

import (
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
)

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
	IncludeAllStages             *bool               `json:"include_all_stages,omitempty"`
	IncludedStages               []string            `json:"included_stages,omitempty"`
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
	Key string
	// Revision fences queued work across edits, disablement and recreation.
	// It is private state, not a field of the vendor policy representation.
	Revision string
	// ProjectIncarnation binds this policy to one project lifetime, even when
	// a platform administrator could administer a later project with that key.
	ProjectIncarnation string `json:"-"`
	// ExecutionCredential retains the verified enabling/updating caller, never
	// a password, bearer token or cached administrator flag. A scheduled run
	// must refresh this reference; CreatedBy/UpdatedBy alone grant no authority.
	ExecutionCredential kernel.CredentialReference `json:"-"`
	Description         string
	CronExp             string
	ProjectKey          string
	ItemType            CleanupItemType
	DurationMinutes     int
	Enabled             bool
	SkipTrashcan        bool
	Criteria            CleanupCriteria
	Created             time.Time
	CreatedBy           string
	Updated             time.Time
	UpdatedBy           string
}

// CleanupRun is one execution of a cleanup policy.
type CleanupRun struct {
	ID               int64
	PolicyKey        string
	TriggerID        int64
	ItemType         CleanupItemType
	Status           api.RunStatus
	DryRun           bool
	Manual           bool
	TriggeredBy      string
	Started          time.Time
	Ended            time.Time
	TotalItems       int64
	CleanedItems     int64
	CleanedSize      int64
	Report           []byte
	ReportDownloaded bool
}

// CleanupRunSnapshot pairs public run facts with the policy it actually used.
// It contains no execution credential or project-lifetime capability. Later
// policy edits, deletion or reuse of its key cannot change this history.
type CleanupRunSnapshot struct {
	Run    CleanupRun
	Policy CleanupPolicy
	// CleanedContentBytes deduplicates actual acknowledged content. Run's
	// private CleanedSize retains the per-item total used by the executor.
	CleanedContentBytes int64
}

// CleanupSnapshot removes private execution bindings from a history projection.
func CleanupSnapshot(run CleanupRun, policy CleanupPolicy) CleanupRunSnapshot {
	run.Report = nil // legacy report blobs are not part of bounded run metadata
	policy.ExecutionCredential, policy.ProjectIncarnation = kernel.CredentialReference{}, ""
	return CleanupRunSnapshot{Run: run, Policy: policy}
}

// CleanupExecution is private durable run state, separate from report bytes.
// The frozen policy and clock survive policy edits/deletion and process restart.
// Occurrence identifies a scheduler fire; empty denotes a distinct manual run.
// Neither this state nor its credential reference is a public report payload.
type CleanupExecution struct {
	RunID              int64  `json:"-"`
	Occurrence         string `json:"-"`
	Policy             CleanupPolicy
	Credential         kernel.CredentialReference
	ProjectIncarnation string
	At                 time.Time
}
