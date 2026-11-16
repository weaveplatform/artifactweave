package artifacts_test

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// virtualConfigWorld seeds the repositories the virtual save rules are
// checked against. stale and misconfigured are stored as a client could no
// longer save them, straight into the store: stale names a member and a
// deployment repository that no longer exist, and misconfigured deploys to
// a repository it does not list.
func virtualConfigWorld(t *testing.T) *repoWorld {
	t.Helper()
	w := newRepoWorld(t)
	w.create(
		repo.Config{Key: "npm-local", Class: repo.Local, PackageType: repo.NPM},
		repo.Config{Key: "npm-other", Class: repo.Local, PackageType: repo.NPM},
		repo.Config{
			Key: "npm-remote", Class: repo.Remote, PackageType: repo.NPM,
			URL: "https://registry.npmjs.org",
		},
		repo.Config{Key: "npm-fed", Class: repo.Federated, PackageType: repo.NPM},
		repo.Config{Key: "npm-virtual", Class: repo.Virtual, PackageType: repo.NPM},
		repo.Config{Key: "maven-local", Class: repo.Local, PackageType: repo.Maven},
		repo.Config{
			Key: "maven-remote", Class: repo.Remote, PackageType: repo.Maven,
			URL: "https://repo1.maven.org/maven2",
		},
		repo.Config{Key: "gradle-local", Class: repo.Local, PackageType: repo.Gradle},
		repo.Config{Key: "generic-local", Class: repo.Local, PackageType: repo.Generic},
		repo.Config{Key: "rpm-local", Class: repo.Local, PackageType: repo.RPM},
		repo.Config{Key: "oci-local", Class: repo.Local, PackageType: repo.OCI},
		repo.Config{Key: "helmoci-local", Class: repo.Local, PackageType: repo.HelmOCI},
		repo.Config{Key: "helm-local", Class: repo.Local, PackageType: repo.Helm},
		repo.Config{Key: "docker-local", Class: repo.Local, PackageType: repo.Docker},
		repo.Config{
			Key: "npm-deploy", Class: repo.Virtual, PackageType: repo.NPM,
			Repositories: []string{"npm-local", "npm-other"}, DefaultDeploymentRepo: "npm-local",
		},
		repo.Config{
			Key: "stale", Class: repo.Virtual, PackageType: repo.NPM,
			Repositories: []string{"npm-local", "gone"}, DefaultDeploymentRepo: "gone",
		},
		repo.Config{
			Key: "misconfigured", Class: repo.Virtual, PackageType: repo.NPM,
			Repositories: []string{"npm-other"}, DefaultDeploymentRepo: "npm-local",
		},
		repo.Config{Key: "yaml-gone", Class: repo.Local, PackageType: repo.NPM},
		repo.Config{
			Key: "yaml-v", Class: repo.Virtual, PackageType: repo.NPM,
			Repositories: []string{"npm-local", "yaml-gone"}, DefaultDeploymentRepo: "yaml-gone",
		},
	)
	return w
}

// apiMessage is the message of a 400 in Artifactory's error envelope.
func apiMessage(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Errors []struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Errors) != 1 {
		return rec.Body.String()
	}
	return body.Errors[0].Message
}

