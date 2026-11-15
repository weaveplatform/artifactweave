package repo

import (
	"reflect"
	"slices"
	"strings"
)

// readRule keeps a field in the v2 read of a repository whose package type
// is in types and whose class is in classes; nil means any.
type readRule struct {
	types   []PackageType
	classes []Class
}

func (r readRule) keeps(c *Config) bool {
	return (r.types == nil || slices.Contains(r.types, c.PackageType)) &&
		(r.classes == nil || slices.Contains(r.classes, c.Class))
}

// The groups 7.161.26's IncludeTypeSpecific annotations name most often.
var (
	mavenTypes  = []PackageType{Maven, Gradle, Ivy, SBT}
	dockerTypes = []PackageType{Docker, OCI, HelmOCI}
	hosted      = []Class{Local, Federated}
	realRepos   = []Class{Local, Federated, Remote}
	remoteOnly  = []Class{Remote}
	virtualOnly = []Class{Virtual}
)

// v2Fields gives, for each field the v2 read leaves out for some
// repositories, the rules under which it keeps it: one per
// IncludeTypeSpecific annotation of 7.161.26's read model, whose v2
// serializer (TypeSpecificFilter) writes a field only when an annotation
// names the repository's package type and class. A field with no rule is
// never kept: the legacy enable<Type>Support flags, which the model does
// not annotate. A field missing from the map is kept for every repository:
// the ones the model annotates for every type and class (key, rclass,
// packageType, description, notes, the patterns, repoLayoutRef,
// signedUrlTtl, projectKey and environments), and a remote's class fields,
// which no other class holds. p2ChildOrigins has no 7.x counterpart and is
// kept where it acts, on a P2 repository.
//
// That the v2 read and the configurations list are written through
// TypeSpecificFilter is inferred [I]: the handlers are in the closed
// RestAddon. JFrog's samples of a generic local repository's v2 read and
// configurations list entry have exactly the fields these rules keep.
var v2Fields = map[string][]readRule{
	"blackedOut":          {{nil, realRepos}},
	"xrayIndex":           {{nil, realRepos}},
	"xrayDataTtl":         {{nil, realRepos}},
	"propertySets":        {{nil, realRepos}},
	"priorityResolution":  {{nil, realRepos}},
	"downloadRedirect":    {{nil, realRepos}},
	"cdnRedirect":         {{nil, hosted}},
	"primaryKeyPairRef":   {{[]PackageType{Alpine, RPM, Debian, Opkg}, nil}},
	"secondaryKeyPairRef": {{[]PackageType{Alpine, RPM, Debian, Opkg}, nil}},
	"members":             {{nil, []Class{Virtual, Remote, Federated}}},
	"proxy":               {{nil, realRepos}},
	"disableProxy":        {{nil, realRepos}},

	"checksumPolicyType":           {{mavenTypes, hosted}},
	"handleReleases":               {{mavenTypes, realRepos}},
	"handleSnapshots":              {{mavenTypes, realRepos}},
	"snapshotVersionBehavior":      {{mavenTypes, hosted}},
	"suppressPomConsistencyChecks": {{mavenTypes, realRepos}},
	"maxUniqueSnapshots": {
		{[]PackageType{Maven, NuGet, Gradle, Ivy, SBT}, hosted},
		{[]PackageType{Maven, VCS}, remoteOnly},
	},
	"remoteRepoChecksumPolicyType": {{mavenTypes, remoteOnly}},
	"fetchJarsEagerly":             {{mavenTypes, remoteOnly}},
	"fetchSourcesEagerly":          {{mavenTypes, remoteOnly}},
	"rejectInvalidJars":            {{mavenTypes, remoteOnly}},
	"keyPair": {
		{[]PackageType{Maven, Gradle, Ivy, SBT, P2}, virtualOnly},
	},
	"pomRepositoryReferencesCleanupPolicy": {{mavenTypes, virtualOnly}},
	"forceMavenAuthentication":             {{[]PackageType{Maven}, virtualOnly}},

	"maxUniqueTags":                    {{dockerTypes, hosted}},
	"dockerTagRetention":               {{dockerTypes, hosted}},
	"dockerApiVersion":                 {{dockerTypes, nil}},
	"blockPushingSchema1":              {{dockerTypes, realRepos}},
	"dockerProjectId":                  {{dockerTypes, remoteOnly}},
	"enableTokenAuthentication":        {{dockerTypes, remoteOnly}},
	"resolveDockerTagsByTimestamp":     {{dockerTypes, virtualOnly}},
	"cachingLocalForeignLayersEnabled": {{dockerTypes, virtualOnly}},

	"archiveBrowsingEnabled":   {{nil, realRepos}},
	"calculateYumMetadata":     {{[]PackageType{RPM}, hosted}},
	"enableFileListsIndexing":  {{[]PackageType{RPM}, hosted}},
	"yumRootDepth":             {{[]PackageType{RPM}, hosted}},
	"yumGroupFileNames":        {{[]PackageType{RPM}, hosted}},
	"enableComposerV1Indexing": {{[]PackageType{Composer}, hosted}},
	"terraformType":            {{[]PackageType{Terraform}, hosted}},
	"encryptStates":            {{[]PackageType{TerraformBackend}, []Class{Local}}},
	"cargoAnonymousAccess":     {{[]PackageType{Cargo}, nil}},
	"cargoInternalIndex":       {{[]PackageType{Cargo}, nil}},

	"debianTrivialLayout": {{[]PackageType{Debian}, hosted}},
	"ddebSupported":       {{[]PackageType{Debian}, nil}},
	"optionalIndexCompressionFormats": {
		{[]PackageType{Debian}, []Class{Local, Federated, Virtual}},
	},
	"debianDefaultArchitectures": {{[]PackageType{Debian}, virtualOnly}},

	"forceNugetAuthentication": {{[]PackageType{NuGet}, nil}},
	"enableNormalizedVersion":  {{[]PackageType{NuGet}, nil}},
	"feedContextPath":          {{[]PackageType{NuGet}, remoteOnly}},
	"downloadContextPath":      {{[]PackageType{NuGet}, remoteOnly}},
	"v3FeedUrl":                {{[]PackageType{NuGet}, remoteOnly}},
	"symbolServerUrl":          {{[]PackageType{NuGet}, remoteOnly}},
	"forceConanAuthentication": {{[]PackageType{Conan}, nil}},
	"forceP2Authentication":    {{[]PackageType{P2}, nil}},
	"p2Urls":                   {{[]PackageType{P2}, virtualOnly}},
	"p2ChildOrigins":           {{[]PackageType{P2}, nil}},

	"forceNonDuplicateChart":   {{[]PackageType{Helm}, []Class{Local}}},
	"forceMetadataNameVersion": {{[]PackageType{Helm}, []Class{Local}}},
	"chartsBaseUrl":            {{[]PackageType{Helm}, remoteOnly}},
	"useNamespaces":            {{[]PackageType{Helm}, virtualOnly}},
	"mlRepoLayout": {
		{[]PackageType{HuggingFaceML}, []Class{Local, Remote}},
	},

	"pyPIRegistryUrl":       {{[]PackageType{PyPI}, remoteOnly}},
	"pyPIRepositorySuffix":  {{[]PackageType{PyPI}, remoteOnly}},
	"terraformRegistryUrl":  {{[]PackageType{Terraform}, remoteOnly}},
	"terraformProvidersUrl": {{[]PackageType{Terraform}, remoteOnly}},
	"bowerRegistryUrl":      {{[]PackageType{Bower}, remoteOnly}},
	"composerRegistryUrl":   {{[]PackageType{Composer}, remoteOnly}},
	"gitRegistryUrl":        {{[]PackageType{Cargo}, remoteOnly}},
	"podsSpecsRepoUrl":      {{[]PackageType{CocoaPods}, remoteOnly}},
	"podsCdnUrl":            {{[]PackageType{CocoaPods}, remoteOnly}},
	"hexPublicKey":          {{[]PackageType{Hex}, remoteOnly}},
	"vcsType":               {{[]PackageType{VCS, Bower, Go, CocoaPods, Composer}, remoteOnly}},
	"vcsGitProvider":        {{[]PackageType{VCS, Bower, Go, CocoaPods, Composer}, remoteOnly}},
	"vcsGitDownloadUrl":     {{[]PackageType{VCS, Bower, Go, CocoaPods, Composer}, remoteOnly}},

	"externalDependenciesEnabled": {
		{[]PackageType{
			Bower, Docker, OCI, CocoaPods, Swift, HelmOCI, Helm, Terraform, AIEditorExtensions,
		}, remoteOnly},
		{[]PackageType{Bower, Go, NPM}, virtualOnly},
	},
	"externalDependenciesPatterns": {
		{[]PackageType{
			Docker, OCI, CocoaPods, Swift, HelmOCI, Helm, Terraform, AIEditorExtensions,
		}, remoteOnly},
		{[]PackageType{Bower, Go, NPM}, virtualOnly},
	},
	"externalDependenciesRemoteRepo": {{[]PackageType{Bower, Go, NPM}, virtualOnly}},
	"virtualRetrievalCachePeriodSecs": {
		{[]PackageType{Chef, Conda, Cran, Conan, Debian, Helm, NPM, RPM}, virtualOnly},
	},

	"enableBowerSupport":     nil,
	"enableChefSupport":      nil,
	"enableCocoaPodsSupport": nil,
	"enableComposerSupport":  nil,
	"enableConanSupport":     nil,
	"enableDebianSupport":    nil,
	"enableDistRepoSupport":  nil,
	"enableDockerSupport":    nil,
	"enableGemsSupport":      nil,
	"enableGitLfsSupport":    nil,
	"enableNpmSupport":       nil,
	"enableNuGetSupport":     nil,
	"enablePuppetSupport":    nil,
	"enablePypiSupport":      nil,
	"enableVagrantSupport":   nil,
}

// configFields are the JSON names of Config's fields, by field index.
var configFields = func() []string {
	t := reflect.TypeFor[Config]()
	out := make([]string, t.NumField())
	for i := range out {
		out[i], _, _ = strings.Cut(t.Field(i).Tag.Get("json"), ",")
	}
	return out
}()

// Relevant returns c without the fields 7.x's v2 read leaves out for its
// package type and class (v2Fields), the shape GET
// /api/v2/repositories/{key} and each entry of GET
// /api/repositories/configurations return: a generic repository has no
// Maven or Docker fields, as JFrog's generic local samples of both show.
func Relevant(c Config) Config {
	v := reflect.ValueOf(&c).Elem()
	var dropped []string
	for i, name := range configFields {
		rules, ruled := v2Fields[name]
		if !ruled || slices.ContainsFunc(rules, func(r readRule) bool { return r.keeps(&c) }) {
			continue
		}
		v.Field(i).SetZero()
		dropped = append(dropped, name)
	}
	c.Shown = slices.DeleteFunc(slices.Clone(c.Shown), func(name string) bool {
		return slices.Contains(dropped, name)
	})
	return c
}
