package app

import (
	"net/http"
	"strings"
)

// restAPI is the prefix of the REST resources 7.x's Jersey servlet serves.
const restAPI = "/artifactory/api/"

// mapperRoots are the REST roots whose 7.161.26 resources are open source
// and refuse an anonymous caller by raising a forbidden: @RolesAllowed's
// ForbiddenException, an AuthorizationRestException or a
// ForbiddenWebAppException without an entity. Jersey hands each to the
// application's global exception mappers, whose interceptForbiddenStatus
// answers an anonymous caller 401 with a challenge, or a bare 404 while
// hideUnauthorizedResources is on (ArtifactoryRestExceptionMapperBase
// 84-93), which RestResponseFilter answers 404 "Not Found". A root matches itself and the paths below it.
//
// Left out on purpose: the package formats' native endpoints, whose closed
// handlers answer for themselves; the versions search and the archive
// download, which build their own 401 (VersionsResources 98-108,
// ArchiveResource 193-196); and roots whose resources are closed code,
// such as vcs, gpg, signed, retention and the cleanup and archive policies,
// where nothing shows whether they raise a forbidden [I].
var mapperRoots = []string{
	"build",                        // BuildPublicResource (build-handler)
	"builds",                       // BuildUiResource (build-handler)
	"checksum/sha256",              // ChecksumResource
	"config",                       // ConfigsServiceResource
	"copy",                         // CopyResource
	"deleteDirectoryTrashProperty", // RemoveDirsTrashcanTimeResource
	"download",                     // DownloadResource
	"export",                       // ExportResource
	"federation",                   // FederatedRepoResource and its siblings
	"import",                       // ImportResource
	"maven",                        // MavenResource
	"metadata",                     // ArtifactMetadataResource
	"metadata_server",              // MetadataServerReindexResource
	"move",                         // MoveResource
	"replication",                  // ReplicationResource
	"replications",                 // ReplicationsResource
	"repositories",                 // RepositoriesResource
	"search",                       // SearchResource, AqlResource
	"security",                     // SecurityResource, ApiKeyResource, TokenResource and the others
	"storage",                      // ArtifactResource
	"storageinfo",                  // StorageSummaryInfoResource
	"system",                       // SystemResource and the system/* resources
	"tasks",                        // TasksResource
	"trash",                        // TrashcanResource
	"v1/metrics",                   // MetricsAPIResource
	"v1/uploads",                   // MultipartUploadResource
	"v2/federation/status",         // FederatedStatusResourceV2
	"v2/repositories",              // RepositoriesResourceV2, RepositoriesBatchResource
	"v2/security",                  // SecurityResourceV2
	"zap",                          // ZapArtifactsResource
}

// mapperExceptions are the operations below mapperRoots whose 7.161.26
// resource answers an anonymous caller with a 401 it builds itself, which
// the mappers pass through with their challenge and never hide. A key
// without a method matches every method.
var mapperExceptions = []struct{ method, root string }{
	// TokenResource.createToken throws WebApplicationException(401) (233).
	{http.MethodPost, "security/token"},
	// MetricsResource returns Response.status(401) (83-208).
	{"", "system/metrics"},
	// ReplicationsResource.denyAnonymousReplicationCaller (265, 319, 338).
	{"", "replications/channels/establishChannel"},
	{"", "replications/channels/removeChannel"},
	{"", "replications/propagateEvents"},
}

// mapperRoutes reports whether 7.x's REST exception mappers answer r's
// anonymous refusal (httpkit.MapsForbidden).
func mapperRoutes(r *http.Request) bool {
	rest, ok := strings.CutPrefix(r.URL.Path, restAPI)
	if !ok {
		return false
	}
	for _, e := range mapperExceptions {
		if (e.method == "" || e.method == r.Method) && underRoot(rest, e.root) {
			return false
		}
	}
	for _, root := range mapperRoots {
		if underRoot(rest, root) {
			return true
		}
	}
	return false
}

// underRoot reports whether rest is root or a path below it, so that
// "build" does not take "builds" or "storage" "storageinfo".
func underRoot(rest, root string) bool {
	return rest == root || strings.HasPrefix(rest, root+"/")
}
