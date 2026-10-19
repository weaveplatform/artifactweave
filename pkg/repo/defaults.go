package repo

// Default values documented for repository configuration fields. Each one
// must match docs/research/03-repository-configuration-model.md, which cites
// JFrog's configuration-json-files page; the Terraform providers compare the
// values they read back against these.
const (
	DefaultIncludesPattern                = "**/*"
	DefaultRetrievalCachePeriodSecs       = 7200
	DefaultDockerRetrievalCachePeriodSecs = 21600
	DefaultMissedRetrievalCachePeriodSecs = 1800
	DefaultGoMissedRetrievalCachePeriod   = 600
	DefaultSocketTimeoutMillis            = 15000
	DefaultAssumedOfflinePeriodSecs       = 300
	DefaultMetadataRetrievalTimeoutSecs   = 60
	DefaultDockerTagRetention             = 1
	DefaultVirtualRetrievalCachePeriod    = 7200
	DefaultPyPIRegistryURL                = "https://pypi.org"
	DefaultPyPIRepositorySuffix           = "simple"
	DefaultFeedContextPath                = "api/v2"
	DefaultDownloadContextPath            = "api/v2/package"
	DefaultV3FeedURL                      = "https://api.nuget.org/v3/index.json"
	DefaultVCSType                        = "GIT"
	DefaultBowerRegistryURL               = "https://registry.bower.io"
	DefaultCargoGitRegistryURL            = "https://github.com/rust-lang/crates.io-index"
	DefaultComposerRegistryURL            = "https://packagist.org"
	DefaultTerraformRegistryURL           = "https://registry.terraform.io"
	DefaultTerraformProvidersURL          = "https://releases.hashicorp.com"
)

// Minimums of a remote repository's retrievalCachePeriodSecs. JFrog's
// remote repositories page gives 30 seconds for most remote repositories and
// 14,400 for a NuGet remote that proxies community.chocolatey.org; SaaS
// 7.144.2's notice exempts smart remote repositories from the 30.
const (
	MinRetrievalCachePeriodSecs           = 30
	MinChocolateyRetrievalCachePeriodSecs = 14400
)

// Defaults fills every field the caller left unset with the value Artifactory
// would apply on create. It returns the config so it can be chained; the
// receiver is modified in place. Fields that belong to another class are left
// alone so a local repository does not grow remote-only keys.
func Defaults(c *Config) *Config {
	if c.PackageType == "" {
		c.PackageType = Generic
	}
	setString(&c.IncludesPattern, DefaultIncludesPattern)
	setString(&c.ExcludesPattern, "")
	if c.RepoLayoutRef == "" {
		c.RepoLayoutRef = createLayout(c.PackageType)
	}
	setBool(&c.BlackedOut, false)
	setBool(&c.XrayIndex, false)
	setFlex(&c.PriorityResolution, false)
	setFlex(&c.DownloadRedirect, false)
	setFlex(&c.CDNRedirect, false)

	switch c.Class {
	case Local, Federated:
		localDefaults(c)
	case Remote:
		remoteDefaults(c)
	case Virtual:
		virtualDefaults(c)
	}
	return c
}

func localDefaults(c *Config) {
	if c.ChecksumPolicyType == "" {
		c.ChecksumPolicyType = ClientChecksums
	}
	setBool(&c.HandleReleases, true)
	setBool(&c.HandleSnapshots, true)
	setInt(&c.MaxUniqueSnapshots, 0)
	if c.SnapshotVersionBehavior == "" {
		c.SnapshotVersionBehavior = Unique
	}
	setBool(&c.SuppressPomConsistencyChecks, suppressPomChecks(c.PackageType))
	setInt(&c.MaxUniqueTags, 0)
	setInt(&c.DockerTagRetention, DefaultDockerTagRetention)
	if c.DockerAPIVersion == "" {
		c.DockerAPIVersion = DockerV2
	}
	setFlex(&c.BlockPushingSchema1, false)
	setBool(&c.ArchiveBrowsingEnabled, false)
	setFlex(&c.EnableFileListsIndexing, false)
	setBool(&c.ForceNugetAuthentication, false)
	setBool(&c.ForceP2Authentication, false)
	setBool(&c.ForceConanAuthentication, false)
	setBool(&c.ForceMetadataNameVersion, false)
	setBool(&c.ForceNonDuplicateChart, false)
	if c.PackageType == TerraformBackend {
		setBool(&c.EncryptStates, true)
	}
	for i := range c.Members {
		// A member without an explicit flag is active: JFrog documents "true"
		// as the default for members[].enabled.
		setFlex(&c.Members[i].Enabled, true)
	}
}

