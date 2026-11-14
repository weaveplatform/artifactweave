package repo

import (
	"net/url"
	"slices"
)

// HTTPRepoDescriptorClass is the "@class" of the shared remote
// configuration: 7.161.26's RepoBaseDescriptor carries
// @JsonTypeInfo(use = CLASS), and RepoConfigurationResource.getRemoteDescriptor
// answers with an HttpRepoDescriptor, whose comment says this "@class" is
// what the deprecated endpoint returns.
const HTTPRepoDescriptorClass = "org.artifactory.descriptor.repo.HttpRepoDescriptor"

// RemoteDescriptor is the shared remote configuration,
// GET /api/repositories/{key}/configuration: 7.161.26's HttpRepoDescriptor
// as RepoConfigurationResource.getRemoteDescriptor builds it from the stored
// remote (HttpRepoDescriptor(RemoteRepoConfig, ...)), masks it (maskData
// clears the username, password, proxy, local address and property sets)
// and writes it with the REST JsonProvider's mapper.
//
// That mapper reads the descriptors' JAXB annotations after Jackson's own
// (JakartaXmlBindAnnotationModule, priority SECONDARY) and leaves nulls out
// (NON_NULL). The package's @XmlAccessorType(FIELD) makes it write every
// field but the @XmlTransient ones (enableNormalizedVersion,
// forceP2Authentication, dockerProjectId, sendContext, passThrough, curated,
// cocoaPodsCdnUrl) and no getter, so queryParams is the raw field. A field's
// @XmlElement name renames it (xray, downloadRedirect, cdnRedirect,
// remoteRepoChecksumPolicyType, customHttpHeader), an @XmlIDREF writes the
// referenced object's id (repoLayoutRef, remoteRepoLayoutRef and the key
// pair refs), and an enumeration writes its @JsonProperty name (the
// RepoType names PackageTypeName gives, GEN_IF_ABSENT, GIT, GITHUB).
//
// The fields follow the mapper's order as read from Jackson's property
// collector [I]: "@class", then key (the @XmlID, which Jackson's object
// identity puts first), HttpRepoDescriptor's propOrder, the fields without
// an @XmlElement name superclass first, then the renamed ones. JSON objects
// are unordered, so no client depends on it.
//
// What artifactweave does not hold is left out, as 7.x leaves out a null:
// gridTopologyId, p2OriginalUrl, the PyPI context paths, the Ansible
// authUrl. A string artifactweave holds empty is left out too, though 7.x
// may hold "" for some [I].
type RemoteDescriptor struct {
	Class string `json:"@class"`
	Key   string `json:"key"`

	AllowAnyHostAuth          bool   `json:"allowAnyHostAuth"`
	SocketTimeoutMillis       int    `json:"socketTimeoutMillis"`
	EnableCookieManagement    bool   `json:"enableCookieManagement"`
	EnableTokenAuthentication bool   `json:"enableTokenAuthentication"`
	DisableProxy              bool   `json:"disableProxy"`
	QueryParams               string `json:"queryParams,omitempty"`
	PropagateQueryParams      bool   `json:"propagateQueryParams"`
	ClientTLSCertificate      string `json:"clientTlsCertificate,omitempty"`
	DisableURLNormalization   bool   `json:"disableUrlNormalization"`
	RetrieveSha256FromServer  bool   `json:"retrieveSha256FromServer"`
	// customHttpHeaders' @XmlElement name, not its @XmlElementWrapper's:
	// the mapper leaves USE_WRAPPER_NAME_AS_PROPERTY_NAME off [I].
	CustomHTTPHeader []CustomHTTPHeader `json:"customHttpHeader"`

	Type                         string           `json:"type"`
	Description                  string           `json:"description,omitempty"`
	Notes                        string           `json:"notes,omitempty"`
	IncludesPattern              string           `json:"includesPattern"`
	ExcludesPattern              string           `json:"excludesPattern,omitempty"`
	DockerAPIVersion             DockerAPIVersion `json:"dockerApiVersion"`
	ForceNugetAuthentication     bool             `json:"forceNugetAuthentication"`
	ForceConanAuthentication     bool             `json:"forceConanAuthentication"`
	DdebSupported                bool             `json:"ddebSupported"`
	SignedURLTTL                 int              `json:"signedUrlTtl"`
	BlackedOut                   bool             `json:"blackedOut"`
	HandleReleases               bool             `json:"handleReleases"`
	HandleSnapshots              bool             `json:"handleSnapshots"`
	MaxUniqueSnapshots           int              `json:"maxUniqueSnapshots"`
	MaxUniqueTags                int              `json:"maxUniqueTags"`
	BlockPushingSchema1          bool             `json:"blockPushingSchema1"`
	SuppressPomConsistencyChecks bool             `json:"suppressPomConsistencyChecks"`
	ArchiveBrowsingEnabled       bool             `json:"archiveBrowsingEnabled"`
	PriorityResolution           bool             `json:"priorityResolution"`

	StoreArtifactsLocally             bool                  `json:"storeArtifactsLocally"`
	FetchJarsEagerly                  bool                  `json:"fetchJarsEagerly"`
	FetchSourcesEagerly               bool                  `json:"fetchSourcesEagerly"`
	ExternalDependencies              *DescriptorExternal   `json:"externalDependencies,omitempty"`
	ShareConfiguration                bool                  `json:"shareConfiguration"`
	URL                               string                `json:"url,omitempty"`
	HardFail                          bool                  `json:"hardFail"`
	Offline                           bool                  `json:"offline"`
	RetrievalCachePeriodSecs          int                   `json:"retrievalCachePeriodSecs"`
	MetadataRetrievalTimeoutSecs      int                   `json:"metadataRetrievalTimeoutSecs"`
	AssumedOfflinePeriodSecs          int                   `json:"assumedOfflinePeriodSecs"`
	MissedRetrievalCachePeriodSecs    int                   `json:"missedRetrievalCachePeriodSecs"`
	UnusedArtifactsCleanupPeriodHours int                   `json:"unusedArtifactsCleanupPeriodHours"`
	SynchronizeProperties             bool                  `json:"synchronizeProperties"`
	ListRemoteFolderItems             bool                  `json:"listRemoteFolderItems"`
	BypassHeadRequests                bool                  `json:"bypassHeadRequests"`
	RejectInvalidJars                 bool                  `json:"rejectInvalidJars"`
	NuGet                             *DescriptorNuGet      `json:"nuget,omitempty"`
	PyPI                              *DescriptorPyPI       `json:"pypi,omitempty"`
	VCS                               *DescriptorVCS        `json:"vcs,omitempty"`
	Bower                             *DescriptorRegistry   `json:"bower,omitempty"`
	Composer                          *DescriptorRegistry   `json:"composer,omitempty"`
	CocoaPods                         *DescriptorCocoaPods  `json:"cocoaPods,omitempty"`
	Helm                              *DescriptorRegistry   `json:"helm,omitempty"`
	Git                               *DescriptorRegistry   `json:"git,omitempty"`
	Terraform                         *DescriptorRegistry   `json:"terraform,omitempty"`
	Hex                               *DescriptorRegistry   `json:"hex,omitempty"`
	Go                                *DescriptorGo         `json:"go,omitempty"`
	HuggingFace                       *DescriptorHF         `json:"huggingFace,omitempty"`
	Ansible                           *DescriptorRegistry   `json:"ansible,omitempty"`
	BlockMismatchingMimeTypes         bool                  `json:"blockMismatchingMimeTypes"`
	MismatchingMimeTypesOverrideList  string                `json:"mismatchingMimeTypesOverrideList,omitempty"`
	RepoLayoutRef                     string                `json:"repoLayoutRef,omitempty"`
	CargoConfig                       *DescriptorCargo      `json:"cargoConfig,omitempty"`
	PrimaryKeyPairRef                 string                `json:"primaryKeyPairRef,omitempty"`
	SecondaryKeyPairRef               string                `json:"secondaryKeyPairRef,omitempty"`
	Xray                              DescriptorXray        `json:"xray"`
	DownloadRedirect                  EnabledFlag           `json:"downloadRedirect"`
	CDNRedirect                       EnabledFlag           `json:"cdnRedirect"`
	RemoteRepoChecksumPolicyType      string                `json:"remoteRepoChecksumPolicyType"`
	RemoteRepoLayoutRef               string                `json:"remoteRepoLayoutRef,omitempty"`
	ContentSynchronisation            DescriptorContentSync `json:"contentSynchronisation"`
}

