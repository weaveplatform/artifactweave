package repo

import "strings"

// packageTypeNames holds the spelling a listing shows for each package type:
// the name of the package type in Artifactory's RepoType enumeration, not the
// lower-case value a configuration carries. The two differ by more than case
// for rpm, whose enumeration name is YUM, so the table cannot be replaced by
// capitalising the type. The source of each spelling is in research 03,
// "Package type names in listings".
var packageTypeNames = map[PackageType]string{
	Alpine:           "Alpine",
	Ansible:          "Ansible",
	BazelModules:     "BazelModules",
	Bower:            "Bower",
	BuildInfo:        "BuildInfo",
	Cargo:            "Cargo",
	Chef:             "Chef",
	CocoaPods:        "CocoaPods",
	Composer:         "Composer",
	Conan:            "Conan",
	Conda:            "Conda",
	Cran:             "CRAN",
	Debian:           "Debian",
	Docker:           "Docker",
	Gems:             "Gems",
	Generic:          "Generic",
	GitLFS:           "GitLfs",
	Go:               "Go",
	Gradle:           "Gradle",
	Helm:             "Helm",
	HelmOCI:          "HelmOCI",
	HuggingFaceML:    "HuggingFaceML",
	Ivy:              "Ivy",
	Maven:            "Maven",
	NPM:              "Npm",
	NuGet:            "NuGet",
	OCI:              "OCI",
	Opkg:             "Opkg",
	P2:               "P2",
	Pub:              "Pub",
	Puppet:           "Puppet",
	PyPI:             "Pypi",
	RPM:              "YUM",
	SBT:              "SBT",
	Swift:            "Swift",
	Terraform:        "Terraform",
	TerraformBackend: "TerraformBackend",
	Vagrant:          "Vagrant",
	VCS:              "VCS",
	// Not in AllowedPackageTypes, but named by the 7.x metrics capture
	// research 03 cites. Capitalising them would give "Machinelearning", so
	// they are spelled out for the day a repository can declare them.
	"machinelearning": "MachineLearning",
	"releasebundles":  "ReleaseBundles",
}

// PackageTypeName returns the spelling of packageType that Artifactory 7.x
// listings show, such as "Generic", "Npm" or "YUM" for rpm. The lookup
// ignores case. A type with no entry gets its first letter capitalised,
// which is how most enumeration names are formed, and an empty type stays
// empty.
func PackageTypeName(packageType string) string {
	t := strings.ToLower(packageType)
	if name, ok := packageTypeNames[PackageType(t)]; ok {
		return name
	}
	if t == "" {
		return ""
	}
	return strings.ToUpper(t[:1]) + t[1:]
}
