package support

import (
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/crypt"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// TerraformBackendRepos is the local backend fixture shared with the root
// cross-format guards.
func TerraformBackendRepos() []repo.Config {
	return []repo.Config{{Key: "tfbe-local", Class: repo.Local, PackageType: repo.TerraformBackend}}
}

// TerraformBackendKeys is the deterministic keyring for sealed fixture state.
func TerraformBackendKeys(t *testing.T) *crypt.Keyring {
	t.Helper()
	k, err := crypt.New([]byte(strings.Repeat("t", 32)))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// TerraformBackendRoutes lists the native remote-state API routes. Generic
// item and storage routes remain covered by the root cross-format guards.
func TerraformBackendRoutes() map[string]bool {
	const base = "/artifactory/api/terraform/remote/v2"
	paths := []string{
		"GET " + base + "/ping",
		"GET " + base + "/account/details",
		"GET " + base + "/organizations/{repo}",
		"GET " + base + "/organizations/{repo}/entitlement-set",
		"GET " + base + "/organizations/{repo}/workspaces",
		"POST " + base + "/organizations/{repo}/workspaces",
		"GET " + base + "/organizations/{repo}/workspaces/{name}",
		"DELETE " + base + "/organizations/{repo}/workspaces/{name}",
		"GET " + base + "/workspaces/{id}",
		"DELETE " + base + "/workspaces/{id}",
		"POST " + base + "/workspaces/{id}/actions/lock",
		"POST " + base + "/workspaces/{id}/actions/unlock",
		"POST " + base + "/workspaces/{id}/actions/force-unlock",
		"GET " + base + "/workspaces/{id}/current-state-version",
		"POST " + base + "/workspaces/{id}/state-versions",
		"GET " + base + "/state-versions",
		"GET " + base + "/state-versions/{id}",
		"GET " + base + "/state-versions/{id}/download",
		"PUT " + base + "/state-versions/{id}/content",
	}
	routes := make(map[string]bool, len(paths))
	for _, route := range paths {
		routes[route] = true
	}
	return routes
}
