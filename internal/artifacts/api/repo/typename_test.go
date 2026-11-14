package repo

import (
	"slices"
	"strings"
	"testing"
)

// Every type a repository can declare, and the seeded build-info type, has a
// researched spelling rather than the capitalised fallback, which is wrong
// for rpm and would be wrong for any multi-word type. A type added to
// AllowedPackageTypes needs an entry in packageTypeNames and a row in
// research 03.
func TestUnitEveryAcceptedTypeHasAListingName(t *testing.T) {
	types := map[PackageType]bool{BuildInfo: true}
	for _, class := range []Class{Local, Remote, Virtual, Federated} {
		for _, pt := range AllowedPackageTypes(class) {
			types[pt] = true
		}
	}
	for pt := range types {
		if _, ok := packageTypeNames[pt]; !ok {
			t.Errorf("no listing name for %q", pt)
		}
	}
}

func TestUnitPackageTypeName(t *testing.T) {
	for in, want := range map[string]string{
		"generic":          "Generic",
		"GENERIC":          "Generic",
		"maven":            "Maven",
		"alpine":           "Alpine",
		"npm":              "Npm",
		"pypi":             "Pypi",
		"rpm":              "YUM",
		"helmoci":          "HelmOCI",
		"huggingfaceml":    "HuggingFaceML",
		"terraformbackend": "TerraformBackend",
		"buildinfo":        "BuildInfo",
		"gitlfs":           "GitLfs",
		"cran":             "CRAN",
		"machinelearning":  "MachineLearning",
		"hex":              "Hex",
		"nix":              "Nix",
		"jetbrainsplugins": "JetBrainsPlugins",
		"releasebundles":   "ReleaseBundles",
		"luarocks":         "LuaRocks",
		"nimmodel":         "NimModel",
		"skills":           "Skills",
		"agentplugins":     "AgentPlugins",
		"agentpackages":    "AgentPackages",
		"":                 "",
	} {
		if got := PackageTypeName(in); got != want {
			t.Errorf("PackageTypeName(%q) = %q, want %q", in, got, want)
		}
	}
}

// PackageTypesNamed names exactly the stored types the listing filter
// matched one by one: the configured type or its listing name, without
// regard to case, Unicode folding included.
func TestUnitPackageTypesNamed(t *testing.T) {
	stored := []PackageType{"", "made-up"}
	for known := range packageTypeNames {
		stored = append(stored, known)
	}
	for _, name := range []string{
		"", "yum", "YUM", "rpm", "Maven", "MAVEN", "maven", "docKer", "docKer",
		"ſwift", "gİtlfs", "made-up", "MADE-UP", "nothing", "GitLfs", "buildinfo",
	} {
		named := PackageTypesNamed(name)
		for _, pt := range stored {
			want := strings.EqualFold(string(pt), name) ||
				strings.EqualFold(PackageTypeName(string(pt)), name)
			if got := slices.Contains(named, pt); got != want {
				t.Errorf("PackageTypesNamed(%q) holds %q: %v, want %v", name, pt, got, want)
			}
		}
		if !slices.IsSorted(named) {
			t.Errorf("PackageTypesNamed(%q) = %q, not sorted", name, named)
		}
	}
	if got := PackageTypesNamed("yum"); !slices.Equal(got, []PackageType{"rpm", "yum"}) {
		t.Errorf(`PackageTypesNamed("yum") = %q`, got)
	}
}
