package repo

import "testing"

func TestPackageTypeDisplayName(t *testing.T) {
	for in, want := range map[string]string{
		// The two the storage summary spells differently from the list.
		"npm": "npm",
		"NPM": "npm",
		"rpm": "RPM",
		// The rest as the list spells them, including every type
		// jfrog-cli-core's transfer-files looks for.
		"generic":   "Generic",
		"maven":     "Maven",
		"gradle":    "Gradle",
		"ivy":       "Ivy",
		"sbt":       "SBT",
		"docker":    "Docker",
		"conan":     "Conan",
		"nuget":     "NuGet",
		"pypi":      "Pypi",
		"buildinfo": "BuildInfo",
		"cran":      "CRAN",
		"helmoci":   "HelmOCI",
		"hex":       "Hex",
		"":          "",
	} {
		if got := PackageTypeDisplayName(in); got != want {
			t.Errorf("PackageTypeDisplayName(%q) = %q, want %q", in, got, want)
		}
	}
}
