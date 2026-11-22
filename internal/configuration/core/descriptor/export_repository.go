package descriptor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// These paths follow 7.161.26 artifactory.xsd and the descriptor JAXB names.
// API-only fields, including project bindings and @XmlTransient fields, have no
// XML projection. No default is synthesized: nil and explicit zero stay distinct.
var repositoryExportPaths = map[repo.Class][][2]string{
	repo.Local: {
		{"key", "key"},
		{"packageType", "type"},
		{"description", "description"},
		{"notes", "notes"},
		{"includesPattern", "includesPattern"},
		{"excludesPattern", "excludesPattern"},
		{"repoLayoutRef", "repoLayoutRef"},
		{"dockerApiVersion", "dockerApiVersion"},
		{"forceNugetAuthentication", "forceNugetAuthentication"},
		{"forceConanAuthentication", "forceConanAuthentication"},
		{"ddebSupported", "ddebSupported"},
		{"cargoAnonymousAccess", "cargoConfig/cargoAnonymousAccess"},
		{"cargoInternalIndex", "cargoConfig/cargoInternalIndex"},
		{"primaryKeyPairRef", "primaryKeyPairRef"},
		{"secondaryKeyPairRef", "secondaryKeyPairRef"},
		{"signedUrlTtl", "signedUrlTtl"},
		{"blackedOut", "blackedOut"},
		{"handleReleases", "handleReleases"},
		{"handleSnapshots", "handleSnapshots"},
		{"maxUniqueSnapshots", "maxUniqueSnapshots"},
		{"maxUniqueTags", "maxUniqueTags"},
		{"blockPushingSchema1", "blockPushingSchema1"},
		{"suppressPomConsistencyChecks", "suppressPomConsistencyChecks"},
		{"propertySets", "propertySets/propertySetRef"},
		{"archiveBrowsingEnabled", "archiveBrowsingEnabled"},
		{"xrayIndex", "xray/enabled"},
		{"xrayDataTtl", "xray/dataTtl"},
		{"downloadRedirect", "downloadRedirect/enabled"},
		{"priorityResolution", "priorityResolution"},
		{"cdnRedirect", "cdnRedirect/enabled"},
		{"snapshotVersionBehavior", "snapshotVersionBehavior"},
		{"checksumPolicyType", "localRepoChecksumPolicyType"},
		{"calculateYumMetadata", "calculateYumMetadata"},
		{"yumRootDepth", "yumRootDepth"},
		{"yumGroupFileNames", "yumGroupFileNames"},
		{"debianTrivialLayout", "debianTrivialLayout"},
		{"enableFileListsIndexing", "enableFileListsIndexing"},
		{"optionalIndexCompressionFormats", "optionalIndexCompressionFormats/debianFormat"},
		{"dockerTagRetention", "dockerTagRetention"},
		{"enableComposerV1Indexing", "enableComposerV1Indexing"},
		{"terraformType", "terraformType"},
	},
	repo.Remote: {
		{"key", "key"},
		{"packageType", "type"},
		{"description", "description"},
		{"notes", "notes"},
		{"includesPattern", "includesPattern"},
		{"excludesPattern", "excludesPattern"},
		{"repoLayoutRef", "repoLayoutRef"},
		{"dockerApiVersion", "dockerApiVersion"},
		{"forceNugetAuthentication", "forceNugetAuthentication"},
		{"forceConanAuthentication", "forceConanAuthentication"},
		{"ddebSupported", "ddebSupported"},
		{"cargoAnonymousAccess", "cargoConfig/cargoAnonymousAccess"},
		{"cargoInternalIndex", "cargoConfig/cargoInternalIndex"},
		{"primaryKeyPairRef", "primaryKeyPairRef"},
		{"secondaryKeyPairRef", "secondaryKeyPairRef"},
		{"signedUrlTtl", "signedUrlTtl"},
		{"blackedOut", "blackedOut"},
		{"handleReleases", "handleReleases"},
		{"handleSnapshots", "handleSnapshots"},
		{"maxUniqueSnapshots", "maxUniqueSnapshots"},
		{"maxUniqueTags", "maxUniqueTags"},
		{"blockPushingSchema1", "blockPushingSchema1"},
		{"suppressPomConsistencyChecks", "suppressPomConsistencyChecks"},
		{"propertySets", "propertySets/propertySetRef"},
		{"archiveBrowsingEnabled", "archiveBrowsingEnabled"},
		{"xrayIndex", "xray/enabled"},
		{"xrayDataTtl", "xray/dataTtl"},
		{"downloadRedirect", "downloadRedirect/enabled"},
		{"priorityResolution", "priorityResolution"},
		{"cdnRedirect", "cdnRedirect/enabled"},
		{"url", "url"},
		{"offline", "offline"},
		{"hardFail", "hardFail"},
		{"storeArtifactsLocally", "storeArtifactsLocally"},
		{"fetchJarsEagerly", "fetchJarsEagerly"},
		{"fetchSourcesEagerly", "fetchSourcesEagerly"},
		{"externalDependenciesEnabled", "externalDependencies/enabled"},
		{"externalDependenciesPatterns", "externalDependencies/patterns/pattern"},
		{"externalDependenciesRemoteRepo", "externalDependencies/remoteRepo"},
		{"retrievalCachePeriodSecs", "retrievalCachePeriodSecs"},
		{"metadataRetrievalTimeoutSecs", "metadataRetrievalTimeoutSecs"},
		{"assumedOfflinePeriodSecs", "assumedOfflinePeriodSecs"},
		{"missedRetrievalCachePeriodSecs", "missedRetrievalCachePeriodSecs"},
		{"remoteRepoChecksumPolicyType", "remoteRepoChecksumPolicyType"},
		{"unusedArtifactsCleanupPeriodHours", "unusedArtifactsCleanupPeriodHours"},
		{"shareConfiguration", "shareConfiguration"},
		{"synchronizeProperties", "synchronizeProperties"},
		{"listRemoteFolderItems", "listRemoteFolderItems"},
		{"remoteRepoLayoutRef", "remoteRepoLayoutRef"},
		{"rejectInvalidJars", "rejectInvalidJars"},
		{"feedContextPath", "nuget/feedContextPath"},
		{"downloadContextPath", "nuget/downloadContextPath"},
		{"v3FeedUrl", "nuget/v3FeedUrl"},
		{"symbolServerUrl", "nuget/symbolServerUrl"},
		{"pyPIRegistryUrl", "pypi/pyPIRegistryUrl"},
		{"pyPIRepositorySuffix", "pypi/repositorySuffix"},
		{"bowerRegistryUrl", "bower/bowerRegistryUrl"},
		{"podsSpecsRepoUrl", "cocoaPods/cocoaPodsSpecsRepoUrl"},
		{"chartsBaseUrl", "helm/chartsBaseUrl"},
		{"gitRegistryUrl", "git/registryUrl"},
		{"vcsType", "vcs/type"},
		{"vcsGitProvider", "vcs/git/provider"},
		{"vcsGitDownloadUrl", "vcs/git/downloadUrl"},
		{"contentSynchronisation", "contentSynchronisation"},
		{"blockMismatchingMimeTypes", "blockMismatchingMimeTypes"},
		{"mismatchingMimeTypesOverrideList", "mismatchingMimeTypesOverrideList"},
		{"bypassHeadRequests", "bypassHeadRequests"},
		{"composerRegistryUrl", "composer/composerRegistryUrl"},
		{"terraformRegistryUrl", "terraform/terraformRegistryUrl"},
		{"terraformProvidersUrl", "terraform/terraformProvidersUrl"},
		{"username", "username"},
		{"allowAnyHostAuth", "allowAnyHostAuth"},
		{"socketTimeoutMillis", "socketTimeoutMillis"},
		{"enableCookieManagement", "enableCookieManagement"},
		{"enableTokenAuthentication", "enableTokenAuthentication"},
		{"localAddress", "localAddress"},
		{"proxy", "proxyRef"},
		{"disableProxy", "disableProxy"},
		{"queryParams", "queryParams"},
		{"propagateQueryParams", "propagateQueryParams"},
		{"clientTlsCertificate", "clientTlsCertificate"},
		{"disableUrlNormalization", "disableUrlNormalization"},
		{"retrieveSha256FromServer", "retrieveSha256FromServer"},
		{"customHttpHeaders", "customHttpHeaders"},
		{"hexPublicKey", "hex/hexPublicKey"},
		{"mlRepoLayout", "huggingFace/mlRepoLayout"},
	},
	repo.Virtual: {
		{"key", "key"},
		{"packageType", "type"},
		{"description", "description"},
		{"notes", "notes"},
		{"includesPattern", "includesPattern"},
		{"excludesPattern", "excludesPattern"},
		{"repoLayoutRef", "repoLayoutRef"},
		{"dockerApiVersion", "dockerApiVersion"},
		{"forceNugetAuthentication", "forceNugetAuthentication"},
		{"forceConanAuthentication", "forceConanAuthentication"},
		{"ddebSupported", "ddebSupported"},
		{"cargoAnonymousAccess", "cargoConfig/cargoAnonymousAccess"},
		{"cargoInternalIndex", "cargoConfig/cargoInternalIndex"},
		{"primaryKeyPairRef", "primaryKeyPairRef"},
		{"secondaryKeyPairRef", "secondaryKeyPairRef"},
		{"signedUrlTtl", "signedUrlTtl"},
		{"hideUnauthorizedResources", "hideUnauthorizedResources"},
		{"artifactoryRequestsCanRetrieveRemoteArtifacts", "artifactoryRequestsCanRetrieveRemoteArtifacts"},
		{"resolveDockerTagsByTimestamp", "resolveDockerTagsByTimestamp"},
		{"repositories", "repositories/repositoryRef"},
		{"keyPair", "keyPair"},
		{"pomRepositoryReferencesCleanupPolicy", "pomRepositoryReferencesCleanupPolicy"},
		{"p2Urls", "p2/urls/url"},
		{"defaultDeploymentRepo", "defaultDeploymentRepo"},
		{"cachingLocalForeignLayersEnabled", "cachingLocalForeignLayersEnabled"},
		{"externalDependenciesEnabled", "externalDependencies/enabled"},
		{"externalDependenciesPatterns", "externalDependencies/patterns/pattern"},
		{"externalDependenciesRemoteRepo", "externalDependencies/remoteRepo"},
		{"virtualRetrievalCachePeriodSecs", "virtualCacheConfig/virtualRetrievalCachePeriodSecs"},
		{"forceMavenAuthentication", "forceMavenAuthentication"},
		{"debianDefaultArchitectures", "debianDefaultArchitectures"},
		{"optionalIndexCompressionFormats", "debianOptionalIndexCompressionFormats/debianFormat"},
		{"useNamespaces", "useNamespaces"},
	},
	repo.Federated: {
		{"key", "key"},
		{"packageType", "type"},
		{"description", "description"},
		{"notes", "notes"},
		{"includesPattern", "includesPattern"},
		{"excludesPattern", "excludesPattern"},
		{"repoLayoutRef", "repoLayoutRef"},
		{"dockerApiVersion", "dockerApiVersion"},
		{"forceNugetAuthentication", "forceNugetAuthentication"},
		{"forceConanAuthentication", "forceConanAuthentication"},
		{"ddebSupported", "ddebSupported"},
		{"cargoAnonymousAccess", "cargoConfig/cargoAnonymousAccess"},
		{"cargoInternalIndex", "cargoConfig/cargoInternalIndex"},
		{"primaryKeyPairRef", "primaryKeyPairRef"},
		{"secondaryKeyPairRef", "secondaryKeyPairRef"},
		{"signedUrlTtl", "signedUrlTtl"},
		{"blackedOut", "blackedOut"},
		{"handleReleases", "handleReleases"},
		{"handleSnapshots", "handleSnapshots"},
		{"maxUniqueSnapshots", "maxUniqueSnapshots"},
		{"maxUniqueTags", "maxUniqueTags"},
		{"blockPushingSchema1", "blockPushingSchema1"},
		{"suppressPomConsistencyChecks", "suppressPomConsistencyChecks"},
		{"propertySets", "propertySets/propertySetRef"},
		{"archiveBrowsingEnabled", "archiveBrowsingEnabled"},
		{"xrayIndex", "xray/enabled"},
		{"xrayDataTtl", "xray/dataTtl"},
		{"downloadRedirect", "downloadRedirect/enabled"},
		{"priorityResolution", "priorityResolution"},
		{"cdnRedirect", "cdnRedirect/enabled"},
		{"snapshotVersionBehavior", "snapshotVersionBehavior"},
		{"checksumPolicyType", "localRepoChecksumPolicyType"},
		{"calculateYumMetadata", "calculateYumMetadata"},
		{"yumRootDepth", "yumRootDepth"},
		{"yumGroupFileNames", "yumGroupFileNames"},
		{"debianTrivialLayout", "debianTrivialLayout"},
		{"enableFileListsIndexing", "enableFileListsIndexing"},
		{"optionalIndexCompressionFormats", "optionalIndexCompressionFormats/debianFormat"},
		{"dockerTagRetention", "dockerTagRetention"},
		{"enableComposerV1Indexing", "enableComposerV1Indexing"},
		{"terraformType", "terraformType"},
		{"members", "federatedMembers"},
		{"proxy", "proxyRef"},
		{"disableProxy", "disableProxy"},
	},
}

