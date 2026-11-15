package repo_test

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// The shared remote configuration is 7.161.26's masked HttpRepoDescriptor
// as its REST JsonProvider writes it: "@class" and key first, no username,
// password, proxy, local address or property sets, the descriptor's names
// and nesting, and its defaults for what was never set (#1493).
func TestUnitDescriptorIsTheMaskedHTTPRepoDescriptor(t *testing.T) {
	c := *repo.Defaults(&repo.Config{
		Key: "maven-remote", Class: repo.Remote, PackageType: repo.Maven,
		Description: "Maven Central", URL: "https://up.example/maven2", Username: "u",
		Password: "p", Proxy: "corp", LocalAddress: "10.0.0.1",
		PropertySets: []string{"artifactory"}, ShareConfiguration: ptr(true),
		CustomHTTPHeaders: []repo.CustomHTTPHeader{
			{Name: "X-Token", Value: "secret", Sensitive: true},
			{Name: "X-Team", Value: "build"},
		},
	})
	var want bytes.Buffer
	if err := json.Compact(&want, []byte(`{
		"@class": "org.artifactory.descriptor.repo.HttpRepoDescriptor", "key": "maven-remote",
		"allowAnyHostAuth": false, "socketTimeoutMillis": 15000,
		"enableCookieManagement": false, "enableTokenAuthentication": false,
		"disableProxy": false, "propagateQueryParams": false,
		"disableUrlNormalization": false, "retrieveSha256FromServer": false,
		"customHttpHeader": [{"name": "X-Token", "value": "", "sensitive": true},
			{"name": "X-Team", "value": "build", "sensitive": false}],
		"type": "Maven", "description": "Maven Central", "includesPattern": "**/*",
		"dockerApiVersion": "V2", "forceNugetAuthentication": false,
		"forceConanAuthentication": false, "ddebSupported": false, "signedUrlTtl": 90,
		"blackedOut": false, "handleReleases": true, "handleSnapshots": true,
		"maxUniqueSnapshots": 0, "maxUniqueTags": 0, "blockPushingSchema1": true,
		"suppressPomConsistencyChecks": false, "archiveBrowsingEnabled": false,
		"priorityResolution": false, "storeArtifactsLocally": true, "fetchJarsEagerly": false,
		"fetchSourcesEagerly": false, "shareConfiguration": true,
		"url": "https://up.example/maven2", "hardFail": false, "offline": false,
		"retrievalCachePeriodSecs": 7200, "metadataRetrievalTimeoutSecs": 60,
		"assumedOfflinePeriodSecs": 300, "missedRetrievalCachePeriodSecs": 1800,
		"unusedArtifactsCleanupPeriodHours": 0, "synchronizeProperties": false,
		"listRemoteFolderItems": false, "bypassHeadRequests": false, "rejectInvalidJars": false,
		"blockMismatchingMimeTypes": true, "repoLayoutRef": "maven-2-default",
		"xray": {"enabled": false, "dataTtl": 90, "xrayNotAvailableMode": null},
		"downloadRedirect": {"enabled": false}, "cdnRedirect": {"enabled": false},
		"remoteRepoChecksumPolicyType": "GEN_IF_ABSENT",
		"contentSynchronisation": {"enabled": false, "statistics": {"enabled": false},
			"properties": {"enabled": false}, "source": {"originAbsenceDetection": false}}}`)); err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(repo.Descriptor(c))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want.Bytes()) {
		t.Errorf("descriptor\n got %s\nwant %s", got, want.Bytes())
	}
}

