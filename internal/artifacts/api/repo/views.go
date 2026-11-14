package repo

import "slices"

// Public returns the subset of c that a caller who can read the repository
// but is not an administrator receives: identity, layout and patterns, with
// nothing that configures access or holds a secret.
func Public(c Config) Config {
	return Config{
		Key: c.Key, Class: c.Class, PackageType: c.PackageType, Description: c.Description,
		RepoLayoutRef: c.RepoLayoutRef, IncludesPattern: c.IncludesPattern,
		ExcludesPattern: c.ExcludesPattern,
	}
}

// Masked returns c without its secrets, for every reader: the remote
// password and the values of sensitive custom headers. An update that omits
// the password, or sends a sensitive header back with an empty value, keeps
// the stored secret (RestoreSecrets). The repositories API's reads send
// Full, which starts from Masked; the shared remote configuration sends
// Masked itself, as 7.x answers that read with its remote repository
// descriptor (RepoConfigurationResource.getRemoteDescriptor, 7.161.26),
// which holds none of the legacy enable<Type>Support flags.
//
// It also lists no environments as [] rather than leaving the key out, as
// Artifactory does: the Terraform provider defaults project_environments to an
// empty set and reads an absent key as null, so every plan would show a change.
// It lists no property sets as [] too, as Artifactory's configuration samples
// do, except on a virtual repository, whose configuration has no such field.
//
// A Debian repository that never set ddebSupported or debianTrivialLayout
// reads them as false, the value it acts on (#1372). 7.161.26's
// configuration model (RepositoryConfigurationBase, the base of every
// class's) holds both with the default false; RTFACT-30304 shows
// "ddebSupported" : false in a Debian remote's configuration, and the
// Terraform provider's sample reads (samples/repository_remote/debian.json
// and samples/generic-repo-local.json) give both false. No dump of a
// virtual repository's read was found [I]. The v2 read keeps only what
// Relevant keeps of them.
func Masked(c Config) Config {
	c.Password = ""
	if c.Environments == nil {
		c.Environments = []string{}
	}
	if c.PackageType == Debian {
		if c.DdebSupported == nil {
			c.DdebSupported = new(bool)
		}
		if c.DebianTrivialLayout == nil {
			c.DebianTrivialLayout = new(bool)
		}
	}
	// The provider's repositories has the same empty default and the same
	// null reading of an absent key, so a virtual repository without
	// members lists none as [].
	if c.Class == Virtual && c.Repositories == nil {
		c.Repositories = []string{}
	}
	if c.PropertySets == nil && c.Class != Virtual {
		c.PropertySets = []string{}
	}
	if len(c.CustomHTTPHeaders) > 0 {
		hs := slices.Clone(c.CustomHTTPHeaders)
		for i := range hs {
			if hs[i].Sensitive {
				hs[i].Value = ""
			}
		}
		c.CustomHTTPHeaders = hs
	}
	return c
}

// Full returns what an administrator's v1 read, GET /api/repositories/{key},
// sends of c: Masked(c) with the fields 7.x's read model writes for every
// repository of every class, whatever was stored. 7.161.26's
// RepositoryConfigurationBase, which every class's model extends, holds
// them with these defaults, and the v1 read writes every field of the
// model, as its dumps of Docker, npm and Maven local repositories
// (RTFACT-29415, RTFACT-30672, RTFACT-31100) and the Terraform provider's
// sample reads of generic local and remote repositories and remotes of
// other types (samples/generic-repo-local.json, samples/repository_remote)
// show:
//
//   - the legacy enable<Type>Support flags, true for c's own package type
//     and false for the rest (setPackageTypeConfig);
//   - forceNugetAuthentication, forceP2Authentication and
//     forceConanAuthentication, taken from the configuration of a NuGet, P2
//     or Conan repository and false for every other type, which is what
//     each acts on (setPackageTypeConfig sets each for its own type only);
//   - ddebSupported and debianTrivialLayout, taken from a Debian
//     repository's configuration (Masked) and false for every other type,
//     as only a Debian repository's configuration sets them
//     (LocalRepositoryConfigurationImpl.setDebianConfigIfNeeded);
//   - signedUrlTtl, 90 when unset, which the configurations list's sample
//     shows too ([D] getallrepositoryconfigurations).
//
// No dump of a virtual repository's v1 read was found, so that it carries
// them is inferred from the model [I]. The v2 read and the configurations
// list keep what Relevant keeps of them: signedUrlTtl, and none of the
// legacy flags.
func Full(c Config) Config {
	c = Masked(c)
	setTypeFlags(&c)
	for own, flag := range map[PackageType]**bool{
		NuGet: &c.ForceNugetAuthentication,
		P2:    &c.ForceP2Authentication,
		Conan: &c.ForceConanAuthentication,
	} {
		if c.PackageType != own || *flag == nil {
			*flag = new(false)
		}
	}
	if c.PackageType != Debian {
		c.DdebSupported, c.DebianTrivialLayout = new(false), new(false)
	}
	if c.SignedURLTTL == nil {
		c.SignedURLTTL = new(DefaultSignedURLTTL)
	}
	return c
}

