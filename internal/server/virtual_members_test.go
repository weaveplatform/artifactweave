package server

import (
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// A virtual repository may have no members, given as an absent or an empty
// repositories (#394). It then answers as an empty repository: reads find
// nothing, deploys have no repository to go to, and nothing answers 500.
func TestAccVirtualWithoutMembers(t *testing.T) {
	w := newItemWorld(t, false)
	for key, body := range map[string]string{
		"gen-empty":   `{"rclass":"virtual"}`,
		"npm-empty":   `{"rclass":"virtual","packageType":"npm","repositories":[]}`,
		"maven-empty": `{"rclass":"virtual","packageType":"maven"}`,
	} {
		if rec := w.do("PUT", "/artifactory/api/repositories/"+key, "admin", body,
			"Content-Type", "application/json"); rec.Code != 200 {
			t.Fatalf("create %s: %d %s", key, rec.Code, rec.Body)
		}
	}
	// With no members a deployment repository has nothing to name.
	if rec := w.do("PUT", "/artifactory/api/repositories/deploy-empty", "admin",
		`{"rclass":"virtual","defaultDeploymentRepo":"libs-local"}`,
		"Content-Type", "application/json"); rec.Code != 400 ||
		!strings.Contains(rec.Body.String(), "defaultDeploymentRepo") {
		t.Fatalf("deployment repository without members: %d %s", rec.Code, rec.Body)
	}
	// The Terraform provider reads an absent repositories as null against
	// its [] default, so a read lists none as [].
	for _, path := range []string{
		"/artifactory/api/repositories/npm-empty",
		"/artifactory/api/v2/repositories/npm-empty",
		"/artifactory/api/v2/repositories/batch?names=npm-empty",
	} {
		if rec := w.do("GET", path, "admin", ""); rec.Code != 200 ||
			!strings.Contains(rec.Body.String(), `"repositories":[]`) {
			t.Errorf("GET %s: %d %s", path, rec.Code, rec.Body)
		}
	}
	for _, tt := range []struct {
		method, path, body string
		want               int
		has                string
	}{
		{"GET", "/artifactory/api/repositories?type=virtual", "", 200, `"key":"gen-empty"`},
		{"GET", "/artifactory/gen-empty/", "", 200, "<pre></pre>"},
		{"GET", "/artifactory/gen-empty/a.txt", "", 404, ""},
		{"HEAD", "/artifactory/gen-empty/a.txt", "", 404, ""},
		{"PUT", "/artifactory/gen-empty/a.txt", "x", 405, "no default deployment repository"},
		{"GET", "/artifactory/api/storage/gen-empty", "", 200, `"children":[]`},
		{"GET", "/artifactory/api/storage/gen-empty/a", "", 404, ""},
		{"GET", "/artifactory/api/storage/gen-empty?list&deep=1", "", 200, `"files":[]`},
		{"POST", "/artifactory/api/search/aql", `items.find({"repo":"gen-empty"})`, 200, `"total": 0`},
		{"GET", "/artifactory/api/search/artifact?name=a.txt&repos=gen-empty", "", 200, `"results":[]`},
		{"GET", "/artifactory/api/search/gavc?g=org&repos=maven-empty", "", 200, `"results":[]`},
		{"GET", "/artifactory/maven-empty/org/x/maven-metadata.xml", "", 404, ""},
		{"GET", "/artifactory/maven-empty/org/x/1.0/x-1.0.jar", "", 404, ""},
		// The npm API does not serve virtual repositories yet, with members
		// or without, and answers both alike.
		{"GET", "/artifactory/api/npm/npm-empty/pkg", "", 501, "does not serve"},
		{"POST", "/artifactory/api/repositories/npm-empty", `{"description":"d"}`, 200, ""},
		{"DELETE", "/artifactory/api/repositories/npm-empty", "", 200, ""},
	} {
		rec := w.do(tt.method, tt.path, "admin", tt.body, "Content-Type", "application/json")
		if rec.Code != tt.want || !strings.Contains(rec.Body.String(), tt.has) {
			t.Errorf("%s %s: %d %s, want %d with %q",
				tt.method, tt.path, rec.Code, rec.Body, tt.want, tt.has)
		}
	}
}

// A member must have the virtual repository's package type unless the
// virtual repository is generic (RTDEV-62756, 7.133.3), on every write path
// of the repositories API.
func TestAccVirtualMemberPackageTypes(t *testing.T) {
	w := newRepoWorld(t)
	w.create(
		repo.Config{Key: "npm-local", Class: repo.Local, PackageType: repo.NPM},
		repo.Config{Key: "maven-local", Class: repo.Local, PackageType: repo.Maven},
		repo.Config{Key: "gradle-local", Class: repo.Local, PackageType: repo.Gradle},
		repo.Config{Key: "npm-virtual", Class: repo.Virtual, PackageType: repo.NPM},
	)
	const mismatch = "Virtual repository member 'maven-local' has package type 'maven', " +
		"which does not match the virtual repository's package type 'npm'"
	r := "/artifactory/api/repositories/"
	batch := "/artifactory/api/v2/repositories/batch"
	for _, tt := range []struct {
		name, method, path, body string
		want                     int
	}{
		{
			"create, other type", "PUT", r + "mixed",
			`{"rclass":"virtual","packageType":"npm","repositories":["npm-local","maven-local"]}`, 400,
		},
		{
			"create, nested virtual of other type", "PUT", r + "go-mixed",
			`{"rclass":"virtual","packageType":"go","repositories":["npm-virtual"]}`, 400,
		},
		{
			"create, same type", "PUT", r + "npm-all",
			`{"rclass":"virtual","packageType":"npm","repositories":["npm-local","npm-virtual"]}`, 200,
		},
		{
			"create, generic takes any", "PUT", r + "gen-all",
			`{"rclass":"virtual","repositories":["npm-local","maven-local","npm-virtual"]}`, 200,
		},
		{
			"create, maven family", "PUT", r + "maven-all",
			`{"rclass":"virtual","packageType":"maven","repositories":["maven-local","gradle-local"]}`, 200,
		},
		{"update, other type", "POST", r + "npm-virtual", `{"repositories":["maven-local"]}`, 400},
		{"update, same type", "POST", r + "npm-virtual", `{"repositories":["npm-local"]}`, 200},
		{
			"batch create, other type", "PUT", batch,
			`[{"key":"b-mixed","rclass":"virtual","packageType":"npm","repositories":["maven-local"]}]`, 400,
		},
		{
			"batch create, member of the batch", "PUT", batch,
			`[{"key":"b-npm","rclass":"local","packageType":"npm"},` +
				`{"key":"b-virt","rclass":"virtual","packageType":"npm","repositories":["b-npm"]}]`, 201,
		},
		{
			"batch update, other type", "POST", batch,
			`[{"key":"npm-virtual","repositories":["maven-local"]}]`, 400,
		},
	} {
		rec := w.do(tt.method, tt.path, "admin", tt.body)
		if rec.Code != tt.want {
			t.Errorf("%s: %d %s", tt.name, rec.Code, rec.Body)
		}
		if tt.want == 400 && strings.Contains(tt.body, "maven-local") &&
			!strings.Contains(rec.Body.String(), mismatch) {
			t.Errorf("%s: message %s", tt.name, rec.Body)
		}
	}
	if v := w.stored("npm-virtual"); strings.Join(v.Repositories, ",") != "npm-local" {
		t.Fatalf("a refused update changed the members: %v", v.Repositories)
	}
}