// What the remote stored reaches the descriptor under the descriptor's
// names, and the class-wide settings a type does not own keep the
// descriptor's defaults whatever was stored.
func TestUnitDescriptorStoredValues(t *testing.T) {
	c := *repo.Defaults(&repo.Config{
		Key: "docker-remote", Class: repo.Remote, PackageType: repo.Docker,
		URL: "https://registry-1.docker.io", Notes: "n", ExcludesPattern: ptr("x/**"),
		IncludesPattern: ptr(""), BlackedOut: ptr(true), XrayIndex: ptr(true),
		XrayDataTTL: ptr(30), DownloadRedirect: repo.Flex(true), CDNRedirect: repo.Flex(true),
		PriorityResolution: repo.Flex(true), SignedURLTTL: ptr(60),
		PrimaryKeyPairRef: "pk", SecondaryKeyPairRef: "sk", RemoteRepoLayoutRef: "simple-default",
		MaxUniqueTags: ptr(5), BlockPushingSchema1: repo.Flex(false), DockerAPIVersion: repo.DockerV2,
		ExternalDependenciesEnabled:  ptr(true),
		ExternalDependenciesPatterns: []string{"**/*.example/**"},
		QueryParams:                  "a=b", ClientTLSCertificate: "cert",
		MismatchingMimeTypesOverrideList: "text/html", ListRemoteFolderItems: repo.Flex(true),
		RemoteRepoChecksumPolicyType: repo.PassThru, FetchJarsEagerly: ptr(true),
		SuppressPomConsistencyChecks: ptr(false), ForceNugetAuthentication: ptr(true),
		ContentSynchronisation: &repo.ContentSynchronisation{
			Enabled: true, Statistics: &repo.EnabledFlag{Enabled: true},
			Properties: &repo.EnabledFlag{Enabled: true},
			Source:     &repo.ContentSyncSource{OriginAbsenceDetection: true},
		},
	})
	want := readDoc(t, `{"notes": "n", "excludesPattern": "x/**", "includesPattern": "",
		"blackedOut": true, "xray": {"enabled": true, "dataTtl": 30, "xrayNotAvailableMode": null},
		"downloadRedirect": {"enabled": true}, "cdnRedirect": {"enabled": true},
		"priorityResolution": true, "signedUrlTtl": 60, "primaryKeyPairRef": "pk",
		"secondaryKeyPairRef": "sk", "remoteRepoLayoutRef": "simple-default", "maxUniqueTags": 5,
		"blockPushingSchema1": false, "dockerApiVersion": "V2",
		"externalDependencies": {"enabled": true, "pattern": ["**/*.example/**"]},
		"queryParams": "a=b", "clientTlsCertificate": "cert",
		"mismatchingMimeTypesOverrideList": "text/html", "listRemoteFolderItems": true,
		"remoteRepoChecksumPolicyType": "PASS_THRU", "fetchJarsEagerly": false,
		"suppressPomConsistencyChecks": true, "forceNugetAuthentication": false,
		"retrievalCachePeriodSecs": 21600, "type": "Docker",
		"contentSynchronisation": {"enabled": true, "statistics": {"enabled": true},
			"properties": {"enabled": true}, "source": {"originAbsenceDetection": true}}}`)
	got := descriptorFields(t, repo.Descriptor(c))
	if diff := cmp.Diff(want, filterKeys(got, slices.Collect(maps.Keys(want)))); diff != "" {
		t.Errorf("stored values (-want +got):\n%s", diff)
	}
	// A docker repository's own settings are V2, true and 0 for any other
	// type, whatever it stored.
	c.PackageType = repo.Generic
	got = descriptorFields(t, repo.Descriptor(c))
	for f, v := range map[string]any{
		"maxUniqueTags": float64(0), "blockPushingSchema1": true, "dockerApiVersion": "V2",
	} {
		if got[f] != v {
			t.Errorf("generic %s = %v, want %v", f, got[f], v)
		}
	}
	for policy, name := range map[repo.RemoteChecksumPolicy]string{
		repo.GenerateIfAbsent: "GEN_IF_ABSENT", repo.Fail: "FAIL",
		repo.IgnoreAndGenerate: "IGNORE_AND_GEN", repo.PassThru: "PASS_THRU", "": "GEN_IF_ABSENT",
	} {
		c.RemoteRepoChecksumPolicyType = policy
		if got := repo.Descriptor(c).RemoteRepoChecksumPolicyType; got != name {
			t.Errorf("%q: %s, want %s", policy, got, name)
		}
	}
}

