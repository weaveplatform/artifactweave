package repo_test

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// legacyFalse are the legacy flags of a repository of none of their types.
const legacyFalse = `"enableBowerSupport": false, "enableChefSupport": false,
	"enableCocoaPodsSupport": false, "enableComposerSupport": false,
	"enableConanSupport": false, "enableDebianSupport": false,
	"enableDistRepoSupport": false, "enableGemsSupport": false,
	"enableGitLfsSupport": false, "enableNpmSupport": false,
	"enableNuGetSupport": false, "enablePuppetSupport": false,
	"enablePypiSupport": false, "enableVagrantSupport": false`

// baseV1 is what 7.161.26's RepositoryConfigurationBase gives every
// repository of a type other than Docker, OCI, Helm OCI, NuGet, Helm,
// Hugging Face, Debian, P2 and Conan, created with every default.
const baseV1 = legacyFalse + `, "enableDockerSupport": false,
	"description": "", "notes": "", "includesPattern": "**/*", "excludesPattern": "",
	"repoLayoutRef": "maven-2-default", "signedUrlTtl": 90, "environments": [],
	"ddebSupported": false, "debianTrivialLayout": false, "dockerApiVersion": "V2",
	"blockPushingSchema1": true, "dockerProjectId": "", "enableNormalizedVersion": false,
	"forceNugetAuthentication": false, "forceP2Authentication": false,
	"forceConanAuthentication": false, "forceNonDuplicateChart": false,
	"forceMetadataNameVersion": false, "mlRepoLayout": false, "priorityResolution": false`

// localV1 is what LocalRepositoryConfigurationImpl adds, the fields of
// RTFACT-31100's v1 read of a Maven local repository (7.104.10).
const localV1 = `"checksumPolicyType": "client-checksums", "handleReleases": true,
	"handleSnapshots": true, "maxUniqueSnapshots": 0, "maxUniqueTags": 0,
	"snapshotVersionBehavior": "unique", "suppressPomConsistencyChecks": false,
	"blackedOut": false, "propertySets": [], "archiveBrowsingEnabled": false,
	"calculateYumMetadata": false, "enableFileListsIndexing": false, "yumRootDepth": 0,
	"dockerTagRetention": 1, "enableComposerV1Indexing": false, "terraformType": "MODULE",
	"encryptStates": true, "downloadRedirect": false, "cdnRedirect": false,
	"cargoInternalIndex": true, "cargoAnonymousAccess": false, "xrayIndex": false,
	"xrayDataTtl": 90`

func readDoc(t *testing.T, doc string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatalf("%v in %s", err, doc)
	}
	return m
}

