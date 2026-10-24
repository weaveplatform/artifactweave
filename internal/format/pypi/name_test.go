package pypi_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/pypi"
)

// PEP 503's normalisation, with the specification's examples: every
// spelling of one name is one project.
func TestUnitNormalize(t *testing.T) {
	for _, in := range []string{
		"friendly-bard", "Friendly-Bard", "FRIENDLY-BARD", "friendly.bard", "friendly_bard",
		"friendly--bard", "FrIeNdLy-._.-bArD",
	} {
		if got := pypi.Normalize(in); got != "friendly-bard" {
			t.Errorf("%q: %q", in, got)
		}
	}
}

// A valid project name is ASCII letters and digits, with ".", "-" and "_"
// inside it only.
func TestUnitValidName(t *testing.T) {
	for name, want := range map[string]bool{
		"a": true, "A1": true, "jfrog.pypi": true, "jfrog_pypi": true, "jfrog-pypi": true,
		"": false, "-a": false, "a-": false, ".a": false, "a b": false, "a/b": false,
		"naïve": false,
	} {
		if got := pypi.ValidName(name); got != want {
			t.Errorf("%q: %v", name, got)
		}
	}
}

// PEP 440's normal form, as the packaging library prints a Version, and a
// version PEP 440 cannot read kept as written.
func TestUnitNormalizeVersion(t *testing.T) {
	for in, want := range map[string]string{
		"1.0":              "1.0",
		"v1.0":             "1.0",
		" 1.0 ":            "1.0",
		"01.002.0003":      "1.2.3",
		"0!1.0":            "1.0",
		"1!2.0":            "1!2.0",
		"1.0-ALPHA.1":      "1.0a1",
		"1.0alpha":         "1.0a0",
		"1.0b2":            "1.0b2",
		"1.0beta2":         "1.0b2",
		"1.0c1":            "1.0rc1",
		"1.0pre1":          "1.0rc1",
		"1.0preview1":      "1.0rc1",
		"1.0-RC.01":        "1.0rc1",
		"1.0-1":            "1.0.post1",
		"1.0.post":         "1.0.post0",
		"1.0-r2":           "1.0.post2",
		"1.0rev3":          "1.0.post3",
		"1.0.dev":          "1.0.dev0",
		"1.0-dev-2":        "1.0.dev2",
		"1.0a1.post2.dev3": "1.0a1.post2.dev3",
		"1.0+Ubuntu-1_2":   "1.0+ubuntu.1.2",
		"2004d":            "2004d",
		"not a version":    "not a version",
	} {
		if got := pypi.NormalizeVersion(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// An upload's canonical path takes the normalised name as its folder and
// the version and file name as they are.
func TestUnitCanonicalPath(t *testing.T) {
	got := pypi.CanonicalPath(
		"Infra_PyPIPkg.Test",
		"0.0.1",
		"infra_pypipkg_test-0.0.1-py3-none-any.whl",
	)
	if got != "infra-pypipkg-test/0.0.1/infra_pypipkg_test-0.0.1-py3-none-any.whl" {
		t.Fatal(got)
	}
}

// The file names of the distribution types 7.x extracts metadata from.
func TestUnitIsDistribution(t *testing.T) {
	for p, want := range map[string]bool{
		"a/x-1.0-py3-none-any.whl": true, "x-1.0.tar.gz": true, "x-1.0.tgz": true,
		"x-1.0.tar.bz2": true, "x-1.0.tar": true, "x-1.0.zip": true, "x-1.0-py3.8.egg": true,
		"x-1.0.WHL": false, "x-1.0.whl.asc": false, "x-1.0.exe": false, ".whl": false,
		"a/.tar.gz": false, "x-1.0.metadata": false,
	} {
		if got := pypi.IsDistribution(p); got != want {
			t.Errorf("%q: %v", p, got)
		}
	}
}
