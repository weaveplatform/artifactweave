package repo_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// An AI Editor Extensions remote created without the external dependency
// fields takes them on, with the vsassets.io pattern, as 7.161.26's
// RemoteDefaultPackageConfigSetter gives them; what the client sends wins,
// and the pattern fills an empty list whether or not they are on. Its
// bypassHeadRequests is on whatever was sent, as PackageRepoConfigChange-
// Interceptor sets it. Swift, which 7.x's switch pairs with this type when
// reading the fields, gets no such default, nor does any other remote (#1511).
func TestUnitAIEditorExtensionsRemoteDefaults(t *testing.T) {
	vsassets := []string{"**/**vsassets.io/**"}
	for _, tc := range []struct {
		name     string
		in       repo.Config
		enabled  bool
		patterns []string
		bypass   bool
	}{
		{"bare", repo.Config{}, true, vsassets, true},
		{"off", repo.Config{ExternalDependenciesEnabled: new(false)}, false, vsassets, true},
		{"empty list", repo.Config{ExternalDependenciesPatterns: []string{}}, true, vsassets, true},
		{
			"own patterns",
			repo.Config{ExternalDependenciesPatterns: []string{"**/cdn.example/**"}},
			true,
			[]string{"**/cdn.example/**"},
			true,
		},
		{"bypass sent off", repo.Config{BypassHeadRequests: new(false)}, true, vsassets, true},
	} {
		c := tc.in
		c.Key, c.Class, c.PackageType = "ai", repo.Remote, repo.AIEditorExtensions
		repo.Defaults(&c)
		if *c.ExternalDependenciesEnabled != tc.enabled ||
			!slices.Equal(c.ExternalDependenciesPatterns, tc.patterns) ||
			*c.BypassHeadRequests != tc.bypass {
			t.Errorf(
				"%s: enabled %v patterns %q bypass %v",
				tc.name,
				*c.ExternalDependenciesEnabled,
				c.ExternalDependenciesPatterns,
				*c.BypassHeadRequests,
			)
		}
	}
	for _, typ := range []repo.PackageType{repo.Swift, repo.Docker, repo.Generic} {
		c := repo.Defaults(&repo.Config{Key: "r", Class: repo.Remote, PackageType: typ})
		if *c.ExternalDependenciesEnabled || c.ExternalDependenciesPatterns != nil ||
			*c.BypassHeadRequests {
			t.Errorf(
				"%s: enabled %v patterns %q bypass %v",
				typ,
				*c.ExternalDependenciesEnabled,
				c.ExternalDependenciesPatterns,
				*c.BypassHeadRequests,
			)
		}
	}
}

// An update may not turn an AI Editor Extensions remote's bypassHeadRequests
// from on to off, in 7.161.26's words; leaving it out, resetting it with
// null, or repeating true passes, as does any change on another type.
func TestUnitValidateBypassHead(t *testing.T) {
	ai := func(bypass *bool) *repo.Config {
		return &repo.Config{
			Key: "vscode", Class: repo.Remote, PackageType: repo.AIEditorExtensions,
			BypassHeadRequests: bypass,
		}
	}
	err := repo.ValidateBypassHead(ai(new(true)), ai(new(false)))
	var refused repo.BypassHeadError
	if !errors.As(err, &refused) || !errors.Is(err, repo.ErrInvalid) || err.Error() !=
		"Changing 'bypass Head Requests' is not allowed for VS Code remote repository: vscode" {
		t.Fatalf("turned off: %v", err)
	}
	off := &repo.Config{BypassHeadRequests: new(false)}
	for name, pair := range map[string][2]*repo.Config{
		"left out":   {ai(new(true)), ai(nil)},
		"repeated":   {ai(new(true)), ai(new(true))},
		"stored off": {ai(new(false)), ai(new(false))},
		"another type": {
			{Class: repo.Remote, PackageType: repo.Generic, BypassHeadRequests: new(true)}, off,
		},
		"another class": {
			{Class: repo.Local, PackageType: repo.AIEditorExtensions, BypassHeadRequests: new(true)}, off,
		},
	} {
		if err := repo.ValidateBypassHead(pair[0], pair[1]); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
