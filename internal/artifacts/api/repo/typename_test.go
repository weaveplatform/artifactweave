package repo

import "testing"

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
