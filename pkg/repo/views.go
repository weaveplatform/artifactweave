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
// the stored secret (RestoreSecrets).
//
// It also lists no environments as [] rather than leaving the key out, as
// Artifactory does: the Terraform provider defaults project_environments to an
// empty set and reads an absent key as null, so every plan would show a change.
// It lists no property sets as [] too, as Artifactory's configuration samples
// do, except on a virtual repository, whose configuration has no such field.
func Masked(c Config) Config {
	c.Password = ""
	if c.Environments == nil {
		c.Environments = []string{}
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
// applies to. No JFrog source lists the fields the v2 read keeps per
// package type; the groups follow the per-ecosystem sections of research 03,
// and fields shared by several ecosystems are always kept. checksumPolicyType
// is a Maven field here because JFrog's generic local samples, the v2 read
// and the configurations list alike, leave it out. maxUniqueSnapshots is
// NuGet's as well as the Maven family's, as research 03's per-ecosystem
// summary lists it and 6.20's type-specific field list has it [I]. A local or
// federated npm repository applies the checksum policy and the release and
// snapshot handling too (#481), so it keeps those three fields; an npm remote
// or virtual repository does not. also, when set, keeps a group for a
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
	{[]PackageType{NuGet}, nil, func(c *Config) {
		c.ForceNugetAuthentication = nil
		c.FeedContextPath, c.DownloadContextPath, c.V3FeedURL, c.SymbolServerURL = "", "", "", ""
	}},
	{[]PackageType{Conan}, nil, func(c *Config) {
		c.ForceConanAuthentication, c.EnableConanSupport = nil, nil
	}},
	{[]PackageType{P2}, nil, func(c *Config) { c.ForceP2Authentication, c.P2URLs = nil, nil }},
	{[]PackageType{Cargo}, nil, func(c *Config) {
		c.CargoAnonymousAccess, c.CargoInternalIndex, c.GitRegistryURL = nil, nil, ""
	}},
	{[]PackageType{CocoaPods}, nil, func(c *Config) { c.PodsSpecsRepoURL = "" }},
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
	{[]PackageType{Go, VCS}, nil, func(c *Config) {
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

// npmHosted reports a local or federated npm repository, which applies the
// checksum policy and the release and snapshot handling to what is
// published to it.
func npmHosted(c Config) bool {
	return c.PackageType == NPM && (c.Class == Local || c.Class == Federated)
}