// TestAccVirtualSaveRules checks the rules 7.161.26's configuration
// interceptors apply when a virtual repository is saved
// (RepoConfigReferencesInterceptor, DefaultDeploymentRepoInterceptor,
// VirtualRepoConfigChangeInterceptor), with their messages, on the single
// create and update, both batch operations and the YAML configuration
// patch. The rows run in order: later rows see what earlier ones saved.
func TestAccVirtualSaveRules(t *testing.T) {
	w := virtualConfigWorld(t)
	const (
		r     = "/artifactory/api/repositories/"
		batch = "/artifactory/api/v2/repositories/batch"
		yaml  = "/artifactory/api/system/configuration"
	)
	missing := func(key, ref string) string {
		return "Cannot create or update repo '" + key + "': repo reference '" + ref +
			"' does not exist in the config descriptor."
	}
	notIncluded := func(ddr, key string) string {
		return "Cannot set repository reference '" + ddr + "' as the default deployment " +
			"repository for virtual repository '" + key + "' since it is not included in " +
			"the repository references"
	}
	notLocal := func(ddr, key string) string {
		return "Cannot set repository reference '" + ddr + "' as the default deployment " +
			"repository for virtual repository '" + key + "' since it is not a local repository"
	}
	mismatch := func(key, vt, member, mt string) string {
		return "Virtual repository '" + key + "' has package type '" + vt +
			"' but includes non-matching repository '" + member + "' with package type '" + mt + "'"
	}
	for _, tt := range []struct {
		name, method, path, body string
		status                   int
		// message is the 400's whole message, or for the YAML patch, which
		// names the entry before it, its end.
		message string
	}{
		// References that do not exist.
		{
			"create, missing member", "PUT", r + "v-ghost",
			`{"rclass":"virtual","packageType":"npm","repositories":["npm-local","ghost"]}`,
			400, missing("v-ghost", "ghost"),
		},
		{
			"create, missing deployment repository", "PUT", r + "v-ghost-ddr",
			`{"rclass":"virtual","packageType":"npm","repositories":["npm-local"],` +
				`"defaultDeploymentRepo":"ghost"}`,
			400, missing("v-ghost-ddr", "ghost"),
		},
		{
			"update, new missing member", "POST", r + "npm-virtual",
			`{"repositories":["ghost"]}`, 400, missing("npm-virtual", "ghost"),
		},
		{
			"update, new missing deployment repository", "POST", r + "npm-deploy",
			`{"defaultDeploymentRepo":"ghost"}`, 400, missing("npm-deploy", "ghost"),
		},
		// A key saved by the same request exists, so a virtual repository
		// may list itself; resolution skips it.
		{"create, itself", "PUT", r + "self", `{"rclass":"virtual","repositories":["self"]}`, 200, ""},
		{"read through itself", "GET", "/artifactory/self/a.txt", "", 404, ""},
		{
			"create, itself and a member", "PUT", r + "npm-self",
			`{"rclass":"virtual","packageType":"npm","repositories":["npm-self","npm-local"]}`,
			200, "",
		},
		// A cycle is saved; resolution skips the repeated virtual.
		{"cycle, b", "PUT", r + "cyc-b", `{"rclass":"virtual"}`, 200, ""},
		{"cycle, a over b", "PUT", r + "cyc-a", `{"rclass":"virtual","repositories":["cyc-b"]}`, 200, ""},
		{"cycle, b over a", "POST", r + "cyc-b", `{"repositories":["cyc-a"]}`, 200, ""},
		{"read through the cycle", "GET", "/artifactory/cyc-a/a.txt", "", 404, ""},
		// The deployment repository must be one of the members when it is set.
		{
			"create, deployment repository outside the members", "PUT", r + "v-out",
			`{"rclass":"virtual","packageType":"npm","repositories":["npm-other"],` +
				`"defaultDeploymentRepo":"npm-local"}`,
			400, notIncluded("npm-local", "v-out"),
		},
		{
			"create, deployment repository without members", "PUT", r + "v-none",
			`{"rclass":"virtual","defaultDeploymentRepo":"npm-local"}`,
			400, notIncluded("npm-local", "v-none"),
		},
		{
			"update, deployment repository changed outside the members", "POST", r + "npm-deploy",
			`{"defaultDeploymentRepo":"npm-fed"}`, 400, notIncluded("npm-fed", "npm-deploy"),
		},
		{
			"update, the deployment repository's member removed", "POST", r + "npm-deploy",
			`{"repositories":["npm-other"]}`,
			400, "Cannot remove repository reference 'npm-local' since it is defined as the " +
				"default deployment repository for the virtual repository 'npm-deploy'",
		},
		{
			"update touching neither, deployment repository outside the members", "POST",
			r + "misconfigured", `{"description":"unrelated"}`, 200, "",
		},
		{
			"update, another member added", "POST", r + "misconfigured",
			`{"repositories":["npm-other","npm-remote"]}`, 200, "",
		},
		// A stored reference that no longer exists is dropped, not refused.
		{"update of stale, description only", "POST", r + "stale", `{"description":"kept"}`, 200, ""},
		// The deployment repository is local or federated.
		{
			"create, remote deployment repository", "PUT", r + "v-remote-ddr",
			`{"rclass":"virtual","packageType":"npm","repositories":["npm-local","npm-remote"],` +
				`"defaultDeploymentRepo":"npm-remote"}`,
			400, notLocal("npm-remote", "v-remote-ddr"),
		},
		{
			"create, virtual deployment repository", "PUT", r + "v-virtual-ddr",
			`{"rclass":"virtual","packageType":"npm","repositories":["npm-virtual"],` +
				`"defaultDeploymentRepo":"npm-virtual"}`,
			400, notLocal("npm-virtual", "v-virtual-ddr"),
		},
		{
			"create, itself as deployment repository", "PUT", r + "v-self-ddr",
			`{"rclass":"virtual","repositories":["v-self-ddr"],"defaultDeploymentRepo":"v-self-ddr"}`,
			400, notLocal("v-self-ddr", "v-self-ddr"),
		},
		{
			"create, federated deployment repository", "PUT", r + "v-fed-ddr",
			`{"rclass":"virtual","packageType":"npm","repositories":["npm-fed","npm-remote"],` +
				`"defaultDeploymentRepo":"npm-fed"}`,
			200, "",
		},
		{
			"create, local deployment repository", "PUT", r + "v-local-ddr",
			`{"rclass":"virtual","packageType":"npm","repositories":["npm-remote","npm-local"],` +
				`"defaultDeploymentRepo":"npm-local"}`,
			200, "",
		},
		// Package types, named as 7.x's RepoType names them.
		{
			"create, member of another type", "PUT", r + "mixed",
			`{"rclass":"virtual","packageType":"npm","repositories":["npm-local","maven-local"]}`,
			400, mismatch("mixed", "Npm", "maven-local", "Maven"),
		},
		{
			"create, rpm virtual over npm", "PUT", r + "rpm-mixed",
			`{"rclass":"virtual","packageType":"rpm","repositories":["rpm-local","npm-local"]}`,
			400, mismatch("rpm-mixed", "YUM", "npm-local", "Npm"),
		},
		{
			"update, member of another type", "POST", r + "npm-virtual",
			`{"repositories":["maven-local"]}`, 400, mismatch("npm-virtual", "Npm", "maven-local", "Maven"),
		},
		{
			"create, oci over helm oci", "PUT", r + "oci-virtual",
			`{"rclass":"virtual","packageType":"oci","repositories":["oci-local","helmoci-local"]}`,
			200, "",
		},
		{
			"create, helm oci over oci", "PUT", r + "helmoci-virtual",
			`{"rclass":"virtual","packageType":"helmoci","repositories":["helmoci-local","oci-local"]}`,
			200, "",
		},
		{
			"create, helm over helm oci", "PUT", r + "helm-mixed",
			`{"rclass":"virtual","packageType":"helm","repositories":["helmoci-local"]}`,
			400, mismatch("helm-mixed", "Helm", "helmoci-local", "HelmOCI"),
		},
		{
			"create, oci over docker", "PUT", r + "oci-docker",
			`{"rclass":"virtual","packageType":"oci","repositories":["docker-local"]}`,
			400, mismatch("oci-docker", "OCI", "docker-local", "Docker"),
		},
		{
			"create, maven family", "PUT", r + "maven-all",
			`{"rclass":"virtual","packageType":"maven","repositories":["maven-local","gradle-local"]}`,
			200, "",
		},
		{
			"create, generic over any", "PUT", r + "generic-all",
			`{"rclass":"virtual","repositories":["npm-local","maven-local","oci-local"]}`, 200, "",
		},
		// A P2 virtual repository aggregates local Maven family and Generic
		// repositories as JFrog's P2 page says, and every member of another
		// type than P2 must be local.
		{
			"create, p2 over gradle", "PUT", r + "p2-gradle",
			`{"rclass":"virtual","packageType":"p2","repositories":["gradle-local"]}`, 200, "",
		},
		{
			"create, p2 over generic", "PUT", r + "p2-generic",
			`{"rclass":"virtual","packageType":"p2","repositories":["generic-local"]}`, 200, "",
		},
		{
			"create, p2 over a remote maven", "PUT", r + "p2-remote",
			`{"rclass":"virtual","packageType":"p2","repositories":["maven-remote"]}`,
			400, "P2 virtual repository member 'maven-remote' with package type 'maven' must be local",
		},
		{
			"create, p2 over npm", "PUT", r + "p2-npm",
			`{"rclass":"virtual","packageType":"p2","repositories":["npm-local"]}`,
			400, mismatch("p2-npm", "P2", "npm-local", "Npm"),
		},
		// The batch operations.
		{
			"batch create, missing member", "PUT", batch,
			`[{"key":"b-ghost","rclass":"virtual","packageType":"npm","repositories":["ghost"]}]`,
			400, missing("b-ghost", "ghost"),
		},
		{
			"batch create, itself and a member of the batch", "PUT", batch,
			`[{"key":"b-npm","rclass":"local","packageType":"npm"},` +
				`{"key":"b-virt","rclass":"virtual","packageType":"npm","repositories":["b-virt","b-npm"],` +
				`"defaultDeploymentRepo":"b-npm"}]`,
			201, "",
		},
		{
			"batch update, the deployment repository's member removed", "POST", batch,
			`[{"key":"npm-deploy","repositories":["npm-other"]}]`,
			400, "Cannot remove repository reference 'npm-local' since it is defined as the " +
				"default deployment repository for the virtual repository 'npm-deploy'",
		},
		{
			"batch update, member of another type", "POST", batch,
			`[{"key":"npm-virtual","repositories":["maven-local"]}]`,
			400, mismatch("npm-virtual", "Npm", "maven-local", "Maven"),
		},
		// The YAML configuration patch.
		{
			"yaml, missing member", "PATCH", yaml,
			"virtualRepositories:\n  y-ghost:\n    type: npm\n    repositories: [ghost]\n",
			400, missing("y-ghost", "ghost"),
		},
		{
			"yaml, deployment repository outside the members", "PATCH", yaml,
			"virtualRepositories:\n  y-out:\n    type: npm\n    repositories: [npm-other]\n" +
				"    defaultDeploymentRepo: npm-local\n",
			400, notIncluded("npm-local", "y-out"),
		},
		{
			"yaml, itself", "PATCH", yaml,
			"virtualRepositories:\n  y-self:\n    type: generic\n    repositories: [y-self]\n", 200, "",
		},
		// A patch that deletes a member drops it from a virtual repository
		// it also updates, with the deployment repository it was.
		{
			"yaml, update of a virtual whose member the patch deletes", "PATCH", yaml,
			"localRepositories:\n  yaml-gone: null\nvirtualRepositories:\n  yaml-v:\n" +
				"    type: npm\n    description: kept\n",
			200, "",
		},
		// Writes of the other classes answer as before.
		{"create local", "PUT", r + "new-local", `{"rclass":"local","packageType":"npm"}`, 200, ""},
		{
			"update remote", "POST", r + "npm-remote", `{"description":"remote"}`, 200, "",
		},
		{
			"create remote without url", "PUT", r + "no-url", `{"rclass":"remote"}`,
			400, "No URL defined for remote repository",
		},
	} {
		headers := []string{}
		if tt.method == "PATCH" {
			headers = []string{"Content-Type", "application/yaml"}
		}
		rec := w.do(tt.method, tt.path, "admin", tt.body, headers...)
		got := ""
		if rec.Code == 400 {
			got = apiMessage(t, rec)
		}
		if rec.Code != tt.status {
			t.Errorf("%s: %d %s, want %d", tt.name, rec.Code, rec.Body, tt.status)
			continue
		}
		if tt.method == "PATCH" && !strings.HasSuffix(got, tt.message) ||
			tt.method != "PATCH" && got != tt.message {
			t.Errorf("%s: message %q, want %q", tt.name, got, tt.message)
		}
	}
	// A refused update changed nothing.
	if v := w.stored("npm-deploy"); strings.Join(v.Repositories, ",") != "npm-local,npm-other" ||
		v.DefaultDeploymentRepo != "npm-local" {
		t.Errorf("npm-deploy after refused updates: %v %q", v.Repositories, v.DefaultDeploymentRepo)
	}
	// The update of stale dropped the member and the deployment repository
	// that no longer exist, as 7.x does, rather than refusing it.
	if v := w.stored("stale"); !slices.Equal(v.Repositories, []string{"npm-local"}) ||
		v.DefaultDeploymentRepo != "" || v.Description != "kept" {
		t.Errorf("stale after its update: %v %q %q", v.Repositories, v.DefaultDeploymentRepo,
			v.Description)
	}
	if v := w.stored("misconfigured"); v.DefaultDeploymentRepo != "npm-local" {
		t.Errorf("misconfigured lost its deployment repository: %q", v.DefaultDeploymentRepo)
	}
	if v := w.stored("yaml-v"); !slices.Equal(v.Repositories, []string{"npm-local"}) ||
		v.DefaultDeploymentRepo != "" || v.Description != "kept" {
		t.Errorf("yaml-v after the patch: %v %q %q", v.Repositories, v.DefaultDeploymentRepo,
			v.Description)
	}
	if v := w.stored("self"); !slices.Equal(v.Repositories, []string{"self"}) {
		t.Errorf("self: %v", v.Repositories)
	}
}
