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

// bundled are the layouts Artifactory ships, in the order its default
// configuration descriptor lists them, followed by those added in 7.x.
//
// The first fifteen are copied from Artifactory 6.20's descriptor,
// base/config/src/main/resources/META-INF/default/artifactory.config.xml in
// the knottnhupfer/artifactory-oss mirror at b99e5649bcad (line numbers
// below), the last open-source release; maven-2-default, ivy-default and
// gradle-default also match JFrog's repository-layouts page. Two depart from
// the descriptor on purpose, each recorded in docs/implementation/deviations.md.
//
// 7.x adds seven layouts, named by the JFrog Terraform provider's default map
// or JFrog's documentation, whose definitions JFrog does not publish.
// cargo-default is copied from configuration descriptors exported from 7.x
// instances (research 03, "Bundled definitions"); the other six are
// reconstructed from the storage paths JFrog documents (DV-055). All seven are
// on the P19 comparison list (#216).
var bundled = []Definition{
	{
		// artifactory.config.xml:112-119.
		Name:                             "maven-2-default",
		ArtifactPathPattern:              "[orgPath]/[module]/[baseRev](-[folderItegRev])/[module]-[baseRev](-[fileItegRev])(-[classifier]).[ext]",
		DistinctiveDescriptorPathPattern: true,
		DescriptorPathPattern:            "[orgPath]/[module]/[baseRev](-[folderItegRev])/[module]-[baseRev](-[fileItegRev])(-[classifier]).pom",
		FolderIntegrationRevisionRegExp:  mavenFolderItegRev,
		FileIntegrationRevisionRegExp:    mavenFileItegRev,
	},
	{
		// artifactory.config.xml:120-127. Ivy's own default pattern,
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
		// artifactory.config.xml:128-135. The descriptor sits beside the module
		// folders, as in Gradle's old Wharf cache, not in the revision folder
		// where Gradle's ivy-publish writes ivy-<revision>.xml.
		Name:                             "gradle-default",
		ArtifactPathPattern:              "[org]/[module]/[baseRev](-[folderItegRev])/[module]-[baseRev](-[fileItegRev])(-[classifier]).[ext]",
		DistinctiveDescriptorPathPattern: true,
		DescriptorPathPattern:            "[org]/[module]/ivy-[baseRev](-[fileItegRev]).xml",
		FolderIntegrationRevisionRegExp:  timestampItegRev,
		FileIntegrationRevisionRegExp:    timestampItegRev,
	},
	{
		// artifactory.config.xml:136-143.
		Name:                             "maven-1-default",
		ArtifactPathPattern:              "[org]/[type]s/[module]-[baseRev](-[fileItegRev])(-[classifier]).[ext]",
		DistinctiveDescriptorPathPattern: true,
		DescriptorPathPattern:            "[org]/[type]s/[module]-[baseRev](-[fileItegRev]).pom",
		FolderIntegrationRevisionRegExp:  maven1ItegRev,
		FileIntegrationRevisionRegExp:    maven1ItegRev,
	},
	{
		// artifactory.config.xml:144-150.
		Name:                            "nuget-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[module].[baseRev](-[fileItegRev]).nupkg",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// artifactory.config.xml:151-157 (research 25 section 5). It does not
		// describe <name>/-/<name>-<version>.tgz, where npm and the JFrog CLI
		// store tarballs, so the npm format derives that path from package.json
		// rather than from this layout (decision 0034).
		Name:                            "npm-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[module]-[baseRev](-[fileItegRev]).tgz",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// artifactory.config.xml:158-164.
		Name:                            "bower-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[module]-[baseRev](-[fileItegRev]).[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// artifactory.config.xml:165-171, and JFrog's vcs-repositories page;
		// the file revision is a 40-character commit hash.
		Name:                            "vcs-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[refs<tags|branches>]/[baseRev]/[module]-[baseRev](-[fileItegRev])(-[classifier]).[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   `[a-zA-Z0-9]{40}`,
	},
	{
		// artifactory.config.xml:172-179. The slashes after the optional scala_
		// and sbt_ groups are outside them, so only sbt plugin paths match; this
		// is why JFrog's sbt page says the layout does not support cleanup.
		Name:                             "sbt-default",
		ArtifactPathPattern:              "[org]/[module]/(scala_[scalaVersion<.+>])/(sbt_[sbtVersion<.+>])/[baseRev]/[type]s/[module](-[classifier]).[ext]",
		DistinctiveDescriptorPathPattern: true,
		DescriptorPathPattern:            "[org]/[module]/(scala_[scalaVersion<.+>])/(sbt_[sbtVersion<.+>])/[baseRev]/[type]s/ivy.xml",
		FolderIntegrationRevisionRegExp:  timestampItegRev,
		FileIntegrationRevisionRegExp:    timestampItegRev,
	},
	{
		// artifactory.config.xml:180-186.
		Name:                            "simple-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[module]-[baseRev].[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// artifactory.config.xml:187-193.
		Name:                            "composer-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[module]-[baseRev](-[fileItegRev]).[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// artifactory.config.xml:194-200, without the "?" the descriptor writes
		// after the optional package group. Artifactory's parser escapes a "?"
		// outside a token, so its layout needs a literal "?" in the path and
		// matches no Conan file (DV-056).
		Name:                            "conan-default",
		ArtifactPathPattern:             "[org]/[module]/[baseRev]/[channel<[^/]+>]/[folderItegRev]/(package/[package_id<[^/]+>]/[fileItegRev]/)[remainder<(?:.+)>]",
		FolderIntegrationRevisionRegExp: `[^/]+`,
		FileIntegrationRevisionRegExp:   `[^/]+`,
	},
	{
		// artifactory.config.xml:201-207.
		Name:                            "puppet-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[orgPath]-[module]-[baseRev].tar.gz",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// artifactory.config.xml:208-214 is [orgPath]/[module]/@v/v[refs].zip;
		// its parser drops the unknown [refs] token, so that layout matches no
		// module file (decision 0035, DV-040).
		Name:                            "go-default",
		ArtifactPathPattern:             "[orgPath]/[module]/@v/[baseRev].[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// artifactory.config.xml:215-221.
		Name:                            "build-default",
		ArtifactPathPattern:             "[orgPath]/[module](-[fileItegRev]).[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// Every exported 7.x descriptor defines it this way, and lists it
		// before the other 7.x additions. It describes the one path JFrog's
		// Cargo page accepts, crates/<name>/<name>-<version>.crate; a crate
		// stored anywhere else is not indexed.
		Name:                            "cargo-default",
		ArtifactPathPattern:             "crates/[module]/[module]-[baseRev].[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// Named by JFrog's sbt page "for Ivy projects". sbt's Ivy-style
		// pattern (librarymanagement Resolver.ivyStylePatterns) with the plugin
		// folders optional, so plain modules and plugins both match.
		Name:                             "sbt-ivy-default",
		ArtifactPathPattern:              "[org]/[module]/(scala_[scalaVersion<[^/]+>]/)(sbt_[sbtVersion<[^/]+>]/)[baseRev]/[type]s/[module](-[classifier]).[ext]",
		DistinctiveDescriptorPathPattern: true,
		DescriptorPathPattern:            "[org]/[module]/(scala_[scalaVersion<[^/]+>]/)(sbt_[sbtVersion<[^/]+>]/)[baseRev]/[type]s/ivy.xml",
		FolderIntegrationRevisionRegExp:  timestampItegRev,
		FileIntegrationRevisionRegExp:    timestampItegRev,
	},
	{
		// <namespace>/<module>/<provider>/<version>.zip, from JFrog's Terraform
		// repositories page.
		Name:                            "terraform-module-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[provider<[^/]+>]/[baseRev].[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// <namespace>/<type>/<version>/terraform-provider-<type>_<version>_<os>_<arch>.zip,
		// from JFrog's Terraform repositories page.
		Name:                            "terraform-provider-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[baseRev]/terraform-provider-[module]_[baseRev]_[os<[^_/]+>]_[arch<[^./]+>].[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// <namespace>/<name>/<namespace>-<name>-<version>.tar.gz, the archive
		// name ansible-galaxy collection build writes; JFrog documents no
		// storage path.
		Name:                            "ansible-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[orgPath]-[module]-[baseRev].tar.gz",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// <scope>/<name>/<name>-<version>.zip, from JFrog's Swift page.
		Name:                            "swift-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[module]-[baseRev].[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// JFrog documents no Nix storage path; simple-default's pattern.
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

// Lookup compiles a bundled layout by name.
func Lookup(name string) (*Layout, error) {
	for _, d := range bundled {
		if d.Name == name {
			return Compile(d)
		}
	}
	return nil, fmt.Errorf("%w: unknown bundled layout %q", ErrPattern, name)
}