func exportRepository(c repo.Config) (exportFields, error) {
	// Mask before any serialization. Passwords are omitted; sensitive headers keep
	// their identity/sensitivity but carry no value, as repository reads do.
	masked := repo.Masked(c)
	// Masked also supplies two effective Debian REST defaults. XML keeps the
	// stored optional values and lets its schema define omitted defaults.
	masked.DdebSupported, masked.DebianTrivialLayout = c.DdebSupported, c.DebianTrivialLayout
	c = masked
	encoded, err := json.Marshal(c)
	if err != nil {
		return exportFields{}, fmt.Errorf("encode export repository %s: %w", c.Key, err)
	}
	var fields map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return exportFields{}, fmt.Errorf("decode export repository %s: %w", c.Key, err)
	}
	fields["packageType"] = repositoryXMLPackageType(c.PackageType)
	out := exportFields{}
	for _, mapping := range repositoryExportPaths[c.Class] {
		key, path := mapping[0], mapping[1]
		value, present := fields[key]
		if !present || value == nil {
			continue
		}
		if key == "vcsType" || key == "vcsGitProvider" {
			// JAXB's XmlEnumValue differs from the uppercase Jackson JsonProperty.
			value = strings.ToLower(fmt.Sprint(value))
		}
		switch key {
		case "customHttpHeaders":
			list, _ := value.([]any)
			out.Fields = append(out.Fields, exportRecords(path, "customHttpHeader", list, []string{"name", "value", "sensitive"}))
		case "members":
			list, _ := value.([]any)
			out.Fields = append(out.Fields, exportRecords(path, "federatedMember", list, []string{"url", "enabled"}))
		case "contentSynchronisation":
			values, _ := value.(map[string]any)
			for _, field := range []string{"enabled", "statistics/enabled", "properties/enabled", "source/originAbsenceDetection"} {
				if v, ok := nestedValue(values, strings.Split(field, "/")); ok {
					putExportPath(&out.Fields, strings.Split(path+"/"+field, "/"), v)
				}
			}
		default:
			putExportPath(&out.Fields, strings.Split(path, "/"), value)
		}
	}
	return out, nil
}

