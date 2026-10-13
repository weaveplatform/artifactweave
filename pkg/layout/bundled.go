package layout

import "fmt"

// Integration-revision regexps shared by the JVM layouts. Maven's file
// revision accepts either the literal SNAPSHOT or a unique timestamp-build
// suffix; Ivy and Gradle use a 14-digit timestamp.
const (
	mavenFolderItegRev = `SNAPSHOT`
	mavenFileItegRev   = `SNAPSHOT|(?:(?:\d{8}.\d{6})-(?:\d+))`
	timestampItegRev   = `\d{14}`
	anyItegRev         = `.+`
)

// bundled are the layouts Artifactory ships. Maven 2, Ivy and Gradle are
// quoted from JFrog's repository-layouts page; the rest reproduce the
// definitions from Artifactory's default configuration descriptor and are
// marked unverified where the page does not show them, so a mismatch found
// against a live instance is a one-line fix here.
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
		// unverified: Maven 1 is not shown on the layouts page.
		Name:                             "maven-1-default",
		ArtifactPathPattern:              "[org]/[type]s/[module]-[baseRev](-[fileItegRev])(-[classifier]).[ext]",
		DistinctiveDescriptorPathPattern: true,
		DescriptorPathPattern:            "[org]/[type]s/[module]-[baseRev](-[fileItegRev]).pom",
		FolderIntegrationRevisionRegExp:  anyItegRev,
		FileIntegrationRevisionRegExp:    anyItegRev,
	},
	{
		Name:                             "ivy-default",
		ArtifactPathPattern:              "[org]/[module]/[baseRev](-[folderItegRev])/[type]s/[module](-[classifier])-[baseRev](-[fileItegRev]).[ext]",
		DistinctiveDescriptorPathPattern: true,
		DescriptorPathPattern:            "[org]/[module]/[baseRev](-[folderItegRev])/[type]s/ivy-[baseRev](-[fileItegRev]).xml",
		FolderIntegrationRevisionRegExp:  timestampItegRev,
		FileIntegrationRevisionRegExp:    timestampItegRev,
	},
	{
		Name:                             "gradle-default",
		ArtifactPathPattern:              "[org]/[module]/[baseRev](-[folderItegRev])/[module]-[baseRev](-[fileItegRev])(-[classifier]).[ext]",
		DistinctiveDescriptorPathPattern: true,
		DescriptorPathPattern:            "[org]/[module]/ivy-[baseRev](-[fileItegRev]).xml",
		FolderIntegrationRevisionRegExp:  timestampItegRev,
		FileIntegrationRevisionRegExp:    timestampItegRev,
	},
	{
		// unverified: reproduced from the default descriptor.
		Name:                             "sbt-default",
		ArtifactPathPattern:              "[org]/[module]/(scala_[scalaVersion<.+>])/(sbt_[sbtVersion<.+>])/[baseRev]/[type]s/[module](-[classifier]).[ext]",
		DistinctiveDescriptorPathPattern: true,
		DescriptorPathPattern:            "[org]/[module]/(scala_[scalaVersion<.+>])/(sbt_[sbtVersion<.+>])/[baseRev]/[type]s/ivy.xml",
		FolderIntegrationRevisionRegExp:  timestampItegRev,
		FileIntegrationRevisionRegExp:    timestampItegRev,
	},
	{
		// unverified: reproduced from the default descriptor.
		Name:                            "sbt-ivy-default",
		ArtifactPathPattern:             "[org]/[module]/(scala_[scalaVersion<.+>])/(sbt_[sbtVersion<.+>])/[baseRev]/[type]s/[module](-[classifier]).[ext]",
		FolderIntegrationRevisionRegExp: timestampItegRev,
		FileIntegrationRevisionRegExp:   timestampItegRev,
	},
	{
		// unverified: reproduced from the default descriptor.
		Name:                            "simple-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[module]-[baseRev].[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// unverified: npm packages are stored as <name>/-/<name>-<version>.tgz
		// (scoped as @scope/<name>/-/...), which this pattern encodes with the
		// scope as an optional organisation.
		Name:                            "npm-default",
		ArtifactPathPattern:             "([orgPath]/)[module]/-/[module]-[baseRev].[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// unverified: reproduced from the default descriptor.
		Name:                            "nuget-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[module].[baseRev](-[fileItegRev]).nupkg",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// unverified: encodes the GOPROXY storage layout <module>/@v/<version>.<ext>.
		Name:                            "go-default",
		ArtifactPathPattern:             "[orgPath]/[module]/@v/[baseRev].[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// unverified: encodes <namespace>/<name>/<provider>/<version>.zip.
		Name:                            "terraform-module-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[provider<[^/]+>]/[baseRev].[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// unverified: encodes <namespace>/<type>/<version>/terraform-provider-<type>_<version>_<os>_<arch>.zip.
		Name:                            "terraform-provider-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[baseRev]/terraform-provider-[module]_[baseRev]_[os<[^_/]+>]_[arch<[^./]+>].[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// unverified: reproduced from the default descriptor.
		Name:                            "bower-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[module]-[baseRev].[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// unverified: reproduced from the default descriptor.
		Name:                            "composer-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[module]-[baseRev].[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// unverified: simplified from the default descriptor, which also
		// distinguishes export and package sub-folders.
		Name:                            "conan-default",
		ArtifactPathPattern:             "[org]/[module]/[baseRev]/[channel<[^/]+>]/[folderItegRev]/[module].[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// unverified: reproduced from the default descriptor.
		Name:                            "puppet-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[orgPath]-[module]-[baseRev].tar.gz",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// unverified: reproduced from the default descriptor.
		Name:                            "ansible-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[orgPath]-[module]-[baseRev].tar.gz",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// unverified: reproduced from the default descriptor.
		Name:                            "swift-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[module]-[baseRev].[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// unverified: reproduced from the default descriptor.
		Name:                            "nix-default",
		ArtifactPathPattern:             "[orgPath]/[module]/[module]-[baseRev].[ext]",
		FolderIntegrationRevisionRegExp: anyItegRev,
		FileIntegrationRevisionRegExp:   anyItegRev,
	},
	{
		// unverified: build-info JSON is stored as <build name>/<number>-<timestamp>.json.
		Name:                            "build-default",
		ArtifactPathPattern:             "[orgPath]/[module](-[fileItegRev]).[ext]",
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
