package repo_test

import (
	"errors"
	"strconv"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// A Terraform remote on the public Terraform, OpenTofu or Wiz registry has
// bypassHeadRequests on whatever was sent, on every save, as 7.161.26's
// PackageRepoConfigChangeInterceptor sets it; the default registry is the
// first. Another registry, the same one with a trailing slash, a local
// Terraform repository and another type keep what was sent (#1583).
func TestUnitTerraformRemoteBypassHeadDefaults(t *testing.T) {
	for _, tc := range []struct {
		registry string
		sent     *bool
		want     bool
	}{
		{"", nil, true},
		{"", new(false), true},
		{"https://registry.terraform.io", new(false), true},
		{"https://registry.opentofu.org", nil, true},
		{"https://tf.app.wiz.io", new(false), true},
		{"https://registry.terraform.io/", nil, false},
		{"https://tf.example", nil, false},
		{"https://tf.example", new(true), true},
	} {
		c := repo.Defaults(&repo.Config{
			Key: "tf", Class: repo.Remote, PackageType: repo.Terraform, URL: "https://up.example",
			TerraformRegistryURL: tc.registry, BypassHeadRequests: tc.sent,
		})
		if *c.BypassHeadRequests != tc.want {
			t.Errorf(
				"registry %q: bypassHeadRequests %v, want %v",
				tc.registry,
				*c.BypassHeadRequests,
				tc.want,
			)
		}
	}
	local := repo.Defaults(
		&repo.Config{Key: "tf-local", Class: repo.Local, PackageType: repo.Terraform},
	)
	if local.BypassHeadRequests != nil {
		t.Errorf("local: bypassHeadRequests %v", *local.BypassHeadRequests)
	}
	npm := repo.Defaults(&repo.Config{
		Key: "npm", Class: repo.Remote, PackageType: repo.NPM, URL: "https://up.example",
		TerraformRegistryURL: "https://registry.terraform.io",
	})
	if *npm.BypassHeadRequests {
		t.Error("npm remote took the Terraform rule")
	}
}

// An npm remote created on a URL that starts with https://npm.pkg.github.com
// gets bypassHeadRequests on whatever was sent, as 7.161.26's
// NpmGithubRegistrySettingsProviderPackage sets it; its test's cases decide
// which URLs match. Another URL, type or class keeps what was sent (#1583).
func TestUnitCreateDefaultsNPMGitHubRegistry(t *testing.T) {
	for _, tc := range []struct {
		class repo.Class
		pt    repo.PackageType
		url   string
		sent  *bool
		want  *bool
	}{
		{repo.Remote, repo.NPM, "https://npm.pkg.github.com", new(false), new(true)},
		{repo.Remote, repo.NPM, "https://npm.pkg.github.com/", nil, new(true)},
		{repo.Remote, repo.NPM, "https://npm.pkg.github.com/repo-name", nil, new(true)},
		{repo.Remote, repo.NPM, "https://dummy.npm.pkg.github.com/", new(false), new(false)},
		{repo.Remote, repo.NPM, "https://dummy.pkg.github.com/", nil, nil},
		{repo.Remote, repo.NPM, "https://registry.npmjs.org", nil, nil},
		{repo.Remote, repo.Generic, "https://npm.pkg.github.com", new(false), new(false)},
		{repo.Virtual, repo.NPM, "", nil, nil},
	} {
		c := repo.Config{
			Key: "r", Class: tc.class, PackageType: tc.pt, URL: tc.url, BypassHeadRequests: tc.sent,
		}
		repo.CreateDefaults(&c)
		show := func(b *bool) string {
			if b == nil {
				return "unset"
			}
			return strconv.FormatBool(*b)
		}
		if got, want := show(c.BypassHeadRequests), show(tc.want); got != want {
			t.Errorf("%s %s %q: bypassHeadRequests %s, want %s", tc.class, tc.pt, tc.url, got, want)
		}
	}
}

// A remote without a URL is refused with 7.x's message alone, on create and
// on update, whatever its package type: none has a default URL over REST.
// Among other faults it keeps its place in the list (#1584).
func TestUnitValidateNoURL(t *testing.T) {
	for _, pt := range []repo.PackageType{
		repo.Generic, repo.NIMModel, repo.AIEditorExtensions, repo.JetBrainsPlugins, repo.Terraform,
	} {
		c := repo.Defaults(&repo.Config{Key: "r", Class: repo.Remote, PackageType: pt})
		for name, validate := range map[string]func(*repo.Config) error{
			"Validate": repo.Validate, "ValidateStored": repo.ValidateStored,
		} {
			err := validate(c)
			var refused repo.NoURLError
			if !errors.As(err, &refused) || !errors.Is(err, repo.ErrInvalid) ||
				err.Error() != "No URL defined for remote repository" {
				t.Errorf("%s %s: %v", name, pt, err)
			}
		}
	}
	c := repo.Defaults(&repo.Config{Key: "bad!key", Class: repo.Remote, PackageType: repo.Generic})
	var errs repo.ValidationError
	if err := repo.Validate(c); !errors.As(err, &errs) || len(errs) != 2 ||
		errs[1] != (repo.FieldError{Field: "url", Message: repo.MsgNoURL}) {
		t.Errorf("with another fault: %v", err)
	}
}
