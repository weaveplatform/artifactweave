// Package repo is the repository configuration model: the JSON document that
// PUT, POST and GET /artifactory/api/repositories/{key} exchange, with JFrog's
// field names. It is a wire model, so its JSON tags follow Artifactory's
// camelCase rather than the repository's snake_case rule; .golangci.yml excludes
// this package from tagliatelle for that reason. Field semantics, defaults and
// per-class rules are documented in docs/research/03-repository-configuration-model.md.
package repo

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Class is the repository class carried in the rclass field.
type Class string

// Repository classes. Listing endpoints echo them upper-case under "type".
const (
	Local     Class = "local"
	Remote    Class = "remote"
	Virtual   Class = "virtual"
	Federated Class = "federated"
)

// PackageType is the ecosystem a repository serves.
type PackageType string

// Package types, in the order JFrog's JSON documentation lists them. VCS and
// P2 exist only for remote repositories.
const (
	Alpine           PackageType = "alpine"
	Cargo            PackageType = "cargo"
	Composer         PackageType = "composer"
	Bower            PackageType = "bower"
	Chef             PackageType = "chef"
	CocoaPods        PackageType = "cocoapods"
	Conan            PackageType = "conan"
	Conda            PackageType = "conda"
	Cran             PackageType = "cran"
	Debian           PackageType = "debian"
	Docker           PackageType = "docker"
	Helm             PackageType = "helm"
	HelmOCI          PackageType = "helmoci"
	HuggingFaceML    PackageType = "huggingfaceml"
	Gems             PackageType = "gems"
	GitLFS           PackageType = "gitlfs"
	Go               PackageType = "go"
	Gradle           PackageType = "gradle"
	Ivy              PackageType = "ivy"
	Maven            PackageType = "maven"
	NPM              PackageType = "npm"
	NuGet            PackageType = "nuget"
	OCI              PackageType = "oci"
	Opkg             PackageType = "opkg"
	Pub              PackageType = "pub"
	Puppet           PackageType = "puppet"
	PyPI             PackageType = "pypi"
	RPM              PackageType = "rpm"
	SBT              PackageType = "sbt"
	Swift            PackageType = "swift"
	Terraform        PackageType = "terraform"
	TerraformBackend PackageType = "terraformbackend"
	Vagrant          PackageType = "vagrant"
	Ansible          PackageType = "ansible"
	BazelModules     PackageType = "bazelmodules"
	Generic          PackageType = "generic"
	VCS              PackageType = "vcs"
	P2               PackageType = "p2"
	// BuildInfo is the type of the built-in artifactory-build-info
	// repository. It is in no AllowedPackageTypes list, so the repositories
	// API cannot create another.
	BuildInfo PackageType = "buildinfo"
)

// ChecksumPolicy governs client-supplied checksums on local repositories.
type ChecksumPolicy string

// Checksum policies.
const (
	ClientChecksums          ChecksumPolicy = "client-checksums"
	ServerGeneratedChecksums ChecksumPolicy = "server-generated-checksums"
)

// RemoteChecksumPolicy governs checksums fetched from an upstream.
type RemoteChecksumPolicy string

// Remote checksum policies.
const (
	GenerateIfAbsent  RemoteChecksumPolicy = "generate-if-absent"
	Fail              RemoteChecksumPolicy = "fail"
	IgnoreAndGenerate RemoteChecksumPolicy = "ignore-and-generate"
	PassThru          RemoteChecksumPolicy = "pass-thru"
)

// SnapshotVersionBehavior decides how Maven snapshot file names are stored.
type SnapshotVersionBehavior string

// Snapshot version behaviours.
const (
	Unique    SnapshotVersionBehavior = "unique"
	NonUnique SnapshotVersionBehavior = "non-unique"
	Deployer  SnapshotVersionBehavior = "deployer"
)

// PomCleanupPolicy is the virtual repository POM repository-reference policy.
type PomCleanupPolicy string

// POM cleanup policies.
const (
	DiscardActiveReference PomCleanupPolicy = "discard_active_reference"
	DiscardAnyReference    PomCleanupPolicy = "discard_any_reference"
	Nothing                PomCleanupPolicy = "nothing"
)

// DockerAPIVersion is the Docker registry API version a repository speaks.
type DockerAPIVersion string

// Docker API versions. V1 is legacy and only accepted for compatibility.
const (
	DockerV1 DockerAPIVersion = "V1"
	DockerV2 DockerAPIVersion = "V2"
)

// TerraformType distinguishes module and provider registries.
type TerraformType string

// Terraform repository types.
const (
	TerraformModule   TerraformType = "MODULE"
	TerraformProvider TerraformType = "PROVIDER"
)

