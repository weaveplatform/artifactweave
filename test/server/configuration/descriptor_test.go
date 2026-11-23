package configuration_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

const configPath = "/artifactory/api/system/configuration"

func TestAccPatchConfiguration(t *testing.T) {
	w := newRepoWorld(t)
	w.create(repo.Config{Key: "old-local", Class: repo.Local})
	patch := `
localRepositories:
  yaml-local:
    type: generic
    repoLayout: simple-default
    xray:
      enabled: true
  old-local: null
virtualRepositories:
  yaml-virtual:
    type: generic
    repositories: [yaml-local]
`
	// Artifactory's answer counts the document's six values.
	const answer = "6 changes to config merged successfully"
	rec := w.do(http.MethodPatch, configPath, "admin", patch, "Content-Type", "application/yaml")
	if rec.Code != http.StatusOK ||
		rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" ||
		rec.Body.String() != answer {
		t.Fatalf("PATCH = %d %s: %q", rec.Code, rec.Header(), rec.Body)
	}
	if c := w.stored(
		"yaml-local",
	); c.RepoLayoutRef != "simple-default" || c.XrayIndex == nil ||
		!*c.XrayIndex {
		t.Fatalf("stored %+v", c)
	}
	if _, err := w.db.Repos().Get(context.Background(), "old-local"); err == nil {
		t.Fatal("old-local was not deleted")
	}
	// The same patch again changes nothing, and answers the same count.
	before := w.stored("yaml-virtual")
	rec = w.do(http.MethodPatch, configPath, "admin", patch, "Content-Type", "application/yaml")
	if rec.Code != http.StatusOK || rec.Body.String() != answer {
		t.Fatalf("second PATCH = %d: %q", rec.Code, rec.Body)
	}
	if after := w.stored("yaml-virtual"); strings.Join(after.Repositories, ",") !=
		strings.Join(before.Repositories, ",") {
		t.Fatalf("second PATCH changed %+v", after)
	}
}

// A remote the YAML configuration creates or updates with a user name and
// password in its URL keeps them as its username and password, as every
// other write does, and the same patch again changes nothing (#1510).
func TestAccPatchConfigurationMovesURLCredentials(t *testing.T) {
	w := newRepoWorld(t)
	w.create(repo.Config{Key: "old-remote", Class: repo.Remote, URL: "https://up.example/old"})
	patch := `
remoteRepositories:
  yaml-remote:
    type: generic
    url: https://u:p@up.example/yaml
  old-remote:
    url: https://v:q@up.example/old?x=1
`
	want := map[string][3]string{
		"yaml-remote": {"https://up.example/yaml", "u", "p"},
		"old-remote":  {"https://up.example/old", "v", "q"},
	}
	for _, round := range []string{"first", "second"} {
		rec := w.do(
			http.MethodPatch,
			configPath,
			"admin",
			patch,
			"Content-Type",
			"application/yaml",
		)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s PATCH = %d: %q", round, rec.Code, rec.Body)
		}
		for key, want := range want {
			if c := w.stored(key); [3]string{c.URL, c.Username, c.Password} != want {
				t.Errorf("%s PATCH stored %s %s %q %q", round, key, c.URL, c.Username, c.Password)
			}
		}
	}
}

// The YAML configuration gives a Terraform remote on the default registry
// bypassHeadRequests on at every create and update, whatever it sends, and
// an npm remote on https://npm.pkg.github.com on create only, as 7.x's
// interceptors do on the repository saves its YAML patch makes (#1583). A
// remote without a URL is refused with 7.x's message, named by its entry
// (#1584).
func TestAccPatchConfigurationRemoteDefaults(t *testing.T) {
	w := newRepoWorld(t)
	for _, step := range []struct {
		patch, key string
		want       bool
	}{
		{"y-tf:\n    type: terraform\n    url: https://tf-up.example\n    bypassHeadRequests: false\n", "y-tf", true},
		{"y-tf:\n    bypassHeadRequests: false\n", "y-tf", true},
		{"y-tf-other:\n    type: terraform\n    url: https://tf-up.example\n" +
			"    terraformRegistryUrl: https://tf.example\n", "y-tf-other", false},
		{"y-npm:\n    type: npm\n    url: https://npm.pkg.github.com\n", "y-npm", true},
		{"y-npm:\n    bypassHeadRequests: false\n", "y-npm", false},
	} {
		rec := w.do(http.MethodPatch, configPath, "admin", "remoteRepositories:\n  "+step.patch,
			"Content-Type", "application/yaml")
		if rec.Code != http.StatusOK {
			t.Fatalf("PATCH %q = %d: %s", step.patch, rec.Code, rec.Body)
		}
		got := w.stored(step.key).BypassHeadRequests
		if got == nil {
			t.Fatalf("PATCH %q: bypassHeadRequests unset", step.patch)
		}
		if *got != step.want {
			t.Errorf("PATCH %q: bypassHeadRequests %t, want %t", step.patch, *got, step.want)
		}
	}
	for key, patch := range map[string]string{
		"y-nourl": "y-nourl:\n    type: generic\n",
		"y-nim":   "y-nim:\n    type: nimmodel\n",
		"y-npm":   "y-npm:\n    url: null\n",
	} {
		rec := w.do(http.MethodPatch, configPath, "admin", "remoteRepositories:\n  "+patch,
			"Content-Type", "application/yaml")
		if rec.Code != http.StatusBadRequest || strings.TrimSpace(rec.Body.String()) !=
			`{"errors":[{"message":"descriptor: invalid configuration descriptor: remoteRepositories.`+key+
				`: No URL defined for remote repository","status":400}]}` {
			t.Errorf("PATCH %q = %d: %s", patch, rec.Code, rec.Body)
		}
	}
}

