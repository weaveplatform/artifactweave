package puppet_test

import (
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

// The production router answers v3/releases without module and v3/modules
// without query, the generated Get Puppet Releases and Get Puppet Modules
// operations, with a local repository's paged lists, and a virtual
// repository's with 404 (7.x; RTFACT-15072).
func TestAccPuppetWholeRepositoryLists(t *testing.T) {
	w := newRemoteWorld(t)
	data := repopath.Read(t, "test/acceptance/fixtures/puppet/acme-widget-1.0.0.tar.gz")
	c := *repo.Defaults(&repo.Config{Key: "puppet", Class: repo.Local, PackageType: repo.Puppet})
	if err := w.rt.Store.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	v := *repo.Defaults(&repo.Config{
		Key: "puppet-virtual", Class: repo.Virtual, PackageType: repo.Puppet,
		Repositories: []string{c.Key},
	})
	if err := w.rt.Store.Repos().Create(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	remoteStatus(t, w.request(
		"PUT", "/artifactory/puppet/acme-widget/acme-widget-1.0.0.tar.gz", "admin", string(data),
	), 201)
	const api = "/artifactory/api/puppet/"
	if got := listed(t, w.request("GET", api+"puppet/v3/releases", "admin", "")); len(got) != 1 ||
		got["1.0.0"] != checksum(data) {
		t.Fatalf("releases: %v", got)
	}
	r := w.request("GET", api+"puppet/v3/releases?limit=1&offset=0", "admin", "")
	want := `"pagination":{"limit":1,"offset":0,"first":"/v3/releases?limit=1&offset=0",` +
		`"previous":null,"current":"/v3/releases?limit=1&offset=0","next":null,"total":1}`
	if r.Code != 200 || !strings.Contains(r.Body.String(), want) {
		t.Fatalf("releases page: %d %s", r.Code, r.Body.String())
	}
	r = w.request("GET", api+"puppet/v3/modules", "admin", "")
	body := r.Body.String()
	if r.Code != 200 || !strings.Contains(body, `"total":1},"results":[{"current_release"`) ||
		!strings.Contains(body, `"slug":"acme-widget","uri":"/v3/modules/acme-widget"}]}`) {
		t.Fatalf("modules: %d %s", r.Code, body)
	}
	for _, route := range []string{"v3/releases", "v3/modules"} {
		remoteStatus(t, w.request("HEAD", api+"puppet/"+route, "admin", ""), 200)
		remoteStatus(t, w.request("GET", api+"puppet/"+route, "denied", ""), 403)
		remoteStatus(t, w.request("GET", api+"puppet/"+route, "", ""), 401)
		r := w.request("GET", api+"puppet-virtual/"+route, "admin", "")
		if r.Code != 404 || r.Body.String() !=
			`{"errors":[{"message":"404 Not found","status":404}]}`+"\n" {
			t.Fatalf("virtual %s: %d %s", route, r.Code, r.Body.String())
		}
	}
}
