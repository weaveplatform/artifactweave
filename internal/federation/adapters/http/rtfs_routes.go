package federationhttp

import (
	"net/http"
	"slices"
	"strings"
)

// RTFSRefusal is one migration-only or internal endpoint that discloses no
// state and always answers the same status, regardless of user credentials.
type RTFSRefusal struct {
	Method string
	Path   string
	Status int
}

var rtfsRefusals = []RTFSRefusal{
	{"GET", "/artifactory/api/federation/status", 404},
	{"GET", "/artifactory/api/federation/validate/{repoKey}", 404},
	{"GET", "/artifactory/api/federation/repos", 404},
	{"GET", "/artifactory/api/federation/baseUrlAndRepos", 404},
	{"POST", "/artifactory/api/federation/properties/{repoKey}", 404},
	{"GET", "/artifactory/api/federation/property/{repoKey}/{fileName}", 404},
	{"GET", "/artifactory/api/federation/extendedInfo/{repoKey}", 404},
	{"GET", "/artifactory/api/federation/ready/{repoKey}", 404},
	{"GET", "/artifactory/api/federation/repoExists/{repoKey}", 404},
	{"POST", "/artifactory/api/federation/metadata", 404},
	{"PUT", "/artifactory/api/federation/metadata", 404},
	{"POST", "/artifactory/api/federation/metadata/invalidate", 404},
	{"PUT", "/artifactory/api/federation/negotiation/disable", 404},
	{"POST", "/artifactory/api/federation/repos/enable", 404},
	{"POST", "/artifactory/api/federation/dryfullsync/repo/{fileName}", 404},
	{"POST", "/artifactory/api/federation/fullsync/repo/{fileName}", 404},
	{"GET", "/artifactory/api/federation/fullsync/repo/{fileName}", 404},
	{"DELETE", "/artifactory/api/federation/fullsync/repo/{fileName}", 404},
	{"POST", "/artifactory/api/federation/fullsync/systemRepo", 404},
	{"GET", "/artifactory/api/federation/federatedRepoConfigInfo", 404},
	{"GET", "/artifactory/api/federation/federatedConfigInfo/{repoKey}", 404},
	{"POST", "/artifactory/api/federation/disableRemoteMember", 404},
	{"POST", "/artifactory/api/federation/member/enable", 404},
	{"GET", "/artifactory/api/federation/lastIdInSystem", 404},
	{"POST", "/artifactory/api/federation/task/submit", 404},
	{"GET", "/artifactory/api/federation/binary/provider/v2/check", 404},
	{"POST", "/artifactory/api/federation/queue/remaining", 404},
	{"PUT", "/artifactory/api/federation/queue/stop", 404},
	{"PUT", "/artifactory/api/federation/queue/deregister", 404},
	{"POST", "/artifactory/api/federation/queue/reset", 404},
	{"GET", "/artifactory/api/federation/queue/state", 404},
	{"PUT", "/artifactory/api/federation/queue/state/{state}", 404},
	{"GET", "/artifactory/api/federation/tenant/move/status", 404},
	{"PUT", "/artifactory/api/federation/snapshotFederatedRepos", 404},
	{"GET", "/artifactory/api/federation/migration/status/verbose", 404},
	{"PUT", "/artifactory/api/federation/binaries/rtfs", 404},
	{"GET", "/artifactory/api/federation/binaries/rtfs", 404},
	{"POST", "/artifactory/api/federation/grid/topology/events", 404},
	{"PUT", "/artifactory/api/federation/rtfs", 400},
	{"PUT", "/artifactory/api/federation/migration", 400},
	{"PUT", "/artifactory/api/federation/migration/status/verbose", 400},
}

// RTFSRefusals returns the researched refusal routes for auth/routing inventories.
func RTFSRefusals() []RTFSRefusal { return slices.Clone(rtfsRefusals) }

// SkipsRTFSAuthentication keeps the migration-tool refusal and internal 404
// ahead of user credential parsing. No privileged operation matches this list.
func SkipsRTFSAuthentication(r *http.Request) bool {
	path := strings.Split(r.URL.Path, "/")
	for _, route := range rtfsRefusals {
		if r.Method != route.Method {
			continue
		}
		pattern := strings.Split(route.Path, "/")
		if len(pattern) != len(path) {
			continue
		}
		matched := true
		for i, part := range pattern {
			if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
				matched = matched && path[i] != ""
			} else {
				matched = matched && part == path[i]
			}
		}
		if matched {
			return true
		}
	}
	return false
}