// Each type's own settings follow HttpRepoDescriptor.setPackageTypeConfig,
// for a remote created with every default.
func TestUnitDescriptorTypeSettings(t *testing.T) {
	typeObjects := []string{
		"externalDependencies", "nuget", "pypi", "vcs", "bower", "composer", "cocoaPods", "helm",
		"git", "terraform", "hex", "go", "huggingFace", "ansible", "cargoConfig",
	}
	vcs := `"vcs": {"type": "GIT", "git": {"provider": "GITHUB"}}`
	ext := `"externalDependencies": {"enabled": false}`
	for _, tc := range []struct {
		t    repo.PackageType
		want string
	}{
		{repo.Generic, `{}`},
		{repo.NuGet, `{"nuget": {"feedContextPath": "api/v2", "downloadContextPath": "api/v2/package",
			"v3FeedUrl": "https://api.nuget.org/v3/index.json", "symbolServerUrl": ""}}`},
		{repo.PyPI, `{"pypi": {"pyPIRegistryUrl": "https://pypi.org", "repositorySuffix": "simple"}}`},
		{repo.Bower, `{"bower": {"bowerRegistryUrl": "https://registry.bower.io"}, ` + vcs + `}`},
		{repo.Composer, `{"composer": {"composerRegistryUrl": "https://packagist.org"}, ` + vcs + `}`},
		{repo.CocoaPods, `{"cocoaPods": {"cocoaPodsForceModifyIndex": false,
			"specRepoProvider": {"provider": "GITHUB"}}, ` + vcs + `, ` + ext + `}`},
		{repo.Helm, `{"helm": {}, ` + ext + `}`},
		{repo.Go, `{"go": {"gitLabResolveSubgroups": false}, ` + vcs + `, ` + ext + `}`},
		{repo.Terraform, `{"terraform": {"terraformRegistryUrl": "https://registry.terraform.io",
			"terraformProvidersUrl": "https://releases.hashicorp.com"}, ` + vcs + `, ` + ext + `}`},
		{repo.VCS, `{` + vcs + `}`},
		{repo.Hex, `{"hex": {"hexRepositoryUrl": "https://repo.hex.pm"}}`},
		{repo.HuggingFaceML, `{"huggingFace": {"mlRepoLayout": false}}`},
		{repo.Ansible, `{"ansible": {}}`},
		{repo.Cargo, `{"cargoConfig": {"cargoAnonymousAccess": false, "cargoInternalIndex": true},
			"git": {"registryUrl": "https://github.com/rust-lang/crates.io-index"}}`},
		{repo.Docker, `{` + ext + `}`},
		{repo.OCI, `{` + ext + `}`},
		{repo.HelmOCI, `{` + ext + `}`},
		{repo.Swift, `{` + ext + `}`},
		{repo.AIEditorExtensions, `{` + ext + `}`},
	} {
		c := *repo.Defaults(&repo.Config{
			Key: "r", Class: repo.Remote, PackageType: tc.t, URL: "https://up.example",
		})
		got := filterKeys(descriptorFields(t, repo.Descriptor(c)), typeObjects)
		if diff := cmp.Diff(readDoc(t, tc.want), got); diff != "" {
			t.Errorf("%s (-want +got):\n%s", tc.t, diff)
		}
	}
	// A VCS remote has its vcs object even with nothing stored, and the
	// provider and download URL a remote stored.
	d := repo.Descriptor(repo.Config{Key: "v", Class: repo.Remote, PackageType: repo.VCS})
	if d.VCS == nil || d.VCS.Type != "GIT" || d.VCS.Git.Provider != repo.GitHub {
		t.Errorf("bare vcs %+v", d.VCS)
	}
	d = repo.Descriptor(repo.Config{
		Key: "g", Class: repo.Remote, PackageType: repo.Go, VCSType: "GIT",
		VCSGitProvider: repo.CustomVCS, VCSGitDownloadURL: "https://git.example/{0}",
	})
	if d.VCS == nil || d.VCS.Git != (repo.DescriptorVCSGit{
		Provider: repo.CustomVCS, DownloadURL: "https://git.example/{0}",
	}) {
		t.Errorf("custom vcs %+v", d.VCS)
	}
	// A Go remote without a vcsType has no vcs object, and one without
	// externalDependenciesEnabled no externalDependencies.
	d = repo.Descriptor(repo.Config{Key: "g", Class: repo.Remote, PackageType: repo.Go})
	if d.VCS != nil || d.ExternalDependencies != nil {
		t.Errorf("bare go %+v %+v", d.VCS, d.ExternalDependencies)
	}
	// The flags a type owns, and the stored values of the per-type objects.
	for _, tc := range []struct {
		c    repo.Config
		want string
	}{
		{
			repo.Config{PackageType: repo.NuGet, ForceNugetAuthentication: ptr(true)},
			`{"forceNugetAuthentication": true}`,
		},
		{
			repo.Config{PackageType: repo.Conan, ForceConanAuthentication: ptr(true)},
			`{"forceConanAuthentication": true}`,
		},
		{repo.Config{PackageType: repo.Debian, DdebSupported: ptr(true)}, `{"ddebSupported": true}`},
		{
			repo.Config{
				PackageType: repo.Gradle, FetchJarsEagerly: ptr(true),
				FetchSourcesEagerly: ptr(true), RejectInvalidJars: ptr(true),
			},
			`{"fetchJarsEagerly": true, "fetchSourcesEagerly": true, "rejectInvalidJars": true,
			"suppressPomConsistencyChecks": true}`,
		},
		{
			repo.Config{PackageType: repo.Helm, ChartsBaseURL: "https://charts.example"},
			`{"helm": {"chartsBaseUrl": "https://charts.example"}}`,
		},
		{
			repo.Config{PackageType: repo.Hex, HexPublicKey: "KEY"},
			`{"hex": {"hexRepositoryUrl": "https://repo.hex.pm", "hexPublicKey": "KEY"}}`,
		},
		{
			repo.Config{PackageType: repo.CocoaPods, PodsSpecsRepoURL: "https://specs.example"},
			`{"cocoaPods": {"cocoaPodsSpecsRepoUrl": "https://specs.example",
			"cocoaPodsForceModifyIndex": false, "specRepoProvider": {"provider": "GITHUB"}}}`,
		},
		{
			repo.Config{PackageType: repo.HuggingFaceML, MLRepoLayout: ptr(true)},
			`{"huggingFace": {"mlRepoLayout": true}}`,
		},
		{
			repo.Config{
				PackageType: repo.Cargo, CargoAnonymousAccess: ptr(true),
				CargoInternalIndex: ptr(false),
			},
			`{"cargoConfig": {"cargoAnonymousAccess": true, "cargoInternalIndex": false}}`,
		},
	} {
		tc.c.Key, tc.c.Class = "r", repo.Remote
		want := readDoc(t, tc.want)
		got := filterKeys(
			descriptorFields(t, repo.Descriptor(tc.c)),
			slices.Collect(maps.Keys(want)),
		)
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("%s (-want +got):\n%s", tc.c.PackageType, diff)
		}
	}
}

// descriptorFields is d as a client decodes it.
func descriptorFields(t *testing.T, d repo.RemoteDescriptor) map[string]any {
	t.Helper()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return readDoc(t, string(b))
}

// filterKeys is the part of doc under keys.
func filterKeys(doc map[string]any, keys []string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := doc[k]; ok {
			out[k] = v
		}
	}
	return out
}