// An administrator's v1 read of a generic local, an RPM remote, a Maven
// virtual and a Docker federated repository, each created with every
// default, is its class's 7.161.26 read model whole (#1493): the fields of
// RTFACT-31100's Maven local read, with mlRepoLayout, which that read
// predates; a remote's credentials and local address as "", as every
// remote sample read of the Terraform provider gives them; no blackedOut,
// xrayIndex or redirect fields on a virtual repository, whose model has
// none; and no dockerProjectId on a Docker repository that set none, as
// RTFACT-29415's Docker local read shows.
func TestUnitFullIsTheClassReadModel(t *testing.T) {
	for _, tc := range []struct {
		c    repo.Config
		want string
	}{
		{
			repo.Config{Key: "generic-local", Class: repo.Local},
			`{"key": "generic-local", "rclass": "local", "packageType": "generic", ` +
				baseV1 + `, ` + localV1 + `}`,
		},
		{
			repo.Config{
				Key: "rpm-remote", Class: repo.Remote, PackageType: repo.RPM,
				URL: "https://up.example",
			},
			`{"key": "rpm-remote", "rclass": "remote", "packageType": "rpm", ` + baseV1 + `,
			"url": "https://up.example", "username": "", "password": "", "localAddress": "",
			"disableProxy": false, "handleReleases": true, "handleSnapshots": true,
			"suppressPomConsistencyChecks": false,
			"remoteRepoChecksumPolicyType": "generate-if-absent", "hardFail": false,
			"offline": false, "blackedOut": false, "storeArtifactsLocally": true,
			"socketTimeoutMillis": 15000, "retrievalCachePeriodSecs": 7200,
			"assumedOfflinePeriodSecs": 300, "missedRetrievalCachePeriodSecs": 1800,
			"metadataRetrievalTimeoutSecs": 60, "unusedArtifactsCleanupPeriodHours": 0,
			"fetchJarsEagerly": false, "fetchSourcesEagerly": false,
			"shareConfiguration": false, "synchronizeProperties": false,
			"maxUniqueSnapshots": 0, "maxUniqueTags": 0, "propertySets": [],
			"archiveBrowsingEnabled": false, "listRemoteFolderItems": false,
			"rejectInvalidJars": false, "allowAnyHostAuth": false,
			"enableCookieManagement": false, "enableTokenAuthentication": false,
			"propagateQueryParams": false, "blockMismatchingMimeTypes": true,
			"bypassHeadRequests": false, "disableUrlNormalization": false,
			"contentSynchronisation": {"enabled": false, "statistics": {"enabled": false},
				"properties": {"enabled": false}, "source": {"originAbsenceDetection": false}},
			"externalDependenciesEnabled": false, "sendContext": false, "passThrough": false,
			"curated": false, "retrieveSha256FromServer": false, "customHttpHeaders": [],
			"xrayIndex": false, "xrayDataTtl": 90, "downloadRedirect": false,
			"cdnRedirect": false, "cargoAnonymousAccess": false, "cargoInternalIndex": true}`,
		},
		{
			repo.Config{Key: "maven-virtual", Class: repo.Virtual, PackageType: repo.Maven},
			`{"key": "maven-virtual", "rclass": "virtual", "packageType": "maven", ` +
				baseV1 + `, "repositories": [], "hideUnauthorizedResources": false,
			"artifactoryRequestsCanRetrieveRemoteArtifacts": false,
			"resolveDockerTagsByTimestamp": false, "keyPair": "",
			"pomRepositoryReferencesCleanupPolicy": "discard_active_reference",
			"cachingLocalForeignLayersEnabled": false, "externalDependenciesEnabled": false,
			"virtualRetrievalCachePeriodSecs": 7200, "forceMavenAuthentication": false,
			"useNamespaces": false, "cargoInternalIndex": true, "cargoAnonymousAccess": false}`,
		},
		{
			repo.Config{Key: "docker-federated", Class: repo.Federated, PackageType: repo.Docker},
			`{"key": "docker-federated", "rclass": "federated", "packageType": "docker", ` +
				legacyFalse + `, "enableDockerSupport": true,
			"description": "", "notes": "", "includesPattern": "**/*", "excludesPattern": "",
			"repoLayoutRef": "maven-2-default", "signedUrlTtl": 90, "environments": [],
			"ddebSupported": false, "debianTrivialLayout": false, "dockerApiVersion": "V2",
			"blockPushingSchema1": true, "enableNormalizedVersion": false,
			"forceNugetAuthentication": false, "forceP2Authentication": false,
			"forceConanAuthentication": false, "forceNonDuplicateChart": false,
			"forceMetadataNameVersion": false, "mlRepoLayout": false,
			"priorityResolution": false, ` + localV1 + `, "disableProxy": false}`,
		},
	} {
		got := fieldsOf(t, repo.Full(*repo.Defaults(&tc.c)))
		if diff := cmp.Diff(readDoc(t, tc.want), got); diff != "" {
			t.Errorf("%s v1 (-want +got):\n%s", tc.c.Key, diff)
		}
	}
}