// UnmarshalJSON accepts any case because JFrog's own Terraform provider sends
// "module" and "provider" while the documentation and the server use upper case.
func (t *TerraformType) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("terraformType: %w", err)
	}
	*t = TerraformType(strings.ToUpper(s))
	return nil
}

// VCSGitProvider names the Git hosting flavour of a VCS remote.
type VCSGitProvider string

// VCS Git providers.
const (
	GitHub           VCSGitProvider = "GITHUB"
	GitHubEnterprise VCSGitProvider = "GITHUBENTERPRISE"
	Bitbucket        VCSGitProvider = "BITBUCKET"
	OldStash         VCSGitProvider = "OLDSTASH"
	Stash            VCSGitProvider = "STASH"
	ArtifactoryVCS   VCSGitProvider = "ARTIFACTORY"
	CustomVCS        VCSGitProvider = "CUSTOM"
)

// CustomHTTPHeader is one entry of a remote repository's customHttpHeaders.
type CustomHTTPHeader struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	Sensitive bool   `json:"sensitive"`
}

// ContentSynchronisation configures a smart remote repository.
type ContentSynchronisation struct {
	Enabled    bool               `json:"enabled"`
	Statistics *EnabledFlag       `json:"statistics,omitempty"`
	Properties *EnabledFlag       `json:"properties,omitempty"`
	Source     *ContentSyncSource `json:"source,omitempty"`
}

// EnabledFlag is a nested {"enabled": bool} object.
type EnabledFlag struct {
	Enabled bool `json:"enabled"`
}

// ContentSyncSource is the source section of contentSynchronisation.
type ContentSyncSource struct {
	OriginAbsenceDetection bool `json:"originAbsenceDetection"`
}

// Member is one federated repository member. Artifactory types enabled as a
// JSON string, which FlexBool renders when the member is marshalled; nil means
// the caller did not say, and Defaults turns that into "true".
type Member struct {
	URL     string    `json:"url"`
	Enabled *FlexBool `json:"enabled,omitempty"`
}

