package layout

import "fmt"

// Integration-revision regexps, as Artifactory's default descriptor writes
// them. Maven's file revision accepts either the literal SNAPSHOT or a unique
// timestamp-build suffix; Ivy, Gradle and sbt use a 14-digit timestamp; Maven 1
// keeps the ".+" of the original layouts, and every later layout ".*".
const (
	mavenFolderItegRev = `SNAPSHOT`
	mavenFileItegRev   = `SNAPSHOT|(?:(?:[0-9]{8}.[0-9]{6})-(?:[0-9]+))`
	timestampItegRev   = `\d{14}`
	maven1ItegRev      = `.+`
	anyItegRev         = `.*`
)

// bundled are the layouts Artifactory 7.x ships, in the order a 7.x
// configuration descriptor lists them (research 03, "Bundled definitions").
// Their sources, best first:
//   - JFrog's own 7.38.8 export, a test fixture of the JFrog CLI
//     (jfrog/jfrog-cli-artifactory 4a0f3827cfa0,
//     artifactory/commands/testdata/artifactory_export), defines all but
//     gradle-default, ansible-default, sbt-ivy-default and nix-default;
//   - configuration descriptors exported from 7.x instances, schemas 3.1.26
//     to 3.4.8, agree with it and add ansible-default and sbt-ivy-default;
//   - Artifactory 6.20's default descriptor (artifactory.config.xml in the
//     knottnhupfer/artifactory-oss mirror at b99e5649bcad) gives
//     gradle-default, which JFrog's repository-layouts page still lists;
//   - nix-default has no source but its name (DV-055).
//
// What JFrog's page does not define is on the P19 list (#216). npm-default
// departs from 7.x on purpose (DV-100). The Terraform, Swift and Ansible
// layouts use tokens outside the documented set; Lookup drops them, as
// Artifactory's parser does, so those layouts match no path.
var bundled = []Definition{
	{
		Name:                             "maven-2-default",
		ArtifactPathPattern:              "[orgPath]/[module]/[baseRev](-[folderItegRev])/[module]-[baseRev](-[fileItegRev])(-[classifier]).[ext]",
		DistinctiveDescriptorPathPattern: true,
		DescriptorPathPattern:            "[orgPath]/[module]/[baseRev](-[folderItegRev])/[module]-[baseRev](-[fileItegRev])(-[classifier]).pom",
		FolderIntegrationRevisionRegExp:  mavenFolderItegRev,
		FileIntegrationRevisionRegExp:    mavenFileItegRev,
	},
	{
		// Ivy's own default pattern,
		// [organisation]/[module]/[revision]/[type]s/[artifact].[ext], puts no
		// revision in the file name, so this layout does not match it.
		Name:                             "ivy-default",
		ArtifactPathPattern:              "[org]/[module]/[baseRev](-[folderItegRev])/[type]s/[module](-[classifier])-[baseRev](-[fileItegRev]).[ext]",
		DistinctiveDescriptorPathPattern: true,
		DescriptorPathPattern:            "[org]/[module]/[baseRev](-[folderItegRev])/[type]s/ivy-[baseRev](-[fileItegRev]).xml",
		FolderIntegrationRevisionRegExp:  timestampItegRev,
		FileIntegrationRevisionRegExp:    timestampItegRev,
	},
	{
		// artifactory.config.xml:128-135, in 6.20's place, which a 7.x
		// instance upgraded from 6.x keeps. JFrog's 7.38.8 export lacks it, as
		// RTFACT-21231 deprecated it for maven-2-default. The descriptor sits
		// beside the module folders, as in Gradle's old Wharf cache, not in
		// the revision folder where Gradle's ivy-publish writes it.
		Name:                             "gradle-default",
		ArtifactPathPattern:              "[org]/[module]/[baseRev](-[folderItegRev])/[module]-[baseRev](-[fileItegRev])(-[classifier]).[ext]",
		DistinctiveDescriptorPathPattern: true,
		DescriptorPathPattern:            "[org]/[module]/ivy-[baseRev](-[fileItegRev]).xml",
		FolderIntegrationRevisionRegExp:  timestampItegRev,
		FileIntegrationRevisionRegExp:    timestampItegRev,
	},
	{
		Name:                             "maven-1-default",
		ArtifactPathPattern:              "[org]/[type]s/[module]-[baseRev](-[fileItegRev])(-[classifier]).[ext]",
		DistinctiveDescriptorPathPattern: true,
		DescriptorPathPattern:            "[org]/[type]s/[module]-[baseRev](-[fileItegRev]).pom",
		FolderIntegrationRevisionRegExp:  maven1ItegRev,
		FileIntegrationRevisionRegExp:    maven1ItegRev,
	},
	{
		Name:                            "nuget-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[module].[baseRev](-[fileItegRev]).nupkg",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// 7.x's pattern since 7.5.0 (RTFACT-21456) is
		// [orgPath]/-/[module]-[baseRev](-[fileItegRev]).tgz, which reads
		// <name>/-/<name>-<version>.tgz as organisation and module <name>.
		// [module] stops at a slash, so it matches no scoped tarball,
		// @scope/name/-/@scope/name-<version>.tgz. The optional scope group
		// reads one as module name of organisation @scope/name and changes
		// nothing else the layout reads or builds (DV-100). [module] is
		// greedy, so a pre-release version splits as 7.x splits it.
		Name:                            "npm-default",
		ArtifactPathPattern:             "[orgPath]/-/([scope<@[^/]+>]/)[module]-[baseRev](-[fileItegRev]).tgz",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		Name:                            "bower-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[module]-[baseRev](-[fileItegRev]).[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// JFrog's vcs-repositories page: the file revision is a 40-character
		// commit hash.
		Name:                            "vcs-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[refs<tags|branches>]/[baseRev]/[module]-[baseRev](-[fileItegRev])(-[classifier]).[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   `[a-zA-Z0-9]{40}`,
	},
	{
		// The slashes after the optional scala_ and sbt_ groups are outside
		// them, so only sbt plugin paths match; this is why JFrog's sbt page
		// says the layout does not support cleanup.
		Name:                             "sbt-default",
		ArtifactPathPattern:              "[org]/[module]/(scala_[scalaVersion<.+>])/(sbt_[sbtVersion<.+>])/[baseRev]/[type]s/[module](-[classifier]).[ext]",
		DistinctiveDescriptorPathPattern: true,
		DescriptorPathPattern:            "[org]/[module]/(scala_[scalaVersion<.+>])/(sbt_[sbtVersion<.+>])/[baseRev]/[type]s/ivy.xml",
		FolderIntegrationRevisionRegExp:  timestampItegRev,
		FileIntegrationRevisionRegExp:    timestampItegRev,
	},
	{
		Name:                            "simple-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[module]-[baseRev].[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// It describes the one path JFrog's Cargo page accepts,
		// crates/<name>/<name>-<version>.crate; a crate stored anywhere else
		// is not indexed. It names no organisation, so Read finds no module
		// in it.
		Name:                            "cargo-default",
		ArtifactPathPattern:             "crates/[module]/[module]-[baseRev].[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		Name:                            "composer-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[module]-[baseRev](-[fileItegRev]).[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// Every 7.x source writes it without the "?" 6.20's descriptor had
		// after the optional package group. Artifactory's parser reads that
		// "?" as a literal, so only 7.x's form matches recipe and package
		// files.
		Name:                            "conan-default",
		ArtifactPathPattern:             "[org]/[module]/[baseRev]/[channel<[^/]+>]/[folderItegRev]/(package/[package_id<[^/]+>]/[fileItegRev]/)[remainder<(?:.+)>]",
		FolderIntegrationRevisionRegExp: `[^/]+`,
		FileIntegrationRevisionRegExp:   `[^/]+`,
	},
	{
		Name:                            "puppet-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[orgPath]-[module]-[baseRev].tar.gz",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// A fresh 7.x instance's; one upgraded from 6.x keeps 6.20's
		// [orgPath]/[module]/@v/v[refs].zip (RTFACT-31118). The v stays
		// outside [baseRev], so v1.2.3-rc.1 reads as base revision 1.2.3
		// and file revision rc.1, and a pseudo-version's timestamp and
		// commit are its file revision.
		Name:                            "go-default",
		ArtifactPathPattern:             "[orgPath]/[module]/@v/v[baseRev](-[fileItegRev]).[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		Name:                            "build-default",
		ArtifactPathPattern:             "[orgPath]/[module](-[fileItegRev]).[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		Name:                            "terraform-module-default",
		ArtifactPathPattern:             "[namespace]/[module-name]/[provider]/[version].[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		Name:                            "terraform-provider-default",
		ArtifactPathPattern:             "[namespace]/[provider-name]/[version]/terraform-provider-[provider-name]_[version]_[os]_[arch].[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		Name:                            "swift-default",
		ArtifactPathPattern:             "[scope]/[name]/[name]-[version].zip",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// From the 3.4.x exports; JFrog's 7.38.8 export predates it.
		Name:                            "ansible-default",
		ArtifactPathPattern:             "collections/[namespace]/[name]/[version]/[namespace]-[name]-[version].tar.gz",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// From the 3.4.5 and 3.4.8 exports, which list it last; JFrog's
		// 7.38.8 export predates it. It is the Maven pattern with an Ivy
		// descriptor in the revision folder.
		Name:                             "sbt-ivy-default",
		ArtifactPathPattern:              "[orgPath]/[module]/[baseRev](-[folderItegRev])/[module]-[baseRev](-[fileItegRev])(-[classifier]).[ext]",
		DistinctiveDescriptorPathPattern: true,
		DescriptorPathPattern:            "[orgPath]/[module]/[baseRev](-[folderItegRev])/ivy-[baseRev](-[fileItegRev])(-[classifier]).xml",
		FolderIntegrationRevisionRegExp:  timestampItegRev,
		FileIntegrationRevisionRegExp:    timestampItegRev,
	},
	{
		// Named only by the JFrog Terraform provider's default map; no 7.x
		// source lists it, so it follows every layout they order, with
		// simple-default's pattern (DV-055).
		Name:                            "nix-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[module]-[baseRev].[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
}

// Bundled returns the definitions of the layouts Artifactory ships, in the
// order the descriptor lists them.
func Bundled() []Definition {
	out := make([]Definition, len(bundled))
	copy(out, bundled)
	return out
}

// Lookup compiles a bundled layout by name. Unlike Compile, it drops a
// token outside the documented set, as Artifactory's parser does when it
// matches a path, because 7.x ships layouts that use such tokens.
func Lookup(name string) (*Layout, error) {
	for _, d := range bundled {
		if d.Name == name {
			return compile(d, true)
		}
	}
	return nil, fmt.Errorf("%w: unknown bundled layout %q", ErrPattern, name)
}