// The v2 read of the same repositories keeps only the fields 7.161.26's
// annotations name for their package type and class (#1494).
func TestUnitRelevantKeepsTheAnnotatedFields(t *testing.T) {
	common := []string{
		"key", "rclass", "packageType", "description", "notes", "includesPattern",
		"excludesPattern", "repoLayoutRef", "signedUrlTtl", "environments",
	}
	realRepo := []string{
		"priorityResolution", "blackedOut", "propertySets", "archiveBrowsingEnabled",
		"xrayIndex", "xrayDataTtl", "downloadRedirect",
	}
	for _, tc := range []struct {
		c    repo.Config
		want []string
	}{
		{
			repo.Config{Key: "generic-local", Class: repo.Local},
			slices.Concat(common, realRepo, []string{"cdnRedirect"}),
		},
		{
			repo.Config{
				Key: "rpm-remote", Class: repo.Remote, PackageType: repo.RPM,
				URL: "https://up.example",
			},
			slices.Concat(common, realRepo, []string{
				"url", "username", "password", "localAddress", "disableProxy", "hardFail",
				"offline", "storeArtifactsLocally", "socketTimeoutMillis",
				"retrievalCachePeriodSecs", "assumedOfflinePeriodSecs",
				"missedRetrievalCachePeriodSecs", "metadataRetrievalTimeoutSecs",
				"unusedArtifactsCleanupPeriodHours", "shareConfiguration",
				"synchronizeProperties", "listRemoteFolderItems", "allowAnyHostAuth",
				"enableCookieManagement", "propagateQueryParams", "blockMismatchingMimeTypes",
				"bypassHeadRequests", "disableUrlNormalization", "contentSynchronisation",
				"sendContext", "passThrough", "curated", "retrieveSha256FromServer",
				"customHttpHeaders",
			}),
		},
		{
			repo.Config{Key: "maven-virtual", Class: repo.Virtual, PackageType: repo.Maven},
			slices.Concat(common, []string{
				"repositories", "hideUnauthorizedResources",
				"artifactoryRequestsCanRetrieveRemoteArtifacts", "keyPair",
				"pomRepositoryReferencesCleanupPolicy", "forceMavenAuthentication",
			}),
		},
		{
			repo.Config{Key: "docker-federated", Class: repo.Federated, PackageType: repo.Docker},
			slices.Concat(common, realRepo, []string{
				"cdnRedirect", "dockerApiVersion", "blockPushingSchema1", "maxUniqueTags",
				"dockerTagRetention", "disableProxy",
			}),
		},
	} {
		got := slices.Sorted(
			maps.Keys(fieldsOf(t, repo.Relevant(repo.Full(*repo.Defaults(&tc.c))))),
		)
		slices.Sort(tc.want)
		if diff := cmp.Diff(tc.want, got); diff != "" {
			t.Errorf("%s v2 fields (-want +got):\n%s", tc.c.Key, diff)
		}
	}
}