// DescriptorExternal is ExternalDependenciesConfig. Its patterns take the
// @XmlElement name "pattern", as customHttpHeader does [I]; remoteRepo is
// never set on a remote's descriptor, so it is left out.
type DescriptorExternal struct {
	Enabled  bool     `json:"enabled"`
	Patterns []string `json:"pattern,omitempty"`
}

// DescriptorNuGet is NuGetConfiguration, whose four paths default to "".
type DescriptorNuGet struct {
	FeedContextPath     string `json:"feedContextPath"`
	DownloadContextPath string `json:"downloadContextPath"`
	V3FeedURL           string `json:"v3FeedUrl"`
	SymbolServerURL     string `json:"symbolServerUrl"`
}

// DescriptorPyPI is PypiConfiguration without the context paths
// artifactweave does not hold.
type DescriptorPyPI struct {
	RegistryURL      string `json:"pyPIRegistryUrl"`
	RepositorySuffix string `json:"repositorySuffix"`
}

// DescriptorVCS is VcsConfiguration.
type DescriptorVCS struct {
	Type string           `json:"type"`
	Git  DescriptorVCSGit `json:"git"`
}

// DescriptorVCSGit is VcsGitConfiguration: CocoaPods' specRepoProvider
// has the same shape.
type DescriptorVCSGit struct {
	Provider    VCSGitProvider `json:"provider"`
	DownloadURL string         `json:"downloadUrl,omitempty"`
}

