package types

import (
	"time"

	"github.com/weaveplatform/artifactweave/pkg/repo"
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

// Replication is one replication target of a repository. Key is the
// server-assigned replicationKey; Password is sealed.
type Replication struct {
	RepoKey              string
	Key                  string
	URL                  string
	Username             string
	Password             string `json:"-"`
	Enabled              bool
	CronExp              string
	EventReplication     bool
	SyncDeletes          bool
	SyncProperties       bool
	SyncStatistics       bool
	IncludePathPrefix    string
	ExcludePathPrefix    string
	Proxy                string
	DisableProxy         bool
	CheckBinaryExistence bool
	SocketTimeout        time.Duration
}

// ReplicationDirection is derived from the repository class.
type ReplicationDirection string

// Replication directions.
const (
	ReplicationPush ReplicationDirection = "push"
	ReplicationPull ReplicationDirection = "pull"
)

// DirectionFor returns how a repository of class c replicates: locals push,
// remotes pull; other classes do not replicate.
func DirectionFor(c repo.Class) (ReplicationDirection, bool) {
	switch c {
	case repo.Local:
		return ReplicationPush, true
	case repo.Remote:
		return ReplicationPull, true
	default:
		return "", false
	}
}

// ReplicationStatus is the reported state of a repository's replication.
type ReplicationStatus string

// Replication statuses.
const (
	ReplicationUnknown        ReplicationStatus = "unknown"
	ReplicationNeverRun       ReplicationStatus = "never_run"
	ReplicationIncomplete     ReplicationStatus = "incomplete"
	ReplicationError          ReplicationStatus = "error"
	ReplicationWarn           ReplicationStatus = "warn"
	ReplicationOK             ReplicationStatus = "ok"
	ReplicationInconsistent   ReplicationStatus = "inconsistent"
	ReplicationPartialFailure ReplicationStatus = "partial_failure"
)

// ReplicationRun records one execution of a replication, for the status
// endpoint. A zero Completed means the run is still going.
type ReplicationRun struct {
	ReplicationKey string
	Started        time.Time
	Completed      time.Time
	Status         ReplicationStatus
}

// Lease is a named lock on background work (cleanup runs, replication, GC,
// archive runs). The scheduler takes one before a run so a job never
// overlaps itself, and the locks endpoint reports the table.
type Lease struct {
	Name     string
	Holder   string
	Task     string
	Acquired time.Time
	Expires  time.Time
}

// Held reports whether the lease is still in force at now.
func (l Lease) Held(now time.Time) bool { return now.Before(l.Expires) }

// WorkState is where an item of the persistent work queue stands (decision
// 0033).
type WorkState string

// Work item states.
const (
	// WorkPending waits for a worker, from NotBefore on.
	WorkPending WorkState = "pending"
	// WorkLeased is being run by LeasedBy until LeaseUntil.
	WorkLeased WorkState = "leased"
	// WorkParked failed too often and waits for a fresh enqueue.
	WorkParked WorkState = "parked"
)

// WorkItem is one unit of asynchronous derived work (decision 0033). There
// is one item per Kind and Key, so work asked for again while an item waits
// coalesces into the run it is waiting for.
type WorkItem struct {
	Kind string
	Key  string
	// Payload is a small JSON document from the latest enqueue.
	Payload []byte
	State   WorkState
	// Attempts counts the runs started since the item was last enqueued
	// afresh; it is what parks a key that keeps failing.
	Attempts int
	// Generation increases with every lease, so a worker whose lease ran
	// out cannot complete or fail the run another worker took over.
	Generation int64
	// NotBefore is when a pending item becomes due.
	NotBefore  time.Time
	LeasedBy   string
	LeaseUntil time.Time
	// DirtySince is when work was asked for again during the current run,
	// which then runs once more; zero when it was not.
	DirtySince time.Time
	LastError  string
	// Enqueued is when the work the item now owes was first asked for.
	Enqueued time.Time
	Updated  time.Time
}

// WorkStats summarises the queue for one kind, for metrics.
type WorkStats struct {
	Kind                    string
	Pending, Leased, Parked int64
	// OldestPending is the earliest Enqueued of a pending item; zero when
	// none is pending.
	OldestPending time.Time
}

// Setting keys of the singleton settings table.
const (
	SettingUserLockPolicy           = "security.user_lock_policy"
	SettingPasswordExpirationPolicy = "security.password_expiration_policy"
	SettingPasswordPolicy           = "security.password_policy"
	SettingBasicAuthEnabled         = "security.basic_auth_enabled"
	SettingDefaultTokenExpiry       = "token.default_expiry"
	SettingAnonymousAccess          = "security.anonymous_access"
	SettingTrashcan                 = "trashcan"
	// SettingFolderDownload holds FolderDownloadSettings
	// (folderDownloadConfig in the descriptor).
	SettingFolderDownload  = "folder_download"
	SettingBundleRetention = "distribution.bundle_retention"
	// SettingSeeded marks that the built-in principals were created, so a
	// later start never recreates one an administrator removed.
	SettingSeeded = "bootstrap.seeded"
	// SettingBootstrapDescriptor marks that bootstrap.descriptor was
	// applied, so it is applied once.
	SettingBootstrapDescriptor = "bootstrap.descriptor_applied"
	// SettingAccessLockSettings holds AccessLockSettings, the Access user
	// lock policy's fields that the lock and expiration policies have no
	// place for.
	SettingAccessLockSettings = "security.access_lock_settings"
)

// UserLockPolicy locks an account after repeated failed logins.
type UserLockPolicy struct {
	Enabled       bool `json:"enabled"`
	LoginAttempts int  `json:"login_attempts"`
}

// PasswordExpirationPolicy expires internal passwords after MaxAgeDays.
type PasswordExpirationPolicy struct {
	Enabled       bool `json:"enabled"`
	MaxAgeDays    int  `json:"max_age_days"`
	NotifyByEmail bool `json:"notify_by_email"`
}

// PasswordPolicy constrains new internal passwords.
//
// DefaultPasswordPolicy is in force while none is stored, and a replacing
// PUT resets every field it leaves out to it.
type PasswordPolicy struct {
	Uppercase   int  `json:"uppercase"`
	Lowercase   int  `json:"lowercase"`
	Digit       int  `json:"digit"`
	SpecialChar int  `json:"special_char"`
	Length      int  `json:"length"`
	NotMatchOld bool `json:"not_match_old"`
}

// DefaultPasswordPolicy is Access's documented default: no character
// classes required, at least 4 characters, and not the old password
// (docs.jfrog.com, supported Access configurations, security.password-policy).
// The documented rule that a password cannot match the username has no
// field and always applies.
func DefaultPasswordPolicy() PasswordPolicy { return PasswordPolicy{Length: 4, NotMatchOld: true} }

// AccessLockSettings are the Access user lock policy's fields beyond the
// lock limit (UserLockPolicy) and the password expiry
// (PasswordExpirationPolicy). They are stored and returned but not yet
// enforced, as the Access OpenAPI source says field by field.
type AccessLockSettings struct {
	SecondsToUnlock                int   `json:"seconds_to_unlock"`
	AdminPasswordExpirable         bool  `json:"admin_password_expirable"`
	MaxLoginDelayIncorrectAttempts int   `json:"max_login_delay_incorrect_attempts"`
	MaxLoginDelayMillis            int64 `json:"max_login_delay_millis"`
	BaseLoginDelayMillis           int64 `json:"base_login_delay_millis"`
	ClientLoginSuspensionEnabled   bool  `json:"client_login_suspension_enabled"`
}

// DefaultAccessLockSettings is in force while none is stored: Access's
// server defaults. docs.jfrog.com, supported Access configurations
// (security.user-lock-policy), documents the two delays. The
// getAccessUserLockPolicy reference page, the source of the attempts before
// suspension and the suspension switch, is gone; the endpoint is now
// documented only in the migration notes of the deprecated JFrog APIs page.
func DefaultAccessLockSettings() AccessLockSettings {
	return AccessLockSettings{
		MaxLoginDelayIncorrectAttempts: 2,
		MaxLoginDelayMillis:            60000,
		BaseLoginDelayMillis:           1000,
		ClientLoginSuspensionEnabled:   true,
	}
}

// TrashcanSettings control the trash can.
type TrashcanSettings struct {
	Enabled       bool `json:"enabled"`
	RetentionDays int  `json:"retention_days"`
}

// FolderDownloadSettings gate downloading a folder as one archive. As in
// Artifactory, it is off until enabled, and anonymous callers need their own
// switch.
type FolderDownloadSettings struct {
	Enabled             bool `json:"enabled"`
	EnabledForAnonymous bool `json:"enabled_for_anonymous"`
	MaxDownloadSizeMB   int  `json:"max_download_size_mb"`
	MaxFiles            int  `json:"max_files"`
}