// Each row of #1494: the v2 read keeps a field for the package types and
// classes its 7.161.26 annotation names, and leaves it out for the others.
func TestUnitRelevantFollowsEachAnnotation(t *testing.T) {
	values := map[string]string{
		"checksumPolicyType": `"client-checksums"`, "handleReleases": `true`,
		"maxUniqueSnapshots": `3`, "forceMavenAuthentication": `true`,
		"enableTokenAuthentication": `true`, "primaryKeyPairRef": `"pair"`,
		"externalDependenciesEnabled": `true`, "externalDependenciesPatterns": `["**"]`,
		"virtualRetrievalCachePeriodSecs": `60`, "keyPair": `"pair"`,
		"useNamespaces": `true`, "propagateQueryParams": `true`,
		"retrieveSha256FromServer": `true`, "vcsType": `"GIT"`,
		"terraformType": `"PROVIDER"`, "encryptStates": `false`,
		"forceMetadataNameVersion": `true`, "priorityResolution": `true`,
		"blackedOut": `true`, "xrayIndex": `true`, "downloadRedirect": `true`,
		"cdnRedirect": `true`,
	}
	for _, tc := range []struct {
		field string
		class repo.Class
		pt    repo.PackageType
		kept  bool
	}{
		{"checksumPolicyType", repo.Local, repo.Maven, true},
		{"checksumPolicyType", repo.Federated, repo.Gradle, true},
		{"checksumPolicyType", repo.Local, repo.NPM, false},
		{"handleReleases", repo.Remote, repo.Maven, true},
		{"handleReleases", repo.Federated, repo.NPM, false},
		{"maxUniqueSnapshots", repo.Remote, repo.Maven, true},
		{"maxUniqueSnapshots", repo.Remote, repo.VCS, true},
		{"maxUniqueSnapshots", repo.Local, repo.NuGet, true},
		{"maxUniqueSnapshots", repo.Remote, repo.NuGet, false},
		{"maxUniqueSnapshots", repo.Remote, repo.Gradle, false},
		{"forceMavenAuthentication", repo.Virtual, repo.Maven, true},
		{"forceMavenAuthentication", repo.Virtual, repo.Gradle, false},
		{"enableTokenAuthentication", repo.Remote, repo.Docker, true},
		{"enableTokenAuthentication", repo.Remote, repo.OCI, true},
		{"enableTokenAuthentication", repo.Remote, repo.Maven, false},
		{"primaryKeyPairRef", repo.Local, repo.RPM, true},
		{"primaryKeyPairRef", repo.Virtual, repo.Debian, true},
		{"primaryKeyPairRef", repo.Remote, repo.Alpine, true},
		{"primaryKeyPairRef", repo.Local, repo.Opkg, true},
		{"primaryKeyPairRef", repo.Local, repo.NPM, false},
		{"externalDependenciesEnabled", repo.Remote, repo.Docker, true},
		{"externalDependenciesEnabled", repo.Remote, repo.Bower, true},
		{"externalDependenciesEnabled", repo.Virtual, repo.NPM, true},
		{"externalDependenciesEnabled", repo.Remote, repo.Maven, false},
		{"externalDependenciesEnabled", repo.Virtual, repo.Docker, false},
		{"externalDependenciesPatterns", repo.Remote, repo.Helm, true},
		{"externalDependenciesPatterns", repo.Virtual, repo.Go, true},
		{"externalDependenciesPatterns", repo.Remote, repo.Bower, false},
		{"virtualRetrievalCachePeriodSecs", repo.Virtual, repo.NPM, true},
		{"virtualRetrievalCachePeriodSecs", repo.Virtual, repo.RPM, true},
		{"virtualRetrievalCachePeriodSecs", repo.Virtual, repo.Maven, false},
		{"keyPair", repo.Virtual, repo.P2, true},
		{"keyPair", repo.Virtual, repo.SBT, true},
		{"keyPair", repo.Virtual, repo.Debian, false},
		{"useNamespaces", repo.Virtual, repo.Helm, true},
		{"useNamespaces", repo.Virtual, repo.NPM, false},
		{"propagateQueryParams", repo.Remote, repo.NPM, true},
		{"retrieveSha256FromServer", repo.Remote, repo.Maven, true},
		{"vcsType", repo.Remote, repo.Composer, true},
		{"vcsType", repo.Remote, repo.CocoaPods, true},
		{"vcsType", repo.Remote, repo.Terraform, false},
		{"terraformType", repo.Local, repo.Terraform, true},
		{"terraformType", repo.Local, repo.TerraformBackend, false},
		{"encryptStates", repo.Local, repo.TerraformBackend, true},
		{"encryptStates", repo.Local, repo.Terraform, false},
		{"forceMetadataNameVersion", repo.Local, repo.Helm, true},
		{"forceMetadataNameVersion", repo.Federated, repo.Helm, false},
		{"forceMetadataNameVersion", repo.Virtual, repo.Helm, false},
		{"priorityResolution", repo.Federated, repo.Generic, true},
		{"priorityResolution", repo.Virtual, repo.Generic, false},
		{"blackedOut", repo.Remote, repo.Generic, true},
		{"blackedOut", repo.Virtual, repo.Generic, false},
		{"xrayIndex", repo.Remote, repo.Generic, true},
		{"xrayIndex", repo.Virtual, repo.Generic, false},
		{"downloadRedirect", repo.Remote, repo.Generic, true},
		{"downloadRedirect", repo.Virtual, repo.Generic, false},
		{"cdnRedirect", repo.Federated, repo.Generic, true},
		{"cdnRedirect", repo.Remote, repo.Generic, false},
		{"cdnRedirect", repo.Virtual, repo.Generic, false},
	} {
		c := repo.Config{Key: "k", Class: tc.class, PackageType: tc.pt, URL: "https://up.example"}
		if tc.class != repo.Remote {
			c.URL = ""
		}
		if err := json.Unmarshal([]byte(`{"`+tc.field+`":`+values[tc.field]+`}`), &c); err != nil {
			t.Fatal(err)
		}
		_, got := fieldsOf(t, repo.Relevant(repo.Full(*repo.Defaults(&c))))[tc.field]
		if got != tc.kept {
			t.Errorf("%s %s %s: kept %v, want %v", tc.class, tc.pt, tc.field, got, tc.kept)
		}
	}
}

