package repo

import (
	"slices"
	"testing"
)

// Every field of Config is either one the v2 read keeps for every
// repository or one v2Fields rules on, so a new field cannot reach the v2
// read without a decision. The JSON-only Shown has no name.
func TestUnitEveryFieldHasAV2Rule(t *testing.T) {
	everyRepository := []string{
		"key", "rclass", "packageType", "projectKey", "environments", "description", "notes",
		"includesPattern", "excludesPattern", "repoLayoutRef", "signedUrlTtl", "-",
		// A remote repository's own fields, which no other class holds.
		"url", "username", "password", "remoteRepoLayoutRef", "hardFail", "offline",
		"storeArtifactsLocally", "socketTimeoutMillis", "localAddress",
		"retrievalCachePeriodSecs", "missedRetrievalCachePeriodSecs",
		"metadataRetrievalTimeoutSecs", "assumedOfflinePeriodSecs",
		"unusedArtifactsCleanupPeriodHours", "shareConfiguration", "synchronizeProperties",
		"blockMismatchingMimeTypes", "mismatchingMimeTypesOverrideList", "allowAnyHostAuth",
		"enableCookieManagement", "bypassHeadRequests", "clientTlsCertificate",
		"listRemoteFolderItems", "queryParams", "propagateQueryParams",
		"retrieveSha256FromServer", "curated", "passThrough", "sendContext",
		"disableUrlNormalization", "customHttpHeaders", "contentSynchronisation",
		// A virtual repository's own fields.
		"repositories", "defaultDeploymentRepo", "artifactoryRequestsCanRetrieveRemoteArtifacts",
		"hideUnauthorizedResources",
	}
	for _, name := range configFields {
		_, ruled := v2Fields[name]
		if ruled == slices.Contains(everyRepository, name) {
			t.Errorf("%s: ruled %v, kept for every repository %v", name, ruled, !ruled)
		}
	}
	for name := range v2Fields {
		if !slices.Contains(configFields, name) {
			t.Errorf("%s is ruled on but is no field", name)
		}
	}
}