func remoteDefaults(c *Config) {
	if c.RemoteRepoChecksumPolicyType == "" {
		c.RemoteRepoChecksumPolicyType = GenerateIfAbsent
	}
	setBool(&c.HandleReleases, true)
	setBool(&c.HandleSnapshots, true)
	setInt(&c.MaxUniqueSnapshots, 0)
	setBool(&c.SuppressPomConsistencyChecks, suppressPomChecks(c.PackageType))
	setBool(&c.DisableProxy, false)
	setBool(&c.HardFail, false)
	setBool(&c.Offline, false)
	setBool(&c.StoreArtifactsLocally, true)
	setInt(&c.SocketTimeoutMillis, DefaultSocketTimeoutMillis)
	retrieval := DefaultRetrievalCachePeriodSecs
	if c.PackageType == Docker || c.PackageType == OCI || c.PackageType == HelmOCI {
		retrieval = DefaultDockerRetrievalCachePeriodSecs
	}
	setInt(&c.RetrievalCachePeriodSecs, retrieval)
	missed := DefaultMissedRetrievalCachePeriodSecs
	if c.PackageType == Go {
		missed = DefaultGoMissedRetrievalCachePeriod
	}
	setInt(&c.MissedRetrievalCachePeriodSecs, missed)
	setInt(&c.MetadataRetrievalTimeoutSecs, DefaultMetadataRetrievalTimeoutSecs)
	setInt(&c.AssumedOfflinePeriodSecs, DefaultAssumedOfflinePeriodSecs)
	setInt(&c.UnusedArtifactsCleanupPeriodHours, 0)
	setBool(&c.FetchJarsEagerly, false)
	setBool(&c.FetchSourcesEagerly, false)
	setBool(&c.RejectInvalidJars, false)
	setBool(&c.ShareConfiguration, false)
	setBool(&c.SynchronizeProperties, false)
	setBool(&c.BlockMismatchingMimeTypes, true)
	setBool(&c.AllowAnyHostAuth, false)
	setBool(&c.EnableCookieManagement, false)
	setBool(&c.EnableTokenAuthentication, false)
	setBool(&c.BypassHeadRequests, false)
	setFlex(&c.ListRemoteFolderItems, false)
	setBool(&c.DisableURLNormalization, false)
	setBool(&c.ExternalDependenciesEnabled, false)
	setFlex(&c.BlockPushingSchema1, false)
	setBool(&c.ForceNugetAuthentication, false)
	setBool(&c.ForceP2Authentication, false)
	setBool(&c.ForceConanAuthentication, false)
	switch c.PackageType {
	case PyPI:
		setStr(&c.PyPIRegistryURL, DefaultPyPIRegistryURL)
		setStr(&c.PyPIRepositorySuffix, DefaultPyPIRepositorySuffix)
	case NuGet:
		setStr(&c.FeedContextPath, DefaultFeedContextPath)
		setStr(&c.DownloadContextPath, DefaultDownloadContextPath)
		setStr(&c.V3FeedURL, DefaultV3FeedURL)
	case Go, VCS:
		setStr(&c.VCSType, DefaultVCSType)
		if c.VCSGitProvider == "" {
			c.VCSGitProvider = GitHub
		}
	case Bower:
		setStr(&c.BowerRegistryURL, DefaultBowerRegistryURL)
	case Cargo:
		setStr(&c.GitRegistryURL, DefaultCargoGitRegistryURL)
	case Composer:
		setStr(&c.ComposerRegistryURL, DefaultComposerRegistryURL)
	case Terraform:
		setStr(&c.TerraformRegistryURL, DefaultTerraformRegistryURL)
		setStr(&c.TerraformProvidersURL, DefaultTerraformProvidersURL)
	}
}

func virtualDefaults(c *Config) {
	setBool(&c.ArtifactoryRequestsCanRetrieveRemoteArtifacts, false)
	setInt(&c.VirtualRetrievalCachePeriodSecs, DefaultVirtualRetrievalCachePeriod)
	if c.PomRepositoryReferencesCleanupPolicy == "" {
		c.PomRepositoryReferencesCleanupPolicy = DiscardActiveReference
	}
	setBool(&c.ForceMavenAuthentication, false)
	setBool(&c.ForceNugetAuthentication, false)
	setBool(&c.ExternalDependenciesEnabled, false)
	setBool(&c.HideUnauthorizedResources, false)
	setBool(&c.ResolveDockerTagsByTimestamp, false)
	setBool(&c.UseNamespaces, false)
	if c.DockerAPIVersion == "" {
		c.DockerAPIVersion = DockerV2
	}
}

// suppressPomChecks is suppressPomConsistencyChecks' default for t: on for
// gradle, ivy and sbt and off for maven, as the Terraform provider and
// Artifactory's UI set it (research 27 section 6), and off for every other
// type, which has no POM checks.
func suppressPomChecks(t PackageType) bool { return t == Gradle || t == Ivy || t == SBT }

func setBool(p **bool, v bool) {
	if *p == nil {
		*p = &v
	}
}

func setInt(p **int, v int) {
	if *p == nil {
		*p = &v
	}
}

func setString(p **string, v string) {
	if *p == nil {
		*p = &v
	}
}

func setStr(p *string, v string) {
	if *p == "" {
		*p = v
	}
}

func setFlex(p **FlexBool, v bool) {
	if *p == nil {
		*p = &FlexBool{Value: v}
	}
}