// The v1 read takes a setting that 7.161.26's model reads from the
// configuration of one package type from a repository of that type alone,
// with the model's default when it stored none, and gives every other
// repository the default (#1493).
func TestUnitFullTakesEachSettingFromItsOwnType(t *testing.T) {
	for _, tc := range []struct {
		name  string
		c     repo.Config
		field string
		want  any
	}{
		{"nuget keeps", repo.Config{
			Class: repo.Local, PackageType: repo.NuGet, EnableNormalizedVersion: ptr(true),
		}, "enableNormalizedVersion", true},
		{"maven drops", repo.Config{
			Class: repo.Local, PackageType: repo.Maven, EnableNormalizedVersion: ptr(true),
		}, "enableNormalizedVersion", false},
		{"helm local keeps", repo.Config{
			Class: repo.Local, PackageType: repo.Helm, ForceNonDuplicateChart: ptr(true),
		}, "forceNonDuplicateChart", true},
		{"helm virtual drops", repo.Config{
			Class: repo.Virtual, PackageType: repo.Helm, ForceNonDuplicateChart: ptr(true),
		}, "forceNonDuplicateChart", false},
		{"huggingface remote keeps", repo.Config{
			Class: repo.Remote, PackageType: repo.HuggingFaceML, MLRepoLayout: ptr(true),
		}, "mlRepoLayout", true},
		{"generic drops", repo.Config{
			Class: repo.Local, MLRepoLayout: ptr(true),
		}, "mlRepoLayout", false},
		{"rpm keeps", repo.Config{
			Class: repo.Local, PackageType: repo.RPM, CalculateYumMetadata: ptr(true),
			YumRootDepth: ptr(2), EnableFileListsIndexing: repo.Flex(true),
			YumGroupFileNames: "groups.xml",
		}, "yumGroupFileNames", "groups.xml"},
		{"rpm depth", repo.Config{
			Class: repo.Federated, PackageType: repo.RPM, YumRootDepth: ptr(2),
		}, "yumRootDepth", float64(2)},
		{"generic yum", repo.Config{
			Class: repo.Local, CalculateYumMetadata: ptr(true),
		}, "calculateYumMetadata", false},
		{"composer keeps", repo.Config{
			Class: repo.Local, PackageType: repo.Composer, EnableComposerV1Indexing: ptr(true),
		}, "enableComposerV1Indexing", true},
		{"terraform keeps", repo.Config{
			Class: repo.Local, PackageType: repo.Terraform, TerraformType: repo.TerraformProvider,
		}, "terraformType", "PROVIDER"},
		{"terraform unset", repo.Config{
			Class: repo.Local, PackageType: repo.Terraform,
		}, "terraformType", nil},
		{"backend keeps", repo.Config{
			Class: repo.Local, PackageType: repo.TerraformBackend, EncryptStates: ptr(false),
		}, "encryptStates", false},
		{"generic states", repo.Config{
			Class: repo.Local, EncryptStates: ptr(false),
		}, "encryptStates", true},
		{"cargo keeps", repo.Config{
			Class: repo.Local, PackageType: repo.Cargo, CargoInternalIndex: ptr(false),
		}, "cargoInternalIndex", false},
		{"cargo virtual", repo.Config{
			Class: repo.Virtual, PackageType: repo.Cargo, CargoAnonymousAccess: ptr(true),
		}, "cargoAnonymousAccess", true},
		{"docker project", repo.Config{
			Class: repo.Remote, PackageType: repo.Docker, DockerProjectID: "p",
		}, "dockerProjectId", "p"},
		{"maven project", repo.Config{
			Class: repo.Remote, PackageType: repo.Maven, DockerProjectID: "p",
		}, "dockerProjectId", ""},
		{"docker schema1", repo.Config{
			Class: repo.Local, PackageType: repo.Docker, BlockPushingSchema1: repo.Flex(false),
		}, "blockPushingSchema1", false},
		{"docker virtual schema1", repo.Config{
			Class: repo.Virtual, PackageType: repo.Docker, BlockPushingSchema1: repo.Flex(false),
		}, "blockPushingSchema1", true},
		{"maven schema1", repo.Config{
			Class: repo.Local, PackageType: repo.Maven, BlockPushingSchema1: repo.Flex(false),
		}, "blockPushingSchema1", true},
		{"oci api", repo.Config{
			Class: repo.Local, PackageType: repo.OCI, DockerAPIVersion: repo.DockerV1,
		}, "dockerApiVersion", "V1"},
		{"maven api", repo.Config{
			Class: repo.Local, PackageType: repo.Maven, DockerAPIVersion: repo.DockerV1,
		}, "dockerApiVersion", "V2"},
		{"docker layers", repo.Config{
			Class: repo.Virtual, PackageType: repo.Docker,
			CachingLocalForeignLayersEnabled: ptr(true),
		}, "cachingLocalForeignLayersEnabled", true},
		{"set headers", repo.Config{
			Class: repo.Remote, CustomHTTPHeaders: []repo.CustomHTTPHeader{{Name: "X", Value: "v"}},
		}, "customHttpHeaders", []any{map[string]any{
			"name": "X", "value": "v", "sensitive": false,
		}}},
		{"virtual blacked out", repo.Config{
			Class: repo.Virtual, BlackedOut: ptr(true),
		}, "blackedOut", nil},
	} {
		tc.c.Key = "k"
		got := fieldsOf(t, repo.Full(tc.c))[tc.field]
		if diff := cmp.Diff(tc.want, got); diff != "" {
			t.Errorf("%s: %s (-want +got):\n%s", tc.name, tc.field, diff)
		}
	}
}

