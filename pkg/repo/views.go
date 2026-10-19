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
// and fields shared by several ecosystems are always kept.
var relevance = []struct {
	types []PackageType
	clear func(*Config)
}{
	{[]PackageType{Maven, Gradle, Ivy, SBT}, func(c *Config) {
		c.HandleReleases, c.HandleSnapshots, c.MaxUniqueSnapshots = nil, nil, nil
		c.SnapshotVersionBehavior, c.SuppressPomConsistencyChecks = "", nil
		c.FetchJarsEagerly, c.FetchSourcesEagerly, c.RejectInvalidJars = nil, nil, nil
		c.PomRepositoryReferencesCleanupPolicy, c.ForceMavenAuthentication = "", nil
	}},
	{[]PackageType{Docker, OCI, HelmOCI}, func(c *Config) {
		c.MaxUniqueTags, c.DockerTagRetention, c.DockerAPIVersion = nil, nil, ""
		c.BlockPushingSchema1, c.ResolveDockerTagsByTimestamp, c.DockerProjectID = nil, nil, ""
	}},
	{[]PackageType{RPM}, func(c *Config) {
		c.CalculateYumMetadata, c.YumRootDepth, c.EnableFileListsIndexing = nil, nil, nil
	}},
	{[]PackageType{Debian}, func(c *Config) {
		c.DebianTrivialLayout, c.OptionalIndexCompressionFormats = nil, nil
		c.DebianDefaultArchitectures = ""
	}},
	{[]PackageType{NuGet}, func(c *Config) {
		c.ForceNugetAuthentication = nil
		c.FeedContextPath, c.DownloadContextPath, c.V3FeedURL, c.SymbolServerURL = "", "", "", ""
	}},
	{[]PackageType{Conan}, func(c *Config) { c.ForceConanAuthentication = nil }},
	{[]PackageType{P2}, func(c *Config) { c.ForceP2Authentication = nil }},
	{[]PackageType{Helm}, func(c *Config) { c.ForceNonDuplicateChart, c.ChartsBaseURL = nil, "" }},
	{[]PackageType{PyPI}, func(c *Config) { c.PyPIRegistryURL, c.PyPIRepositorySuffix = "", "" }},
	{[]PackageType{Go, VCS}, func(c *Config) {
		c.VCSType, c.VCSGitProvider, c.VCSGitDownloadURL = "", "", ""
	}},
	{[]PackageType{Terraform, TerraformBackend}, func(c *Config) {
		c.TerraformType, c.TerraformRegistryURL, c.TerraformProvidersURL = "", "", ""
		c.EncryptStates = nil
	}},
	{[]PackageType{Bower}, func(c *Config) { c.BowerRegistryURL = "" }},
	{[]PackageType{Composer}, func(c *Config) { c.ComposerRegistryURL = "" }},
}

// Relevant returns c without the package-specific fields that do not apply
// to its package type, the shape GET /api/v2/repositories/{key} returns: a
// generic repository has no Maven or Docker fields.
func Relevant(c Config) Config {
	for _, g := range relevance {
		if !slices.Contains(g.types, c.PackageType) {
			g.clear(&c)
		}
	}
	return c
}
