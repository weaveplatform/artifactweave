package repo_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// TestUnitDebianSettingsReads (#1372): a Debian repository of any class
// that never set ddebSupported or debianTrivialLayout reads them as false
// (Full, the v1 read), as 7.161.26's RepositoryConfigurationBase defaults
// them, RTFACT-30304 shows for a Debian remote and the Terraform provider's
// sample reads give, and keeps a value it set. The v2 read (Relevant)
// keeps ddebSupported for every Debian class and debianTrivialLayout for a
// local or federated one only, as that model's @IncludeTypeSpecific marks
// them. A repository of another package type reads both as false in v1,
// whatever was stored, as only a Debian repository's configuration sets
// them in 7.x (LocalRepositoryConfigurationImpl.setDebianConfigIfNeeded,
// 7.161.26) and the provider's sample reads of generic and RPM
// repositories give false (#1479), and not at all in v2.
func TestUnitDebianSettingsReads(t *testing.T) {
	read := func(b *bool) string {
		switch {
		case b == nil:
			return "unset"
		case *b:
			return "true"
		default:
			return "false"
		}
	}
	for _, tc := range []struct {
		c                 repo.Config
		ddeb, trivial     string
		ddebV2, trivialV2 string
	}{
		{repo.Config{Class: repo.Local}, "false", "false", "false", "false"},
		{repo.Config{Class: repo.Federated}, "false", "false", "false", "false"},
		{repo.Config{Class: repo.Remote}, "false", "false", "false", "unset"},
		{repo.Config{Class: repo.Virtual}, "false", "false", "false", "unset"},
		{
			repo.Config{Class: repo.Remote, DdebSupported: ptr(true), DebianTrivialLayout: ptr(true)},
			"true", "true", "true", "unset",
		},
		{
			repo.Config{Class: repo.Local, DebianTrivialLayout: ptr(true)},
			"false", "true", "false", "true",
		},
		{
			repo.Config{Class: repo.Remote, PackageType: repo.RPM, DdebSupported: ptr(true)},
			"false", "false", "unset", "unset",
		},
		{repo.Config{Class: repo.Local, PackageType: repo.Generic}, "false", "false", "unset", "unset"},
		{
			repo.Config{Class: repo.Virtual, PackageType: repo.Maven, DebianTrivialLayout: ptr(true)},
			"false", "false", "unset", "unset",
		},
	} {
		if tc.c.PackageType == "" {
			tc.c.PackageType = repo.Debian
		}
		v1 := repo.Full(tc.c)
		v2 := repo.Relevant(v1)
		if got := [4]string{
			read(v1.DdebSupported), read(v1.DebianTrivialLayout),
			read(v2.DdebSupported), read(v2.DebianTrivialLayout),
		}; got != [4]string{tc.ddeb, tc.trivial, tc.ddebV2, tc.trivialV2} {
			t.Errorf(
				"%s %s reads ddebSupported and debianTrivialLayout as %q in v1 and v2, want %q",
				tc.c.Class,
				tc.c.PackageType,
				got,
				[4]string{tc.ddeb, tc.trivial, tc.ddebV2, tc.trivialV2},
			)
		}
	}
}
