package repo

import (
	"slices"
	"strings"
	"unicode"
)

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
	MachineLearning:  "MachineLearning",
	Hex:              "Hex",
	Nix:              "Nix",
	// No source shows these two listed; they are spelled as the enumeration
	// spells its other multi-word names, such as HuggingFaceML [I].
	AIEditorExtensions: "AIEditorExtensions",
	JetBrainsPlugins:   "JetBrainsPlugins",
	// Named by the 7.x metrics capture research 03 cites. Capitalising it
	// would give "Releasebundles".
	ReleaseBundles: "ReleaseBundles",
	// The rolling format guides do not show GET /api/repositories output
	// for these types. Match JFrog's RepoType spelling convention until the
	// 7.161.32 capture in #216 checks it [I].
	LuaRocks:      "LuaRocks",
	NIMModel:      "NimModel",
	Skills:        "Skills",
	AgentPlugins:  "AgentPlugins",
	AgentPackages: "AgentPackages",
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

// PackageTypesNamed returns, sorted, the package types a listing's package
// type filter names when it compares without regard to case both the
// configured type and the name the listing shows, as Get Repositories
// does: "yum" names rpm, and "MAVEN" maven. It lets a store filter by a
// list of stored values in place of comparing each repository. Besides the
// types with a listing name it holds name folded to lower-case ASCII,
// which is the stored spelling of any type without one that equals name
// without regard to case: a configured type is always lower case
// (Validate checks it against AllowedPackageTypes).
func PackageTypesNamed(name string) []PackageType {
	var out []PackageType
	for t, listed := range packageTypeNames {
		if strings.EqualFold(string(t), name) || strings.EqualFold(listed, name) {
			out = append(out, t)
		}
	}
	if folded := foldASCII(name); strings.EqualFold(folded, name) {
		out = append(out, PackageType(folded))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// foldASCII maps each rune of s to the lower-case ASCII letter its case
// folding reaches, such as k for the Kelvin sign and s for the long s,
// which strings.ToLower leaves alone, and any other rune to its lower case.
func foldASCII(s string) string {
	return strings.Map(func(r rune) rune {
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if 'a' <= f && f <= 'z' {
				return f
			}
		}
		return unicode.ToLower(r)
	}, s)
}