// A Docker, OCI or Helm OCI repository created without blockPushingSchema1
// stores true, the default of 7.161.26's descriptor and read model, and
// every read shows it; a virtual one stores none, and its v1 read gives the
// model's true (#1493).
func TestUnitDockerRepositoriesBlockSchema1ByDefault(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Remote, repo.Federated, repo.Virtual} {
		for _, pt := range []repo.PackageType{repo.Docker, repo.OCI, repo.HelmOCI} {
			c := *repo.Defaults(&repo.Config{Key: "k", Class: class, PackageType: pt})
			if stored := c.BlockPushingSchema1; (stored != nil) != (class != repo.Virtual) ||
				stored != nil && !stored.Value {
				t.Errorf("%s %s stored %v", class, pt, stored)
			}
			for name, read := range map[string]repo.Config{
				"v1": repo.Full(c), "v2": repo.Relevant(repo.Full(c)),
			} {
				got, ok := fieldsOf(t, read)["blockPushingSchema1"]
				if want := name == "v1" || class != repo.Virtual; ok != want ||
					ok && got != true {
					t.Errorf(
						"%s %s %s blockPushingSchema1 %v (present %v)",
						class,
						pt,
						name,
						got,
						ok,
					)
				}
			}
		}
	}
}

// The shared remote configuration is the remote's configuration masked as
// 7.161.26 masks its remote descriptor, with the descriptor's defaults for
// what was never set (#1493).
func TestUnitSharedIsTheMaskedDescriptor(t *testing.T) {
	c := *repo.Defaults(&repo.Config{
		Key: "maven-remote", Class: repo.Remote, PackageType: repo.Maven,
		URL: "https://up.example", Username: "u", Password: "p", Proxy: "corp",
		LocalAddress: "10.0.0.1", PropertySets: []string{"artifactory"},
		ShareConfiguration: ptr(true),
	})
	want := readDoc(t, `{"key": "maven-remote", "rclass": "remote", "packageType": "maven",
		"description": "", "notes": "", "includesPattern": "**/*", "excludesPattern": "",
		"repoLayoutRef": "maven-2-default", "environments": [], "signedUrlTtl": 90,
		"dockerApiVersion": "V2", "blockPushingSchema1": true, "maxUniqueTags": 0,
		"archiveBrowsingEnabled": false, "ddebSupported": false,
		"propagateQueryParams": false, "retrieveSha256FromServer": false,
		"customHttpHeaders": [], "url": "https://up.example", "blackedOut": false,
		"xrayIndex": false, "priorityResolution": false, "downloadRedirect": false,
		"cdnRedirect": false, "handleReleases": true, "handleSnapshots": true,
		"maxUniqueSnapshots": 0, "suppressPomConsistencyChecks": false,
		"remoteRepoChecksumPolicyType": "generate-if-absent", "disableProxy": false,
		"hardFail": false, "offline": false, "storeArtifactsLocally": true,
		"socketTimeoutMillis": 15000, "retrievalCachePeriodSecs": 7200,
		"missedRetrievalCachePeriodSecs": 1800, "metadataRetrievalTimeoutSecs": 60,
		"assumedOfflinePeriodSecs": 300, "unusedArtifactsCleanupPeriodHours": 0,
		"fetchJarsEagerly": false, "fetchSourcesEagerly": false, "rejectInvalidJars": false,
		"shareConfiguration": true, "synchronizeProperties": false,
		"blockMismatchingMimeTypes": true, "allowAnyHostAuth": false,
		"enableCookieManagement": false, "enableTokenAuthentication": false,
		"bypassHeadRequests": false, "listRemoteFolderItems": false,
		"disableUrlNormalization": false, "externalDependenciesEnabled": false,
		"forceNugetAuthentication": false, "forceP2Authentication": false,
		"forceConanAuthentication": false}`)
	if diff := cmp.Diff(want, fieldsOf(t, repo.Shared(c))); diff != "" {
		t.Errorf("shared (-want +got):\n%s", diff)
	}
}

