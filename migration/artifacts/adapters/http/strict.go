package artifactshttp

import (
	"cmp"
	"context"
	"time"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/gc"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/multipart"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/smart"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/usage"
)

// ArtifactoryAPI is the artifacts module's partial strict server: the
// repository, build, search, trash, storage summary and reindex operations
// of the artifactory spec, and the handwritten item, transfer, storage and
// search routes that its registrars mount. The router's composite strict
// server adds the other partials and the generated stubs.
type ArtifactoryAPI struct {
	// Kit is the item kit every partial shares; New builds it, and a
	// literal without one panics on its first kit call.
	*itemkit.Kit
	cfg Deps
	// folderSlots holds a token for each folder archive download running;
	// nil, in services built only to reach the store, limits nothing.
	folderSlots chan struct{}
}

// Deps is what the artifacts handlers need: the item kit's dependencies,
// over which New builds the kit, and the handlers' own.
type Deps struct {
	// Deps are the item kit's: the stores, the access module's API, the
	// remote sources, the formats, and the overlays and private predicate
	// the router passes.
	itemkit.Deps
	// RemoteHTTP drops the connection pool of a repository that is
	// deleted. Assign it only a pool that exists, or leave it nil: a nil
	// *upstream.Pool stored in it is a non-nil interface, so a delete would
	// call Remove on the nil pool, which panics on the pool's lock.
	RemoteHTTP interface{ Remove(key string) }
	// RemoteStatus serves artifactweave-only, administrator-only
	// diagnostics; nil answers them 501.
	RemoteStatus func(context.Context, string, string) (smart.SmartStatus, error)
	// GC is the garbage collector the runtime schedules; POST
	// /api/system/storage/gc runs the same one. Nil builds one per request,
	// for tests.
	GC *gc.Collector
	// Multipart serves the JFrog CLI's multipart upload protocol; nil
	// reports it unsupported, so the CLI uploads in one request.
	Multipart *multipart.Service
	// Mirrors administers the blob mirrors; nil when no tier is a mirror.
	Mirrors Mirrors
	// BlobLocation describes the hot blob store in the storage summary.
	BlobLocation usage.Location
	// AQL bounds AQL searches; the zero value has no cap. Its MaxResults
	// caps the GET searches too.
	AQL AQLLimits
	// Search limits what the GET searches return; the zero value limits
	// nothing.
	Search SearchLimits
	// AllowDuplicateBuildEntries accepts a build-info publish whose module
	// lists one file twice (build.block_duplicate_entries false); the zero
	// value refuses it, as Artifactory 7.x does by default.
	AllowDuplicateBuildEntries bool
	// FolderDownloadMaxConcurrent is how many folder archive downloads run
	// at once (download.folder.max_concurrent_requests); zero means
	// Artifactory's 10.
	FolderDownloadMaxConcurrent int
	// HuggingFaceXetEnabled turns on Xet in the Hugging Face protocol
	// (huggingface.xet_enabled); the zero value answers its token routes
	// 501 and keeps Git LFS.
	HuggingFaceXetEnabled bool
	// NPMLoginTokenExpiry is how long a token from npm login lasts
	// (npm.login_token_expiry); zero means decision 0034's default.
	NPMLoginTokenExpiry time.Duration
	// NPMMaxPublishSize caps an npm publish request (npm.max_publish_size);
	// zero means decision 0034's default.
	NPMMaxPublishSize int64
}

// New returns the artifacts partial over d with its own item kit, which the
// router hands to the ecosystem partials as well, so that the routes of the
// whole spec share one set of overlays and one redirect throttle. Every
// artifacts partial is built here: a literal without a kit compiles and then
// panics on its first kit call.
func New(d Deps) ArtifactoryAPI {
	return ArtifactoryAPI{
		Kit: itemkit.New(d.Deps),
		cfg: d,
		folderSlots: make(
			chan struct{},
			cmp.Or(d.FolderDownloadMaxConcurrent, defaultFolderSlots),
		),
	}
}