func TestAccPatchConfigurationErrors(t *testing.T) {
	w := newRepoWorld(t)
	for name, c := range map[string]struct {
		as, body string
		fail     string
		expected int
		message  string
	}{
		"no credentials": {as: "", body: "localRepositories: {}", expected: 401},
		"not an admin":   {as: "reader", body: "localRepositories: {}", expected: 403},
		"empty":          {as: "admin", body: "", expected: 400, message: "the document is empty"},
		"not yaml":       {as: "admin", body: "[unclosed", expected: 400, message: "not a yaml mapping"},
		"protected": {
			as: "admin", body: "localRepositories:\n  artifactory-build-info: null\n",
			expected: 400, message: "cannot be deleted",
		},
		"layout names another": {
			as: "admin", body: "repoLayouts:\n  mine:\n    name: theirs\n    artifactPathPattern: x\n",
			expected: 400, message: "repoLayouts.mine: name theirs does not match the entry key",
		},
		"unapplied sections": {
			as: "admin", body: "security:\n  anonAccessEnabled: true\nsumoLogicConfig:\n  enabled: true\n",
			expected: 400, message: "sections not supported: sumoLogicConfig",
		},
		"null on a boolean": {
			as: "admin", body: "localRepositories:\n  x-local:\n    blackedOut: null\n",
			expected: 400, message: "localRepositories.x-local.blackedOut cannot be null",
		},
		"store fails": {
			as: "admin", body: "localRepositories:\n  x-local: {type: generic}\n",
			fail: "list", expected: 500, message: "internal error",
		},
	} {
		clear(w.fail)
		if c.fail != "" {
			w.fail[c.fail] = true
		}
		rec := w.do(http.MethodPatch, configPath, c.as, c.body, "Content-Type", "application/yaml")
		if rec.Code != c.expected || !strings.Contains(rec.Body.String(), c.message) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestUnitPatchConfigurationAnonymousAccess(t *testing.T) {
	w := newRepoWorld(t)
	read := func() bool {
		t.Helper()
		var enabled bool
		if err := w.db.Settings().
			Get(context.Background(), settings.SettingAnonymousAccess, &enabled); err != nil {
			t.Fatal(err)
		}
		return enabled
	}
	patch := "security:\n  anonAccessEnabled: true\n"
	for _, as := range []string{"", "reader"} {
		rec := w.do(http.MethodPatch, configPath, as, patch, "Content-Type", "application/yaml")
		if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
			t.Fatalf("%s PATCH = %d: %s", as, rec.Code, rec.Body)
		}
	}
	rec := w.do(http.MethodPatch, configPath, "admin", patch, "Content-Type", "application/yaml")
	if rec.Code != http.StatusOK || !read() {
		t.Fatalf("admin PATCH = %d %s; enabled=%t", rec.Code, rec.Body, read())
	}
	for _, body := range []string{
		"security:\n  anonAccessEnabled: yes\n",
		"security:\n  anonAccessEnabled: null\n",
		"security:\n  anonymousRole: admin\n",
		"security:\n  anonAccessEnabled: false\nlocalRepositories:\n  artifactory-build-info: null\n",
	} {
		rec = w.do(http.MethodPatch, configPath, "admin", body, "Content-Type", "application/yaml")
		if rec.Code != http.StatusBadRequest || !read() {
			t.Fatalf("invalid PATCH %q = %d %s; enabled=%t", body, rec.Code, rec.Body, read())
		}
	}
	rec = w.do(http.MethodPatch, configPath, "admin",
		"security:\n  anonAccessEnabled: false\n", "Content-Type", "application/yaml")
	if rec.Code != http.StatusOK || read() {
		t.Fatalf("disable PATCH = %d %s; enabled=%t", rec.Code, rec.Body, read())
	}
}