// DescriptorRegistry is each of the per-type objects that hold only URLs or
// a key: BowerConfiguration, ComposerConfiguration, HelmConfiguration,
// GitConfiguration, TerraformConfiguration, HexConfiguration and
// AnsibleRemoteConfiguration. Each sets only the fields of its own type,
// and 7.x's leaves a null one out, so the rest stay empty and unwritten.
type DescriptorRegistry struct {
	BowerRegistryURL      string `json:"bowerRegistryUrl,omitempty"`
	ComposerRegistryURL   string `json:"composerRegistryUrl,omitempty"`
	ChartsBaseURL         string `json:"chartsBaseUrl,omitempty"`
	RegistryURL           string `json:"registryUrl,omitempty"`
	TerraformRegistryURL  string `json:"terraformRegistryUrl,omitempty"`
	TerraformProvidersURL string `json:"terraformProvidersUrl,omitempty"`
	HexRepositoryURL      string `json:"hexRepositoryUrl,omitempty"`
	HexPublicKey          string `json:"hexPublicKey,omitempty"`
}

// DescriptorCocoaPods is CocoaPodsConfiguration: its specRepoProvider is
// renamed, so it follows cocoaPodsForceModifyIndex.
type DescriptorCocoaPods struct {
	SpecsRepoURL     string           `json:"cocoaPodsSpecsRepoUrl,omitempty"`
	ForceModifyIndex bool             `json:"cocoaPodsForceModifyIndex"`
	SpecRepoProvider DescriptorVCSGit `json:"specRepoProvider"`
}

// DescriptorGo is GoConfiguration.
type DescriptorGo struct {
	GitLabResolveSubgroups bool `json:"gitLabResolveSubgroups"`
}

