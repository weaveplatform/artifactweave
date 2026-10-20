package nuget_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Normalisation follows the cases NuGet's and JFrog's documentation give:
// Microsoft's "Normalized version numbers" (leading zeros, a fourth part
// of 0, build metadata) and JFrog's migration tool's table ("Understanding
// Normalized Versions"), with NuGet's reading of fewer parts and the
// pre-release label's case kept.
func TestNormalizeVersion(t *testing.T) {
	for in, want := range map[string]string{
		// Microsoft.
		"1.00":        "1.0.0",
		"1.01.1":      "1.1.1",
		"1.00.0.1":    "1.0.0.1",
		"1.0.0.0":     "1.0.0",
		"1.0.01.0":    "1.0.1",
		"1.0.7+r3456": "1.0.7",
		// JFrog's migration tool.
		"1.2.3.0":       "1.2.3",
		"1.2.3.1":       "1.2.3.1",
		"1.0":           "1.0.0",
		"01.02.03":      "1.2.3",
		"1.2.3+build.5": "1.2.3",
		// NuGet's reading: one part, labels with their case, metadata of
		// any parts, space around the whole.
		"1":                  "1.0.0",
		"2.0.0-RC.1+build.7": "2.0.0-RC.1",
		"1.0.0-Beta":         "1.0.0-Beta",
		"1.0.0-beta-1":       "1.0.0-beta-1",
		"1.0.0-0":            "1.0.0-0",
		"1.0.0-0a.1":         "1.0.0-0a.1",
		"1.0.0+01.x-y":       "1.0.0",
		" 3.0.0.4-pre ":      "3.0.0.4-pre",
		"1.0.0+meta-data":    "1.0.0",
		"2147483647.0.0":     "2147483647.0.0",
		"00000000000000001":  "1.0.0",
	} {
		got, ok := nuget.NormalizeVersion(in)
		if !ok || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, ok, want)
		}
	}
}

// What NuGet cannot parse is no version: empty parts or labels, a trailing
// "-" or "+", a fifth part, letters in the numbers, a leading zero in an
// all-digit label, characters outside letters, digits and hyphens, and a
// part past a 32-bit integer.
func TestInvalidVersions(t *testing.T) {
	for _, in := range []string{
		"", " ", "a", "v1.0.0", "1.0.0.0.0", "1..0", ".1", "1.", "-1.0", "1.0.0-", "1.0.0+",
		"1.0.0-beta+", "1.0.0-01", "1.0.0-a..b", "1.0.0-a_b", "1.0.0+a+b", "1.0.0+a..b",
		"1.0.x", "1. 0", "2147483648.0", "99999999999999999999.0", "1.0.0-beta!", "+build",
	} {
		if got, ok := nuget.NormalizeVersion(in); ok {
			t.Errorf("%q parsed as %q", in, got)
		}
	}
}

// A version is a pre-release when it has a label, and SemVer 2 when the
// label has more than one part or there is build metadata (Microsoft's
// "Semantic Versioning 2.0.0"). The original keeps what was written.
func TestVersionKinds(t *testing.T) {
	for _, c := range []struct {
		in              string
		prerelease, sv2 bool
		release         string
	}{
		{"1.0.0", false, false, "1.0.0"},
		{"1.0.0-beta", true, false, "1.0.0"},
		{"1.0.0-alpha.1", true, true, "1.0.0"},
		{"1.0.0+githash", false, true, "1.0.0"},
		{"1.0.0.3-rc", true, false, "1.0.0.3"},
	} {
		v, ok := nuget.ParseVersion(c.in)
		if !ok || v.IsPrerelease() != c.prerelease || v.IsSemVer2() != c.sv2 ||
			v.Release() != c.release || v.Original != c.in {
			t.Errorf("%q: %+v %v", c.in, v, ok)
		}
	}
}

// The index's version is the normalised one lower-cased, so versions NuGet
// takes for one are one, and it orders as NuGet orders.
func TestVersionKey(t *testing.T) {
	a, _ := nuget.ParseVersion("1.0.0-Beta")
	b, _ := nuget.ParseVersion("01.0.0.0-beta+x")
	if nuget.VersionKey(a) != "1.0.0-beta" || nuget.VersionKey(b) != nuget.VersionKey(a) {
		t.Fatalf("%q %q", nuget.VersionKey(a), nuget.VersionKey(b))
	}
	if types.CompareNuGet(nuget.VersionKey(a), a.Normalized()) != 0 ||
		types.CompareNuGet("1.0.0-rc.10", "1.0.0-rc.2") <= 0 {
		t.Fatal("the key orders apart from the version")
	}
	if nuget.PackageName("Newtonsoft.Json") != "newtonsoft.json" {
		t.Fatal(nuget.PackageName("Newtonsoft.Json"))
	}
	if got := nuget.FileName("Newtonsoft.Json", b); got != "Newtonsoft.Json.1.0.0-beta.nupkg" {
		t.Fatal(got)
	}
	if got := nuget.SymbolsFileName(
		"Newtonsoft.Json",
		a,
	); got != "Newtonsoft.Json.1.0.0-Beta.snupkg" {
		t.Fatal(got)
	}
}
