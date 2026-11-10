package artifacts

import (
	"time"

	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem"
)

// Settings are the artifacts module's options from the installation's
// configuration (decision 0009): one field per value that app hands the
// module's code, named as that code names it, and the format settings. Only
// app builds them, through app/config's Artifacts accessor. The zero value
// is not the configuration's default: app/config's Default holds the
// defaults, and the accessor copies whatever was loaded.
type Settings struct {
	// RedirectMinSize is download.redirect.min_size in bytes: the smallest
	// file that downloadRedirect and cdnRedirect send away; smaller files
	// stream.
	RedirectMinSize int64
	// RedirectTTL is download.redirect.ttl: the least time a signed URL
	// stays valid once handed out.
	RedirectTTL time.Duration
	// FolderDownloadMaxConcurrent is download.folder.max_concurrent_requests:
	// how many folder downloads may run at once on this node.
	FolderDownloadMaxConcurrent int
	// AQLMaxResults is search.aql.max_results: the cap on the rows of every
	// AQL query; zero means no cap.
	AQLMaxResults int
	// AQLScanFactor is search.aql.non_admin_scan_factor: what a non-admin
	// query's limit is multiplied by.
	AQLScanFactor int
	// AQLTimeout is aql_timeout.query: how long an AQL query may read the
	// database; zero means no timeout.
	AQLTimeout time.Duration
	// AQLStreamTimeout is aql_timeout.streamed: the same for a request with
	// a stream: true header.
	AQLStreamTimeout time.Duration
	// UserQueryLimit is search.user_query_limit: the cap on a GET search's
	// results for anonymous callers; zero means no cap.
	UserQueryLimit int
	// LimitInternalUsers is the negation of
	// search.limit_anonymous_users_only: true applies UserQueryLimit to
	// signed-in non-admins too.
	LimitInternalUsers bool
	// AllowDuplicateBuildEntries is the negation of
	// build.block_duplicate_entries: true accepts a build-info publish whose
	// module lists one file twice.
	AllowDuplicateBuildEntries bool
	// EnforceIdenticalChecksumDeploy is
	// deploy.enforce_permission_check_on_identical_checksum_deploy: a
	// checksum deploy over a file with the same content needs the delete
	// permission.
	EnforceIdenticalChecksumDeploy bool
	// NPMLoginTokenExpiry is npm.login_token_expiry: how long a token from
	// npm login lasts.
	NPMLoginTokenExpiry time.Duration
	// NPMMaxPublishSize is npm.max_publish_size in bytes: the largest npm
	// publish request accepted.
	NPMMaxPublishSize int64
	// CranArchiveMoverEnabled is cran.archive_mover_enabled: it turns on
	// the legacy CRAN archive move operation.
	CranArchiveMoverEnabled bool
	// HuggingFaceXetEnabled is huggingface.xet_enabled: it turns on the
	// native Xet transfer protocol.
	HuggingFaceXetEnabled bool
	// MavenSnapshotCleanupInterval is maven.snapshot_cleanup_interval: how
	// often each node runs the maxUniqueSnapshots cleanups that Maven
	// deploys have queued.
	MavenSnapshotCleanupInterval time.Duration
	// NuGetSnapshotCleanupInterval is nuget.snapshot_cleanup_interval: the
	// same for NuGet's.
	NuGetSnapshotCleanupInterval time.Duration
	// AllowedPrivateOrigins is remote.allowed_private_origins: the exact
	// private origins an operator approves as remote sources. Repository
	// URL patterns still authorize each archive URL.
	AllowedPrivateOrigins []string
	// GCGrace is gc.grace_period: how long unreferenced content is kept
	// before garbage collection deletes it; zero means the default.
	GCGrace time.Duration
	// GCInterval is gc.interval: the time between scheduled garbage
	// collection passes; zero means the default.
	GCInterval time.Duration
	// Formats are the formats' settings: conda.shards_enabled,
	// helm.relative_urls, npm.tag_latest_by_publish, npm.enforce_path and
	// the four pypi keys. Formats.Helm.RelativeURLs is never nil, because
	// nil means the default, relative URLs, and would ignore
	// relative_urls: false. Formats.TerraformBackend.Keys is not
	// configuration: it is nil here, and app sets the keyring.
	Formats ecosystem.Settings
}