// Shown never reaches a stored document, and an empty field outside it
// stays out of a read.
func TestUnitOnlyFullShowsEmptyFields(t *testing.T) {
	c := repo.Config{Key: "k", Class: repo.Remote, URL: "https://up.example"}
	for name, read := range map[string]repo.Config{
		"stored": c, "masked": repo.Masked(c),
	} {
		for _, field := range []string{"username", "password", "localAddress", "customHttpHeaders"} {
			if _, ok := fieldsOf(t, read)[field]; ok {
				t.Errorf("%s has %s", name, field)
			}
		}
	}
	if got := fieldsOf(t, repo.Full(c))["username"]; got != "" {
		t.Errorf("v1 username %v", got)
	}
}

// A remote with a password reads without one, as Masked leaves it, so a
// client that writes the read back keeps the password; one without reads
// "" (#1493).
func TestUnitFullLeavesAStoredPasswordOut(t *testing.T) {
	c := repo.Config{Key: "k", Class: repo.Remote, URL: "https://up.example", Password: "secret"}
	if v, ok := fieldsOf(t, repo.Full(c))["password"]; ok {
		t.Errorf("password %v", v)
	}
	c.Password = ""
	if v, ok := fieldsOf(t, repo.Full(c))["password"]; !ok || v != "" {
		t.Errorf("no password reads %v (present %v)", v, ok)
	}
}