// Config is the repository configuration document. Optional scalars are
// pointers so that "not supplied" is distinguishable from the zero value: an
// update is partial, and Defaults fills only what the caller left nil.
type Config struct {
	// Common
	Key                 string      `json:"key"`
	Class               Class       `json:"rclass"`
	PackageType         PackageType `json:"packageType,omitempty"`
	ProjectKey          string      `json:"projectKey,omitempty"`
	Environments        []string    `json:"environments,omitempty"`
	Description         string      `json:"description,omitempty"`
	Notes               string      `json:"notes,omitempty"`
	IncludesPattern     *string     `json:"includesPattern,omitempty"`
	ExcludesPattern     *string     `json:"excludesPattern,omitempty"`
	RepoLayoutRef       string      `json:"repoLayoutRef,omitempty"`
	BlackedOut          *bool       `json:"blackedOut,omitempty"`
	XrayIndex           *bool       `json:"xrayIndex,omitempty"`
	PropertySets        []string    `json:"propertySets,omitempty"`
	PriorityResolution  *FlexBool   `json:"priorityResolution,omitempty"`
	DownloadRedirect    *FlexBool   `json:"downloadRedirect,omitempty"`
	CDNRedirect         *FlexBool   `json:"cdnRedirect,omitempty"`
	PrimaryKeyPairRef   string      `json:"primaryKeyPairRef,omitempty"`
	SecondaryKeyPairRef string      `json:"secondaryKeyPairRef,omitempty"`
	SignedURLTTL        *int        `json:"signedUrlTtl,omitempty"`
	XrayDataTTL         *int        `json:"xrayDataTtl,omitempty"`

	// Local and federated
	ChecksumPolicyType              ChecksumPolicy          `json:"checksumPolicyType,omitempty"`
	HandleReleases                  *bool                   `json:"handleReleases,omitempty"`
	HandleSnapshots                 *bool                   `json:"handleSnapshots,omitempty"`
	MaxUniqueSnapshots              *int                    `json:"maxUniqueSnapshots,omitempty"`
	SnapshotVersionBehavior         SnapshotVersionBehavior `json:"snapshotVersionBehavior,omitempty"`
	SuppressPomConsistencyChecks    *bool                   `json:"suppressPomConsistencyChecks,omitempty"`
	MaxUniqueTags                   *int                    `json:"maxUniqueTags,omitempty"`
	DockerTagRetention              *int                    `json:"dockerTagRetention,omitempty"`
	DockerAPIVersion                DockerAPIVersion        `json:"dockerApiVersion,omitempty"`
	BlockPushingSchema1             *FlexBool               `json:"blockPushingSchema1,omitempty"`
	ArchiveBrowsingEnabled          *bool                   `json:"archiveBrowsingEnabled,omitempty"`
	EnableFileListsIndexing         *FlexBool               `json:"enableFileListsIndexing,omitempty"`
	OptionalIndexCompressionFormats []string                `json:"optionalIndexCompressionFormats,omitempty"`
	DebianTrivialLayout             *bool                   `json:"debianTrivialLayout,omitempty"`
	CalculateYumMetadata            *bool                   `json:"calculateYumMetadata,omitempty"`
	YumRootDepth                    *int                    `json:"yumRootDepth,omitempty"`
	TerraformType                   TerraformType           `json:"terraformType,omitempty"`
	EncryptStates                   *bool                   `json:"encryptStates,omitempty"`
	ForceNugetAuthentication        *bool                   `json:"forceNugetAuthentication,omitempty"`
	ForceP2Authentication           *bool                   `json:"forceP2Authentication,omitempty"`
	ForceConanAuthentication        *bool                   `json:"forceConanAuthentication,omitempty"`
	ForceMetadataNameVersion        *bool                   `json:"forceMetadataNameVersion,omitempty"`
	ForceNonDuplicateChart          *bool                   `json:"forceNonDuplicateChart,omitempty"`
	Members                         []Member                `json:"members,omitempty"`

	// Remote
	URL                               string                  `json:"url,omitempty"`
	Username                          string                  `json:"username,omitempty"`
	Password                          string                  `json:"password,omitempty"`
	Proxy                             string                  `json:"proxy,omitempty"`
	DisableProxy                      *bool                   `json:"disableProxy,omitempty"`
	RemoteRepoLayoutRef               string                  `json:"remoteRepoLayoutRef,omitempty"`
	RemoteRepoChecksumPolicyType      RemoteChecksumPolicy    `json:"remoteRepoChecksumPolicyType,omitempty"`
	HardFail                          *bool                   `json:"hardFail,omitempty"`
	Offline                           *bool                   `json:"offline,omitempty"`
	StoreArtifactsLocally             *bool                   `json:"storeArtifactsLocally,omitempty"`
	SocketTimeoutMillis               *int                    `json:"socketTimeoutMillis,omitempty"`
	LocalAddress                      string                  `json:"localAddress,omitempty"`
	RetrievalCachePeriodSecs          *int                    `json:"retrievalCachePeriodSecs,omitempty"`
	MissedRetrievalCachePeriodSecs    *int                    `json:"missedRetrievalCachePeriodSecs,omitempty"`
	MetadataRetrievalTimeoutSecs      *int                    `json:"metadataRetrievalTimeoutSecs,omitempty"`
	AssumedOfflinePeriodSecs          *int                    `json:"assumedOfflinePeriodSecs,omitempty"`
	UnusedArtifactsCleanupPeriodHours *int                    `json:"unusedArtifactsCleanupPeriodHours,omitempty"`
	FetchJarsEagerly                  *bool                   `json:"fetchJarsEagerly,omitempty"`
	FetchSourcesEagerly               *bool                   `json:"fetchSourcesEagerly,omitempty"`
	RejectInvalidJars                 *bool                   `json:"rejectInvalidJars,omitempty"`
	ShareConfiguration                *bool                   `json:"shareConfiguration,omitempty"`
	SynchronizeProperties             *bool                   `json:"synchronizeProperties,omitempty"`
	BlockMismatchingMimeTypes         *bool                   `json:"blockMismatchingMimeTypes,omitempty"`
	MismatchingMimeTypesOverrideList  string                  `json:"mismatchingMimeTypesOverrideList,omitempty"`
	AllowAnyHostAuth                  *bool                   `json:"allowAnyHostAuth,omitempty"`
	EnableCookieManagement            *bool                   `json:"enableCookieManagement,omitempty"`
	EnableTokenAuthentication         *bool                   `json:"enableTokenAuthentication,omitempty"`
	BypassHeadRequests                *bool                   `json:"bypassHeadRequests,omitempty"`
	ClientTLSCertificate              string                  `json:"clientTlsCertificate,omitempty"`
	ListRemoteFolderItems             *bool                   `json:"listRemoteFolderItems,omitempty"`
	QueryParams                       string                  `json:"queryParams,omitempty"`
	DisableURLNormalization           *bool                   `json:"disableUrlNormalization,omitempty"`
	CustomHTTPHeaders                 []CustomHTTPHeader      `json:"customHttpHeaders,omitempty"`
	ContentSynchronisation            *ContentSynchronisation `json:"contentSynchronisation,omitempty"`
	PyPIRegistryURL                   string                  `json:"pyPIRegistryUrl,omitempty"`
	PyPIRepositorySuffix              string                  `json:"pyPIRepositorySuffix,omitempty"`
	FeedContextPath                   string                  `json:"feedContextPath,omitempty"`
	DownloadContextPath               string                  `json:"downloadContextPath,omitempty"`
	V3FeedURL                         string                  `json:"v3FeedUrl,omitempty"`
	SymbolServerURL                   string                  `json:"symbolServerUrl,omitempty"`
	ChartsBaseURL                     string                  `json:"chartsBaseUrl,omitempty"`
	TerraformRegistryURL              string                  `json:"terraformRegistryUrl,omitempty"`
	TerraformProvidersURL             string                  `json:"terraformProvidersUrl,omitempty"`
	VCSType                           string                  `json:"vcsType,omitempty"`
	VCSGitProvider                    VCSGitProvider          `json:"vcsGitProvider,omitempty"`
	VCSGitDownloadURL                 string                  `json:"vcsGitDownloadUrl,omitempty"`
	DockerProjectID                   string                  `json:"dockerProjectId,omitempty"`
	BowerRegistryURL                  string                  `json:"bowerRegistryUrl,omitempty"`
	GitRegistryURL                    string                  `json:"gitRegistryUrl,omitempty"`
	ComposerRegistryURL               string                  `json:"composerRegistryUrl,omitempty"`

	// Shared by remote and virtual
	ExternalDependenciesEnabled    *bool    `json:"externalDependenciesEnabled,omitempty"`
	ExternalDependenciesPatterns   []string `json:"externalDependenciesPatterns,omitempty"`
	ExternalDependenciesRemoteRepo string   `json:"externalDependenciesRemoteRepo,omitempty"`

	// Virtual
	Repositories                                  []string         `json:"repositories,omitempty"`
	DefaultDeploymentRepo                         string           `json:"defaultDeploymentRepo,omitempty"`
	ArtifactoryRequestsCanRetrieveRemoteArtifacts *bool            `json:"artifactoryRequestsCanRetrieveRemoteArtifacts,omitempty"`
	VirtualRetrievalCachePeriodSecs               *int             `json:"virtualRetrievalCachePeriodSecs,omitempty"`
	KeyPair                                       string           `json:"keyPair,omitempty"`
	PomRepositoryReferencesCleanupPolicy          PomCleanupPolicy `json:"pomRepositoryReferencesCleanupPolicy,omitempty"`
	ForceMavenAuthentication                      *bool            `json:"forceMavenAuthentication,omitempty"`
	HideUnauthorizedResources                     *bool            `json:"hideUnauthorizedResources,omitempty"`
	ResolveDockerTagsByTimestamp                  *bool            `json:"resolveDockerTagsByTimestamp,omitempty"`
	UseNamespaces                                 *bool            `json:"useNamespaces,omitempty"`
	DebianDefaultArchitectures                    string           `json:"debianDefaultArchitectures,omitempty"`
}

