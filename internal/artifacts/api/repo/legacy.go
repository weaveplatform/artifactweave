package repo

import "slices"

// typeFlag is one legacy enable<Type>Support flag: the type a create that
// sends it without packageType takes, and the package types whose reads
// set it true.
type typeFlag struct {
	flag  **bool
	types []PackageType
}

// typeFlags lists c's legacy flags in the order 7.161.26's
// RepositoryConfigurationBase.getPackageType reads them when packageType
// is blank: NuGet, Gems, npm, Bower, CocoaPods, Debian, Distribution, PyPI,
// Puppet, Docker, Vagrant, GitLFS, Composer, Conan, Chef. The types are
// setPackageTypeConfig's, which sets enableDockerSupport for OCI and Helm
// OCI too; the first of each is the type a create takes.
func (c *Config) typeFlags() []typeFlag {
	return []typeFlag{
		{&c.EnableNuGetSupport, []PackageType{NuGet}},
		{&c.EnableGemsSupport, []PackageType{Gems}},
		{&c.EnableNpmSupport, []PackageType{NPM}},
		{&c.EnableBowerSupport, []PackageType{Bower}},
		{&c.EnableCocoaPodsSupport, []PackageType{CocoaPods}},
		{&c.EnableDebianSupport, []PackageType{Debian}},
		{&c.EnableDistRepoSupport, []PackageType{Distribution}},
		{&c.EnablePypiSupport, []PackageType{PyPI}},
		{&c.EnablePuppetSupport, []PackageType{Puppet}},
		{&c.EnableDockerSupport, []PackageType{Docker, OCI, HelmOCI}},
		{&c.EnableVagrantSupport, []PackageType{Vagrant}},
		{&c.EnableGitLfsSupport, []PackageType{GitLFS}},
		{&c.EnableComposerSupport, []PackageType{Composer}},
		// 7.161.26 maps this flag to RepoType.Conda, a slip in a list where
		// every other flag names its own type; artifactweave follows the
		// intended mapping, Conan, as setPackageTypeConfig sets the flag for
		// Conan repositories.
		{&c.EnableConanSupport, []PackageType{Conan}},
		{&c.EnableChefSupport, []PackageType{Chef}},
	}
}

// legacyPackageType is the package type 7.x gives a create that sends no
// packageType: that of the first legacy flag sent true, else generic.
// packageType is not required on a create ([D] createrepository).
func legacyPackageType(c *Config) PackageType {
	for _, f := range c.typeFlags() {
		if *f.flag != nil && **f.flag {
			return f.types[0]
		}
	}
	return Generic
}

// setTypeFlags sets each legacy flag as 7.x's read model does
// (RepositoryConfigurationBase.setPackageTypeConfig, 7.161.26): true for c's
// own package type and false for the rest, whatever c holds.
func setTypeFlags(c *Config) {
	for _, f := range c.typeFlags() {
		*f.flag = new(slices.Contains(f.types, c.PackageType))
	}
}

// clearTypeFlags removes every legacy flag, as no read but the v1 read
// carries one.
func clearTypeFlags(c *Config) {
	for _, f := range c.typeFlags() {
		*f.flag = nil
	}
}
