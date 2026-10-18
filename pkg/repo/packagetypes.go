package repo

import "slices"

// localPackageTypes is the full enumeration JFrog documents for a local
// repository; the other classes are derived by exclusion so the lists cannot
// drift apart.
var localPackageTypes = []PackageType{
	Alpine, Cargo, Composer, Bower, Chef, CocoaPods, Conan, Conda, Cran, Debian,
	Docker, Helm, HelmOCI, HuggingFaceML, Gems, GitLFS, Go, Gradle, Ivy, Maven,
	NPM, NuGet, OCI, Opkg, Pub, Puppet, PyPI, RPM, SBT, Swift, Terraform,
	TerraformBackend, Vagrant, Ansible, BazelModules, Generic,
}

// AllowedPackageTypes returns the package types a repository of the given
// class may declare. The exclusions come from JFrog's JSON documentation; the
// remote list additionally carries the YAML-only vcs and p2 types because the
// descriptor import feeds the same validation.
func AllowedPackageTypes(class Class) []PackageType {
	var exclude []PackageType
	var extra []PackageType
	switch class {
	case Local:
	case Remote:
		exclude = []PackageType{TerraformBackend, Vagrant, BazelModules}
		extra = []PackageType{VCS, P2}
	case Virtual:
		exclude = []PackageType{Cargo, CocoaPods, Opkg, Vagrant, TerraformBackend}
	case Federated:
		exclude = []PackageType{TerraformBackend, Swift}
	default:
		return nil
	}
	out := make([]PackageType, 0, len(localPackageTypes)+len(extra))
	for _, t := range localPackageTypes {
		if !slices.Contains(exclude, t) {
			out = append(out, t)
		}
	}
	return append(out, extra...)
}

// Layout identifiers bundled with Artifactory.
const (
	LayoutMaven2            = "maven-2-default"
	LayoutMaven1            = "maven-1-default"
	LayoutIvy               = "ivy-default"
	LayoutGradle            = "gradle-default"
	LayoutSBT               = "sbt-default"
	LayoutSBTIvy            = "sbt-ivy-default"
	LayoutSimple            = "simple-default"
	LayoutNPM               = "npm-default"
	LayoutNuGet             = "nuget-default"
	LayoutGo                = "go-default"
	LayoutTerraformModule   = "terraform-module-default"
	LayoutTerraformProvider = "terraform-provider-default"
	LayoutCargo             = "cargo-default"
	LayoutBower             = "bower-default"
	LayoutComposer          = "composer-default"
	LayoutConan             = "conan-default"
	LayoutPuppet            = "puppet-default"
	LayoutAnsible           = "ansible-default"
	LayoutSwift             = "swift-default"
	LayoutNix               = "nix-default"
	LayoutBuild             = "build-default"
)

// DefaultLayout returns the repoLayoutRef the JFrog Terraform provider assigns
// to a package type when the caller sets none. The REST API itself falls back
// to maven-2-default for every type when the field is omitted, which Defaults
// reproduces. Nothing in artifactweave calls this outside the tests: every
// create path, the descriptor's included, goes through Defaults and stores
// maven-2-default. It is exported for importers of pkg/repo that want the
// ecosystem-appropriate layout.
func DefaultLayout(packageType PackageType, terraformType TerraformType) string {
	switch packageType {
	case Maven, Gradle:
		return LayoutMaven2
	case Ivy:
		return LayoutIvy
	case SBT:
		return LayoutSBT
	case NPM:
		return LayoutNPM
	case NuGet:
		return LayoutNuGet
	case Go:
		return LayoutGo
	case Bower:
		return LayoutBower
	case Composer:
		return LayoutComposer
	case Conan:
		return LayoutConan
	case Puppet:
		return LayoutPuppet
	case Ansible:
		return LayoutAnsible
	case Swift:
		return LayoutSwift
	case Terraform:
		switch terraformType {
		case TerraformModule:
			return LayoutTerraformModule
		case TerraformProvider:
			return LayoutTerraformProvider
		}
		return LayoutSimple
	default:
		return LayoutSimple
	}
}
