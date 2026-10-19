package repo

import "slices"

// packageTypes is every package type some class accepts, in the order
// AllowedPackageTypes returns them: JFrog's JSON documentation's list for a
// local repository, then the types that list leaves out.
var packageTypes = []PackageType{
	Alpine, Cargo, Composer, Bower, Chef, CocoaPods, Conan, Conda, Cran, Debian,
	Docker, Helm, HelmOCI, HuggingFaceML, Gems, GitLFS, Go, Gradle, Ivy, Maven,
	NPM, NuGet, OCI, Opkg, Pub, Puppet, PyPI, RPM, SBT, Swift, Terraform,
	TerraformBackend, Vagrant, Ansible, BazelModules, Generic,
	VCS, P2, Hex, Nix, MachineLearning, AIEditorExtensions, JetBrainsPlugins, ReleaseBundles,
}

// refused names, for each class, the package types no 7.x source offers for
// it. A type is allowed where any source offers it: JFrog's JSON
// documentation, the Terraform provider's default layout map, a release note
// or the type's own page (research 03, "Package types"). The sources lag
// each other, so their union is the set 7.x accepts: refusing a type 7.x
// accepts fails a client's create, while accepting one it refuses only
// stores a configuration that no package handler serves.
var refused = map[Class][]PackageType{
	Local:  {VCS, P2, AIEditorExtensions, JetBrainsPlugins},
	Remote: {TerraformBackend, Vagrant, MachineLearning, ReleaseBundles},
	Virtual: {
		Cargo, Vagrant, TerraformBackend, MachineLearning, VCS,
		AIEditorExtensions, JetBrainsPlugins, ReleaseBundles,
	},
	Federated: {TerraformBackend, VCS, P2, Hex, AIEditorExtensions, JetBrainsPlugins},
}

// AllowedPackageTypes returns the package types a repository of the given
// class may declare, or nil for an unknown class.
func AllowedPackageTypes(class Class) []PackageType {
	exclude, ok := refused[class]
	if !ok {
		return nil
	}
	out := make([]PackageType, 0, len(packageTypes))
	for _, t := range packageTypes {
		if !slices.Contains(exclude, t) {
			out = append(out, t)
		}
	}
	return out
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

// createLayout is the repoLayoutRef a create that names none gets, which
// Defaults applies on every create path, the descriptor's included. JFrog's
// sbt page says sbt-default "is automatically applied when creating a
// repository without specifying a layout", and its Gradle page that Gradle
// repositories use maven-2-default. Ivy gets ivy-default, as the Terraform
// provider's layout map gives it, by analogy with sbt [I]. Every other type
// gets maven-2-default, the JSON documentation's default and the layout of
// JFrog's own generic local sample (research 03, "Repository layouts").
func createLayout(packageType PackageType) string {
	switch packageType {
	case SBT:
		return LayoutSBT
	case Ivy:
		return LayoutIvy
	default:
		return LayoutMaven2
	}
}

// DefaultLayout returns the repoLayoutRef the JFrog Terraform provider assigns
// to a package type when the caller sets none (its default_repo_layout_map).
// The REST API gives a create without one createLayout's layout instead,
// which differs for most types. Nothing in artifactweave calls this outside
// the tests; it is exported for importers of pkg/repo that want the
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
	case Nix:
		return LayoutNix
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