// RepoType's JAXB values mostly match the stored package type. These two
// differ; PackageTypeName is its Jackson JSON spelling and is not XML.
func repositoryXMLPackageType(kind repo.PackageType) string {
	switch kind {
	case repo.RPM:
		return "yum"
	case repo.HelmOCI:
		return "helmOci"
	default:
		return string(kind)
	}
}

func nestedValue(fields map[string]any, path []string) (any, bool) {
	value, present := fields[path[0]]
	if !present || len(path) == 1 {
		return value, present
	}
	children, _ := value.(map[string]any)
	return nestedValue(children, path[1:])
}

func exportRecords(name, element string, list []any, fields []string) exportField {
	out := exportField{Name: name}
	for _, item := range list {
		values, _ := item.(map[string]any)
		record := exportField{Name: element}
		for _, field := range fields {
			if value, present := values[field]; present {
				record.Children = append(record.Children, exportValue(field, value))
			}
		}
		out.Children = append(out.Children, record)
	}
	return out
}

func putExportPath(fields *[]exportField, path []string, value any) {
	if len(path) == 1 {
		if list, ok := value.([]any); ok {
			for _, item := range list {
				*fields = append(*fields, exportValue(path[0], item))
			}
		} else {
			*fields = append(*fields, exportValue(path[0], value))
		}
		return
	}
	for i := range *fields {
		if (*fields)[i].Name == path[0] {
			putExportPath(&(*fields)[i].Children, path[1:], value)
			return
		}
	}
	field := exportField{Name: path[0]}
	putExportPath(&field.Children, path[1:], value)
	*fields = append(*fields, field)
}