// DescriptorHF is HuggingFaceMLRemoteConfiguration.
type DescriptorHF struct {
	MLRepoLayout bool `json:"mlRepoLayout"`
}

// DescriptorCargo is CargoConfiguration.
type DescriptorCargo struct {
	AnonymousAccess bool `json:"cargoAnonymousAccess"`
	InternalIndex   bool `json:"cargoInternalIndex"`
}

// DescriptorXray is XrayRepoConfigDescriptor. Its xrayNotAvailableMode is
// @XmlElement(nillable = true), which the mapper writes even when null; no
// remote here has one [I].
type DescriptorXray struct {
	Enabled              bool    `json:"enabled"`
	DataTTL              int     `json:"dataTtl"`
	XrayNotAvailableMode *string `json:"xrayNotAvailableMode"`
}

// DescriptorContentSync is ContentSynchronisationDescriptor, whose three
// nested objects are never null.
type DescriptorContentSync struct {
	Enabled    bool              `json:"enabled"`
	Statistics EnabledFlag       `json:"statistics"`
	Properties EnabledFlag       `json:"properties"`
	Source     ContentSyncSource `json:"source"`
}

// Descriptor returns the shared remote configuration of the remote
// repository c. Each value is what HttpRepoDescriptor's constructor sets
// from the stored configuration, or the descriptor's field default where
// artifactweave holds none; the per-type settings follow its
// setPackageTypeConfig:
//
//   - the Maven group (Maven, Gradle, Ivy, SBT, P2) takes
//     suppressPomConsistencyChecks, fetchJarsEagerly, fetchSourcesEagerly
//     and rejectInvalidJars from its configuration, and every other type
//     has true, false, false and false;
//   - Docker, OCI and Helm OCI take dockerApiVersion, blockPushingSchema1,
//     maxUniqueTags and externalDependencies, and every other type has V2,
//     true and 0. 7.161.26's switch names OCI alone, but its reverse
//     mapping (buildPackageTypeConfig) and its defaults
//     (RemoteDefaultPackageConfigSetter) treat the three alike
//     (isDockerGroup), so Docker and Helm OCI losing their settings is not
//     what it intends;
//   - NuGet, Conan and Debian take forceNugetAuthentication,
//     forceConanAuthentication and ddebSupported, false for the others;
//   - externalDependencies, for Go, Helm, the Docker group, CocoaPods,
//     Swift, AI editor extensions and Terraform, appears when the
//     repository holds externalDependenciesEnabled, as 7.x's appears when
//     the configuration has the key;
//   - vcs appears for VCS always, and for Go, Bower, CocoaPods, Composer and
//     Terraform when vcsType is set;
//   - each type's own object (nuget, pypi, bower, composer, cocoaPods,
//     helm, terraform, hex, go, huggingFace, ansible, cargoConfig and, for
//     Cargo with a gitRegistryUrl, git) appears for that type only.
//
// The xray, downloadRedirect, cdnRedirect and contentSynchronisation
// objects appear for every remote: 7.x builds each when the stored
// configuration has it, which a remote created through the REST API is
// assumed to have [I]. A sensitive header's value is "" as Masked gives
// it: maskData leaves header values alone, but a sensitive header is one
// whose value 7.x keeps from readers, and this answer goes to every
// signed-in user [I]. A user name and password written into the URL are
// left out, as 7.x moves them into the username and password on save
// (HttpRepoDescriptor.processUrl) and maskData then clears them.
func Descriptor(c Config) RemoteDescriptor {
	c = Masked(c)
	t := c.PackageType
	docker, maven := isDocker(t), slices.Contains([]PackageType{Maven, Gradle, Ivy, SBT, P2}, t)
	d := RemoteDescriptor{
		Class: HTTPRepoDescriptorClass,
		Key:   c.Key,

		AllowAnyHostAuth:          boolOr(c.AllowAnyHostAuth, false),
		SocketTimeoutMillis:       intOr(c.SocketTimeoutMillis, DefaultSocketTimeoutMillis),
		EnableCookieManagement:    boolOr(c.EnableCookieManagement, false),
		EnableTokenAuthentication: boolOr(c.EnableTokenAuthentication, false),
		DisableProxy:              boolOr(c.DisableProxy, false),
		QueryParams:               c.QueryParams,
		PropagateQueryParams:      boolOr(c.PropagateQueryParams, false),
		ClientTLSCertificate:      c.ClientTLSCertificate,
		DisableURLNormalization:   boolOr(c.DisableURLNormalization, false),
		RetrieveSha256FromServer:  boolOr(c.RetrieveSha256FromServer, false),
		CustomHTTPHeader:          c.CustomHTTPHeaders,

		Type:                         PackageTypeName(string(t)),
		Description:                  c.Description,
		Notes:                        c.Notes,
		IncludesPattern:              DefaultIncludesPattern,
		DockerAPIVersion:             DockerV2,
		ForceNugetAuthentication:     t == NuGet && boolOr(c.ForceNugetAuthentication, false),
		ForceConanAuthentication:     t == Conan && boolOr(c.ForceConanAuthentication, false),
		DdebSupported:                t == Debian && boolOr(c.DdebSupported, false),
		SignedURLTTL:                 intOr(c.SignedURLTTL, DefaultSignedURLTTL),
		BlackedOut:                   boolOr(c.BlackedOut, false),
		HandleReleases:               boolOr(c.HandleReleases, true),
		HandleSnapshots:              boolOr(c.HandleSnapshots, true),
		MaxUniqueSnapshots:           intOr(c.MaxUniqueSnapshots, 0),
		BlockPushingSchema1:          true,
		SuppressPomConsistencyChecks: true,
		ArchiveBrowsingEnabled:       boolOr(c.ArchiveBrowsingEnabled, false),
		PriorityResolution:           c.PriorityResolution.Bool(),

		StoreArtifactsLocally: boolOr(c.StoreArtifactsLocally, true),
		ShareConfiguration:    boolOr(c.ShareConfiguration, false),
		URL:                   WithoutCredentials(c.URL),
		HardFail:              boolOr(c.HardFail, false),
		Offline:               boolOr(c.Offline, false),
		RetrievalCachePeriodSecs: intOr(
			c.RetrievalCachePeriodSecs,
			DefaultRetrievalCachePeriodSecs,
		),
		MetadataRetrievalTimeoutSecs: intOr(
			c.MetadataRetrievalTimeoutSecs,
			DefaultMetadataRetrievalTimeoutSecs,
		),
		AssumedOfflinePeriodSecs: intOr(
			c.AssumedOfflinePeriodSecs,
			DefaultAssumedOfflinePeriodSecs,
		),
		MissedRetrievalCachePeriodSecs: intOr(
			c.MissedRetrievalCachePeriodSecs,
			DefaultMissedRetrievalCachePeriodSecs,
		),
		UnusedArtifactsCleanupPeriodHours: intOr(c.UnusedArtifactsCleanupPeriodHours, 0),
		SynchronizeProperties:             boolOr(c.SynchronizeProperties, false),
		ListRemoteFolderItems:             c.ListRemoteFolderItems.Bool(),
		BypassHeadRequests:                boolOr(c.BypassHeadRequests, false),
		BlockMismatchingMimeTypes:         boolOr(c.BlockMismatchingMimeTypes, true),
		MismatchingMimeTypesOverrideList:  c.MismatchingMimeTypesOverrideList,
		RepoLayoutRef:                     c.RepoLayoutRef,
		PrimaryKeyPairRef:                 c.PrimaryKeyPairRef,
		SecondaryKeyPairRef:               c.SecondaryKeyPairRef,
		Xray: DescriptorXray{
			Enabled: boolOr(c.XrayIndex, false),
			DataTTL: intOr(c.XrayDataTTL, DefaultXrayDataTTL),
		},
		DownloadRedirect:             EnabledFlag{Enabled: c.DownloadRedirect.Bool()},
		CDNRedirect:                  EnabledFlag{Enabled: c.CDNRedirect.Bool()},
		RemoteRepoChecksumPolicyType: checksumPolicyName(c.RemoteRepoChecksumPolicyType),
		RemoteRepoLayoutRef:          c.RemoteRepoLayoutRef,
		ContentSynchronisation:       contentSync(c.ContentSynchronisation),
	}
	if d.CustomHTTPHeader == nil {
		d.CustomHTTPHeader = []CustomHTTPHeader{}
	}
	if c.IncludesPattern != nil {
		d.IncludesPattern = *c.IncludesPattern
	}
	if c.ExcludesPattern != nil {
		d.ExcludesPattern = *c.ExcludesPattern
	}
	if maven {
		d.SuppressPomConsistencyChecks = boolOr(
			c.SuppressPomConsistencyChecks,
			suppressPomChecks(t),
		)
		d.FetchJarsEagerly = boolOr(c.FetchJarsEagerly, false)
		d.FetchSourcesEagerly = boolOr(c.FetchSourcesEagerly, false)
		d.RejectInvalidJars = boolOr(c.RejectInvalidJars, false)
	}
	if docker {
		if c.DockerAPIVersion != "" {
			d.DockerAPIVersion = c.DockerAPIVersion
		}
		d.BlockPushingSchema1 = c.BlockPushingSchema1 == nil || c.BlockPushingSchema1.Value
		d.MaxUniqueTags = intOr(c.MaxUniqueTags, 0)
	}
	descriptorTypeObjects(&d, c)
	return d
}

