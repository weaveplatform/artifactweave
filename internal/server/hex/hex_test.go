package hex

import (
	"testing"

	hexfmt "github.com/weaveplatform/artifactweave/internal/format/hex"
)

func TestTarballPathGrammar(t *testing.T) {
	for _, tc := range []struct{ path, name, version string }{
		{"tarballs/decimal-2.0.0.tar", "decimal", "2.0.0"},
		{"tarballs/my-lib-1.0.0-rc.1.tar", "my-lib", "1.0.0-rc.1"},
		{"tarballs/../secret.tar", "", ""},
		{"tarballs/pkg.tar", "", ""},
	} {
		name, version, ok := splitTarball(tc.path)
		if ok != (tc.name != "") || name != tc.name || version != tc.version {
			t.Errorf("%s => %q %q %v", tc.path, name, version, ok)
		}
	}
}

func TestRebindReleasePreservesExternalDependencies(t *testing.T) {
	original := hexfmt.Package{Requirements: []hexfmt.Requirement{
		{Package: "telemetry", Repository: "hexpm"},
		{Package: "external", Repository: "other"},
		{Package: "implicit"},
	}}
	remote := rebindRelease(original, "hexpm", "hex-remote")
	virtual := rebindRelease(remote, "hex-remote", "hex-virtual")
	if got := virtual.Requirements[0].Repository; got != "hex-virtual" {
		t.Errorf("same-source dependency bypasses virtual repository: %q", got)
	}
	if got := virtual.Requirements[1].Repository; got != "other" {
		t.Errorf("explicit external dependency changed: %q", got)
	}
	if got := virtual.Requirements[2].Repository; got != "" {
		t.Errorf("implicit dependency changed: %q", got)
	}
	if original.Requirements[0].Repository != "hexpm" ||
		remote.Requirements[0].Repository != "hex-remote" {
		t.Fatal("rebind mutated a source registry view")
	}
}