// flexBoolFields lists the fields whose JSON type depends on the class; see
// FlexBool. Members' enabled flag is handled separately because it is always a
// string.
func (c *Config) flexBoolFields() []**FlexBool {
	return []**FlexBool{
		&c.PriorityResolution, &c.DownloadRedirect, &c.CDNRedirect,
		&c.BlockPushingSchema1, &c.EnableFileListsIndexing,
	}
}

// UnmarshalJSON decodes a repository document. FlexBool already accepts both
// spellings, so this only exists because the generated API server wraps
// Config in response types that delegate both directions to it; without the
// method the wrapper does not compile.
func (c *Config) UnmarshalJSON(b []byte) error {
	type plain Config
	if err := json.Unmarshal(b, (*plain)(c)); err != nil {
		return fmt.Errorf("unmarshal repository: %w", err)
	}
	return nil
}

// MarshalJSON renders the class-dependent boolean fields in the spelling
// Artifactory uses for that class. The receiver is copied so the caller's
// values are not mutated; the copy is shallow, which is safe because only the
// FlexBool pointers are replaced.
func (c Config) MarshalJSON() ([]byte, error) {
	asString := c.Class == Local || c.Class == Federated
	for _, f := range c.flexBoolFields() {
		if *f != nil {
			v := **f
			v.asString = asString
			*f = &v
		}
	}
	// The federated schema is the one exception in the oracle: it types
	// priorityResolution as a boolean while keeping the other four as strings
	// (research 15, section 5).
	if c.Class == Federated && c.PriorityResolution != nil {
		v := *c.PriorityResolution
		v.asString = false
		c.PriorityResolution = &v
	}
	if len(c.Members) > 0 {
		members := make([]Member, len(c.Members))
		for i, m := range c.Members {
			if m.Enabled != nil {
				v := *m.Enabled
				v.asString = true
				m.Enabled = &v
			}
			members[i] = m
		}
		c.Members = members
	}
	type plain Config
	b, err := json.Marshal(plain(c))
	if err != nil {
		return nil, fmt.Errorf("marshal repository %q: %w", c.Key, err)
	}
	return b, nil
}