// descriptorTypeObjects sets the per-type objects of d, the descriptor of
// c (Descriptor).
func descriptorTypeObjects(d *RemoteDescriptor, c Config) {
	t := c.PackageType
	if c.ExternalDependenciesEnabled != nil && (isDocker(t) || slices.Contains(
		[]PackageType{Go, Helm, CocoaPods, Swift, AIEditorExtensions, Terraform}, t)) {
		d.ExternalDependencies = &DescriptorExternal{
			Enabled: *c.ExternalDependenciesEnabled, Patterns: c.ExternalDependenciesPatterns,
		}
	}
	if t == VCS ||
		(c.VCSType != "" && slices.Contains([]PackageType{Go, Bower, CocoaPods, Composer, Terraform}, t)) {
		d.VCS = &DescriptorVCS{Type: DefaultVCSType, Git: DescriptorVCSGit{
			Provider: GitHub, DownloadURL: c.VCSGitDownloadURL,
		}}
		if c.VCSType != "" {
			d.VCS.Type = c.VCSType
		}
		if c.VCSGitProvider != "" {
			d.VCS.Git.Provider = c.VCSGitProvider
		}
	}
	switch t {
	case NuGet:
		d.NuGet = &DescriptorNuGet{V3FeedURL: c.V3FeedURL, SymbolServerURL: c.SymbolServerURL}
		if c.FeedContextPath != nil {
			d.NuGet.FeedContextPath = *c.FeedContextPath
		}
		if c.DownloadContextPath != nil {
			d.NuGet.DownloadContextPath = *c.DownloadContextPath
		}
	case PyPI:
		d.PyPI = &DescriptorPyPI{
			RegistryURL:      stringOr(c.PyPIRegistryURL, DefaultPyPIRegistryURL),
			RepositorySuffix: stringOr(c.PyPIRepositorySuffix, DefaultPyPIRepositorySuffix),
		}
	case Bower:
		d.Bower = &DescriptorRegistry{BowerRegistryURL: c.BowerRegistryURL}
	case Composer:
		d.Composer = &DescriptorRegistry{ComposerRegistryURL: c.ComposerRegistryURL}
	case CocoaPods:
		// The specs repository's download provider is one artifactweave
		// does not hold, so it keeps the descriptor's default, GitHub.
		d.CocoaPods = &DescriptorCocoaPods{
			SpecsRepoURL: c.PodsSpecsRepoURL, SpecRepoProvider: DescriptorVCSGit{Provider: GitHub},
		}
	case Helm:
		d.Helm = &DescriptorRegistry{ChartsBaseURL: c.ChartsBaseURL}
	case Terraform:
		d.Terraform = &DescriptorRegistry{
			TerraformRegistryURL:  stringOr(c.TerraformRegistryURL, DefaultTerraformRegistryURL),
			TerraformProvidersURL: stringOr(c.TerraformProvidersURL, DefaultTerraformProvidersURL),
		}
	case Hex:
		// HexConfiguration's hexRepositoryUrl keeps its default, which the
		// REST configuration has no field to change [I].
		d.Hex = &DescriptorRegistry{
			HexRepositoryURL: DefaultHexRepositoryURL,
			HexPublicKey:     c.HexPublicKey,
		}
	case Go:
		d.Go = &DescriptorGo{}
	case HuggingFaceML:
		d.HuggingFace = &DescriptorHF{MLRepoLayout: boolOr(c.MLRepoLayout, false)}
	case Ansible:
		d.Ansible = &DescriptorRegistry{}
	case Cargo:
		// cargoInternalIndex is true when unset, as the read model has it;
		// 7.x's descriptor parses the stored string, which a REST create is
		// assumed to set [I].
		d.CargoConfig = &DescriptorCargo{
			AnonymousAccess: boolOr(c.CargoAnonymousAccess, false),
			InternalIndex:   boolOr(c.CargoInternalIndex, true),
		}
		if c.GitRegistryURL != "" {
			d.Git = &DescriptorRegistry{RegistryURL: c.GitRegistryURL}
		}
	}
}

