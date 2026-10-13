package types

import (
	"errors"
	"fmt"
	"time"
)

// ErrInvalidRestore reports a restore request that cannot be executed.
var ErrInvalidRestore = errors.New("invalid restore request")

// MaxRestoreItems caps one restore job, as JFrog does.
const MaxRestoreItems = 30000

// ArchiveFamily is which of JFrog's two archive policy APIs a policy belongs
// to (decision 0024): cold-storage retention v1 or smart archiving v2.
type ArchiveFamily string

// Archive families.
const (
	ArchiveRetentionV1 ArchiveFamily = "retention_v1"
	ArchiveSmartV2     ArchiveFamily = "smart_v2"
)

// ArchivePolicy moves selected items to the cold blob store. Criteria is the
// family's selection document, kept in that family's wire shape because the
// two families select differently.
type ArchivePolicy struct {
	Key              string
	Family           ArchiveFamily
	Description      string
	Enabled          bool
	CronExp          string
	ProjectKey       string
	DurationMinutes  int
	ExpirationMonths int
	SkipTrashcan     bool
	Criteria         []byte
	Created          time.Time
	Updated          time.Time
}

// ExecutionKind is what an archive execution does.
type ExecutionKind string

// Execution kinds.
const (
	ExecutionArchive ExecutionKind = "archive"
	ExecutionRestore ExecutionKind = "restore"
	ExecutionPurge   ExecutionKind = "purge"
)

// ExecutionState is the state of an archive, restore or purge execution.
type ExecutionState string

// Execution states as the cold-storage API reports them.
const (
	ExecutionPending              ExecutionState = "PENDING"
	ExecutionInProgress           ExecutionState = "IN_PROGRESS"
	ExecutionInProgressWithErrors ExecutionState = "IN_PROGRESS_WITH_ERRORS"
	ExecutionComplete             ExecutionState = "COMPLETE"
	ExecutionCompleteWithErrors   ExecutionState = "COMPLETE_WITH_ERRORS"
	ExecutionFailed               ExecutionState = "FAILED"
)

// ArchiveExecution is one run of an archive policy, a restore job or a purge.
type ArchiveExecution struct {
	ID           int64
	PolicyKey    string
	Kind         ExecutionKind
	State        ExecutionState
	Started      time.Time
	Finished     time.Time
	Total        int64
	Completed    int64
	Failed       int64
	TotalBytes   int64
	SuccessBytes int64
	FailureBytes int64
	TriggeredBy  string
	Destination  []byte
}

// Progress is the completed share in percent, as progressPercentage reports.
func (e ArchiveExecution) Progress() int {
	if e.Total <= 0 {
		return 0
	}
	return int((e.Completed + e.Failed) * 100 / e.Total)
}

// ArchivedItem is the record an archived item leaves behind: its full
// metadata, so a restore recreates it and AQL over archives can search it.
type ArchivedItem struct {
	ID             int64
	Namespace      string
	Item           Item
	Properties     Properties
	Stats          ItemStats
	PackageType    string
	PackageName    string
	PackageVersion string
	PolicyKey      string
	ExecutionID    int64
	ArchivedAt     time.Time
	ExpiresAt      time.Time
	ColdBlobSHA256 string
}

// Expired reports whether the record is due for purging.
func (a ArchivedItem) Expired(now time.Time) bool {
	return !a.ExpiresAt.IsZero() && !now.Before(a.ExpiresAt)
}

// RestoreType is what a restore request names.
type RestoreType string

// Restore types.
const (
	RestorePackage  RestoreType = "PACKAGE_RESTORE"
	RestoreArtifact RestoreType = "ARTIFACT_RESTORE"
)

// RestoreRequest asks for archived content back. Items are archived paths
// or package references; when ToOriginal is false or the original path is
// taken, the fallback, central or new destination applies.
type RestoreRequest struct {
	Type          RestoreType
	Items         []string
	ToOriginal    bool
	FallbackPath  string
	CentralPath   string
	NewPath       string
	AddNamespace  bool
	ManifestPaths []string
}

// Validate checks the request is executable: a known type, at least one and
// at most MaxRestoreItems items, and somewhere to put them.
func (r RestoreRequest) Validate() error {
	switch {
	case r.Type != RestorePackage && r.Type != RestoreArtifact:
		return fmt.Errorf("%w: unknown type %q", ErrInvalidRestore, r.Type)
	case len(r.Items) == 0 && len(r.ManifestPaths) == 0:
		return fmt.Errorf("%w: nothing to restore", ErrInvalidRestore)
	case len(r.Items) > MaxRestoreItems:
		return fmt.Errorf(
			"%w: %d items exceeds %d",
			ErrInvalidRestore,
			len(r.Items),
			MaxRestoreItems,
		)
	case !r.ToOriginal && r.FallbackPath == "" && r.CentralPath == "" && r.NewPath == "":
		return fmt.Errorf("%w: no destination", ErrInvalidRestore)
	default:
		return nil
	}
}