// RestoreSecrets copies stored sensitive header values into updated where
// updated carries the same header back with the empty value Masked gave it,
// so a client that reads a configuration and writes it back does not wipe
// its secrets.
func RestoreSecrets(stored Config, updated *Config) {
	for i, h := range updated.CustomHTTPHeaders {
		if !h.Sensitive || h.Value != "" {
			continue
		}
		for _, s := range stored.CustomHTTPHeaders {
			if s.Name == h.Name && s.Sensitive {
				updated.CustomHTTPHeaders[i].Value = s.Value
			}
		}
	}
}

// relevance names the package types each group of package-specific fields
// applies to. 7.161.26's IncludeTypeSpecific annotations list the fields the
// v2 read keeps per package type and class; the groups follow the
// per-ecosystem sections of research 03, and fields shared by several
// ecosystems are always kept. Research 03, "The v2 read and 7.161.26's
// annotations", lists where the groups and the annotations differ [I]
// (#1479). checksumPolicyType is a Maven field here because JFrog's generic
// local samples, the v2 read and the configurations list alike, leave it
// out. maxUniqueSnapshots is NuGet's as well as the Maven family's, as
// 7.161.26 marks it on local repositories. A local or federated npm
// repository applies the checksum policy and the release and snapshot
// handling too (#481), so it keeps those three fields; an npm remote or
// virtual repository does not. also, when set, keeps a group for a
// configuration whose package type is not in types.
var relevance = []struct {
	types []PackageType
	also  func(Config) bool
	clear func(*Config)
}{
	{[]PackageType{Maven, Gradle, Ivy, SBT}, npmHosted, func(c *Config) {
		c.ChecksumPolicyType = ""
		c.HandleReleases, c.HandleSnapshots = nil, nil
	}},
	{[]PackageType{Maven, Gradle, Ivy, SBT}, nil, func(c *Config) {
		c.SnapshotVersionBehavior, c.SuppressPomConsistencyChecks = "", nil
		c.FetchJarsEagerly, c.FetchSourcesEagerly, c.RejectInvalidJars = nil, nil, nil
		c.PomRepositoryReferencesCleanupPolicy, c.ForceMavenAuthentication = "", nil
	}},
	{[]PackageType{Maven, Gradle, Ivy, SBT, NuGet}, nil, func(c *Config) {
		c.MaxUniqueSnapshots = nil
	}},
	{[]PackageType{Docker, OCI, HelmOCI}, nil, func(c *Config) {
		c.MaxUniqueTags, c.DockerTagRetention, c.DockerAPIVersion = nil, nil, ""
		c.BlockPushingSchema1, c.ResolveDockerTagsByTimestamp, c.DockerProjectID = nil, nil, ""
	}},
	{[]PackageType{RPM}, nil, func(c *Config) {
		c.CalculateYumMetadata, c.YumRootDepth, c.EnableFileListsIndexing = nil, nil, nil
		c.YumGroupFileNames = ""
	}},
	{[]PackageType{Debian}, nil, func(c *Config) {
		c.DebianTrivialLayout, c.OptionalIndexCompressionFormats = nil, nil
		c.DebianDefaultArchitectures, c.DdebSupported = "", nil
	}},
	// 7.161.26's RepositoryConfigurationBase marks debianTrivialLayout
	// @IncludeTypeSpecific(packageType = Debian, repoType = {LOCAL,
	// FEDERATED}), so a Debian remote or virtual repository's v2 read
	// leaves it out, and ddebSupported for every Debian class (#1372).
	{nil, debianHosted, func(c *Config) { c.DebianTrivialLayout = nil }},
	{[]PackageType{NuGet}, nil, func(c *Config) {
		c.ForceNugetAuthentication = nil
		c.FeedContextPath, c.DownloadContextPath = nil, nil
		c.V3FeedURL, c.SymbolServerURL = "", ""
	}},
	{[]PackageType{Conan}, nil, func(c *Config) { c.ForceConanAuthentication = nil }},
	// No package type keeps a legacy enable<Type>Support flag: 7.161.26's
	// v2 serializer (TypeSpecificFilter) writes only the fields marked
	// IncludeTypeSpecific, and RepositoryConfigurationBase marks none of
	// them.
	{nil, nil, clearTypeFlags},
	{
		[]PackageType{P2},
		nil,
		func(c *Config) { c.ForceP2Authentication, c.P2URLs, c.P2ChildOrigins = nil, nil, nil },
	},
	{[]PackageType{Cargo}, nil, func(c *Config) {
		c.CargoAnonymousAccess, c.CargoInternalIndex, c.GitRegistryURL = nil, nil, ""
	}},
	{[]PackageType{CocoaPods}, nil, func(c *Config) { c.PodsSpecsRepoURL, c.PodsCdnURL = "", "" }},
	{[]PackageType{Hex}, nil, func(c *Config) { c.HexPublicKey = "" }},
	{[]PackageType{Generic}, nil, func(c *Config) {
		c.PropagateQueryParams, c.RetrieveSha256FromServer = nil, nil
	}},
	{[]PackageType{Helm}, nil, func(c *Config) {
		c.ForceMetadataNameVersion, c.ForceNonDuplicateChart, c.ChartsBaseURL = nil, nil, ""
	}},
	{
		[]PackageType{PyPI},
		nil,
		func(c *Config) { c.PyPIRegistryURL, c.PyPIRepositorySuffix = "", "" },
	},
	{[]PackageType{Go, VCS, Bower}, nil, func(c *Config) {
		c.VCSType, c.VCSGitProvider, c.VCSGitDownloadURL = "", "", ""
	}},
	{[]PackageType{Terraform, TerraformBackend}, nil, func(c *Config) {
		c.TerraformType, c.TerraformRegistryURL, c.TerraformProvidersURL = "", "", ""
		c.EncryptStates = nil
	}},
	{[]PackageType{Bower}, nil, func(c *Config) { c.BowerRegistryURL = "" }},
	{[]PackageType{Composer}, nil, func(c *Config) { c.ComposerRegistryURL = "" }},
}

// Relevant returns c without the package-specific fields that do not apply
// to its package type, the shape GET /api/v2/repositories/{key} and each
// entry of GET /api/repositories/configurations return: a generic repository
// has no Maven or Docker fields, as JFrog's generic local samples of both
// show.
func Relevant(c Config) Config {
	for _, g := range relevance {
		if !slices.Contains(g.types, c.PackageType) && (g.also == nil || !g.also(c)) {
			g.clear(&c)
		}
	}
	return c
}

// debianHosted reports a local or federated Debian repository, the classes
// whose layout debianTrivialLayout sets.
func debianHosted(c Config) bool {
	return c.PackageType == Debian && (c.Class == Local || c.Class == Federated)
}

// npmHosted reports a local or federated npm repository, which applies the
// checksum policy and the release and snapshot handling to what is
// published to it.
func npmHosted(c Config) bool {
	return c.PackageType == NPM && (c.Class == Local || c.Class == Federated)
}