// DefaultHexRepositoryURL is HexConfiguration's hexRepositoryUrl default
// (PackageTypeConfigFields.HEX_REGISTRY_URL).
const DefaultHexRepositoryURL = "https://repo.hex.pm"

// checksumPolicyName is p as ChecksumPolicyTypeDescriptor's @JsonProperty
// spells it, GEN_IF_ABSENT when unset.
func checksumPolicyName(p RemoteChecksumPolicy) string {
	switch p {
	case Fail:
		return "FAIL"
	case IgnoreAndGenerate:
		return "IGNORE_AND_GEN"
	case PassThru:
		return "PASS_THRU"
	default:
		return "GEN_IF_ABSENT"
	}
}

// contentSync is the descriptor's contentSynchronisation for the stored
// cs: all off where nothing was stored.
func contentSync(cs *ContentSynchronisation) DescriptorContentSync {
	var d DescriptorContentSync
	if cs == nil {
		return d
	}
	d.Enabled = cs.Enabled
	if cs.Statistics != nil {
		d.Statistics = *cs.Statistics
	}
	if cs.Properties != nil {
		d.Properties = *cs.Properties
	}
	if cs.Source != nil {
		d.Source = *cs.Source
	}
	return d
}

// WithoutCredentials returns the URL raw without a user name and password
// written into it, which 7.x never keeps in a remote's URL: on every save,
// HttpRepoDescriptor.processUrl moves them into the username and password.
// A user name alone, or with an empty password, stays, as processUrl
// leaves it (its split on ":" gives one part). raw comes back as it is when
// it does not parse.
func WithoutCredentials(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	if p, _ := u.User.Password(); p == "" {
		return raw
	}
	u.User = nil
	return u.String()
}

func boolOr(p *bool, v bool) bool {
	if p == nil {
		return v
	}
	return *p
}

func intOr(p *int, v int) int {
	if p == nil {
		return v
	}
	return *p
}

func stringOr(s, v string) string {
	if s == "" {
		return v
	}
	return s
}
