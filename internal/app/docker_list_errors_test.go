package app

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// specOperations finds the operations named in ids across the bundled specs.
func specOperations(t *testing.T, ids ...string) map[string]*openapi3.Operation {
	t.Helper()
	ops := map[string]*openapi3.Operation{}
	for _, name := range specFiles {
		for _, item := range loadSpec(t, name).Paths.Map() {
			for _, op := range item.Operations() {
				for _, id := range ids {
					if op.OperationID == id {
						ops[id] = op
					}
				}
			}
		}
	}
	if len(ops) != len(ids) {
		t.Fatalf("found %d of the operations %v", len(ops), ids)
	}
	return ops
}

// TestAccDockerListsAnswerRegistryErrors: the image catalog and the tag
// list answer each error a registry client can meet in the registry
// envelope their declarations name, with its OCI code, not in the
// Artifactory envelope JFrog's Searches pages declare for them (DV-153).
// The catalog never refuses a request as malformed, so only the tag list's
// 400 is reached; the API restriction stands in for the catalog's 403,
// since a caller without read is shown an empty catalog instead.
func TestAccDockerListsAnswerRegistryErrors(t *testing.T) {
	ops := specOperations(t, "listDockerRepositories", "listDockerTags")
	fail := map[string]bool{}
	w := newItemWorld(t, false, func(c *RouterConfig) {
		c.Store = faultyStore{Store: c.Store.(*inmem.Store), fail: fail}
	})
	for _, c := range []repo.Config{
		{Key: "docker-local", Class: repo.Local, PackageType: repo.Docker},
		{Key: "docker-remote", Class: repo.Remote, PackageType: repo.Docker, URL: "https://up.example"},
		{Key: "docker-virtual", Class: repo.Virtual, PackageType: repo.Docker, Repositories: []string{"docker-local"}},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	if rec := w.do(http.MethodPut, apiRestrictionPath+"reader", "admin", restrictionBody(),
		"Content-Type", "application/json"); rec.Code != http.StatusOK {
		t.Fatalf("restrict reader: %d %s", rec.Code, rec.Body)
	}
	catalog := func(key string) string { return "/artifactory/api/docker/" + key + "/v2/_catalog" }
	tags := func(key, image string) string {
		return "/artifactory/api/docker/" + key + "/v2/" + image + "/tags/list"
	}
	for _, c := range []struct {
		op, path, as string
		status       int
		code, fail   string
	}{
		{"listDockerRepositories", catalog("docker-local"), "", 401, "UNAUTHORIZED", ""},
		{"listDockerRepositories", catalog("docker-local"), "reader", 403, "DENIED", ""},
		{"listDockerRepositories", catalog("missing"), "admin", 404, "NAME_UNKNOWN", ""},
		{"listDockerRepositories", catalog("libs-local"), "admin", 404, "NAME_UNKNOWN", ""},
		{"listDockerRepositories", catalog("docker-remote"), "admin", 405, "UNSUPPORTED", ""},
		{"listDockerRepositories", catalog("docker-virtual"), "admin", 405, "UNSUPPORTED", ""},
		{"listDockerRepositories", catalog("docker-local"), "admin", 500, "INTERNAL_ERROR", "get:docker-local"},
		{"listDockerTags", tags("docker-local", "BAD"), "admin", 400, "NAME_INVALID", ""},
		{"listDockerTags", tags("docker-local", "app"), "", 401, "UNAUTHORIZED", ""},
		{"listDockerTags", tags("docker-local", "app"), "user", 403, "UNAUTHORIZED", ""},
		{"listDockerTags", tags("docker-local", "app"), "reader", 403, "DENIED", ""},
		{"listDockerTags", tags("docker-local", "app"), "admin", 404, "NAME_UNKNOWN", ""},
		{"listDockerTags", tags("missing", "app"), "admin", 404, "NAME_UNKNOWN", ""},
		{"listDockerTags", tags("docker-remote", "app"), "admin", 405, "UNSUPPORTED", ""},
		{"listDockerTags", tags("docker-local", "app"), "admin", 500, "INTERNAL_ERROR", "get:docker-local"},
	} {
		clear(fail)
		if c.fail != "" {
			fail[c.fail] = true
		}
		rec := w.do(http.MethodGet, c.path, c.as, "")
		problems := declaredAnswer(ops[c.op], rec)
		var body struct {
			Errors []struct{ Code string } `json:"errors"`
		}
		switch {
		case rec.Code != c.status:
			problems = []string{"answered " + strconv.Itoa(rec.Code)}
		case json.Unmarshal(rec.Body.Bytes(), &body) != nil || len(body.Errors) == 0:
			problems = append(problems, "no registry error")
		case body.Errors[0].Code != c.code:
			problems = append(problems, "code "+body.Errors[0].Code+", want "+c.code)
		}
		if len(problems) > 0 {
			t.Errorf(
				"GET %s as %q (%s): %s\n%s",
				c.path,
				c.as,
				c.op,
				strings.Join(problems, "; "),
				rec.Body,
			)
		}
	}
}
