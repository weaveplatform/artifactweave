package api

import (
	"context"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// MaxDownloadVersions bounds one federation evidence query independently of
// the number of files linked to each indexed version.
const MaxDownloadVersions = 64

// CleanupRepository binds secret-free ownership and membership to one durable
// repository lifetime and configuration generation. Its member URLs stay with
// federation's resolver, never in cleanup reports or explanations.
type CleanupRepository struct {
	Repository              IndexRepository
	Incarnation, Generation string
	Members                 []repo.Member
}

// DownloadVersion identifies an existing logical version on a member. IDs are
// local to that member and are returned separately as incarnation evidence.
type DownloadVersion struct {
	Package string `json:"package"`
	Version string `json:"version"`
}

// VersionDownload carries recorded use and its independent mutation marker.
// Removing a file changes the ordinary content marker, but cannot manufacture
// a download observation. An absent row is unknown, not proof of no downloads.
type VersionDownload struct {
	DownloadVersion
	ID                      int64
	Revision                string
	Created, LastDownloaded time.Time
	AdmittedAt              time.Time
	AdmissionRevision       string
}

// CleanupMetadata performs bounded index-metadata reads under the current
// repository identity. Each backend read uses or joins a short transaction;
// an administration-row guard serializes context changes but does not authorize
// the caller. The owning services authenticate the member or original caller,
// bind scope before collection, and refresh caller/context checks before
// disclosing an outcome or publishing deletion. No metadata transaction spans
// network I/O, and these descriptions confer no deletion permission.
type CleanupMetadata interface {
	Repository(context.Context, string) (CleanupRepository, error)
	Downloads(context.Context, CleanupRepository, []DownloadVersion) ([]VersionDownload, error)
}

// FederatedRemovalPermit owns a durable cross-member exclusion. Revalidate
// joins guarded removal's local transaction and validates its exact snapshot.
// Exclusion is still verified against the durable participant reservation by
// every actual item mutation. Fresh checks publication state and Commit records
// the irreversible outcome and final outbox watermark in the removal transaction.
type FederatedRemovalPermit interface {
	Revalidate(context.Context, string, int64) error
	Fresh(context.Context) error
	Exclusion() CleanupExclusion
	Commit(context.Context) error
}
