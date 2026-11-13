package puppet_test

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// puppetVirtual creates pvirtual, a virtual Puppet repository over members
// in order, in w.
func puppetVirtual(t *testing.T, w *remoteWorld, members ...string) {
	t.Helper()
	virtual := *repo.Defaults(&repo.Config{
		Key: "pvirtual", Class: repo.Virtual, PackageType: repo.Puppet, Repositories: members,
	})
	if err := w.rt.Store.Repos().Create(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
}

// askedForModules reports whether asked holds a request for a module record.
func askedForModules(asked []string) bool {
	return slices.ContainsFunc(asked, func(path string) bool {
		return strings.HasPrefix(path, "/v3/modules/")
	})
}

// A virtual repository over a remote answers v3/modules/<m> for a module
// the remote's Forge release list leaves out, a Puppet Enterprise module's
// here, with the record the remote answers from the Forge's own record of
// the module (#1370), once no member lists a release of it (#1422) [I].
// r10k reads that record and then fetches its current release's file_uri
// from the same repository, which the virtual repository serves (#1369), so
// r10k installs the module through it. A module a member lists keeps its
// answer, merged from the members' releases, and Read on the virtual
// repository is checked before the Forge is asked.
func TestAccPuppetVirtualModuleOnlyAnUpstreamRecordHolds(t *testing.T) {
	const v = "/artifactory/api/puppet/pvirtual"
	up := newGroupedForge(t)
	w := groupedRemote(t, up, nil)
	puppetVirtual(t, w, "plocal", "remote")
	r := w.request("GET", v+"/v3/modules/acme-pe", "admin", "")
	if asked := up.requests(); !slices.Contains(asked,
		"/v3/modules/acme-pe?exclude_fields=current_release") {
		t.Fatalf("the Forge was not asked for the module record: %q", asked)
	}
	// The remote member answers the same record, from its cache.
	remote := w.request("GET", "/artifactory/api/puppet/remote/v3/modules/acme-pe", "admin", "")
	if r.Code != 200 || r.Body.String() != remote.Body.String() {
		t.Fatalf("module: %d %s\nwant %d %s", r.Code, r.Body.String(), remote.Code,
			remote.Body.String())
	}
	r = w.request("HEAD", v+"/v3/modules/acme-pe", "admin", "")
	if r.Code != 200 || r.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %s", r.Code, r.Body.String())
	}
	var module struct {
		CurrentRelease struct {
			FileURI string `json:"file_uri"`
		} `json:"current_release"`
	}
	if err := json.Unmarshal(remote.Body.Bytes(), &module); err != nil {
		t.Fatal(err)
	}
	r = w.request("GET", v+module.CurrentRelease.FileURI, "admin", "")
	if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), up.archives["acme-pe-1.0.0"]) {
		t.Fatalf("current release's archive: %d %s", r.Code, r.Body.String())
	}

	local := w.request("GET", "/artifactory/api/puppet/plocal/v3/modules/acme-widget", "admin", "")
	up.requests()
	r = w.request("GET", v+"/v3/modules/acme-widget", "admin", "")
	if r.Code != 200 || r.Body.String() != local.Body.String() {
		t.Fatalf("listed module: %d %s\nwant 200 %s", r.Code, r.Body.String(), local.Body.String())
	}
	if asked := up.requests(); askedForModules(asked) {
		t.Fatalf("a listed module read the Forge's module record: %q", asked)
	}

	for _, user := range []string{"denied", "reader"} {
		remoteStatus(t, w.request("GET", v+"/v3/modules/acme-pe", user, ""), 403)
	}
	if asked := up.requests(); len(asked) != 0 {
		t.Fatalf("a refused caller reached the Forge: %q", asked)
	}
	remoteStatus(t, w.request("GET", v+"/v3/modules/acme-none", "admin", ""), 404)
}

// A virtual repository passes over a remote member that is blacked out, or
// whose patterns reject the release pages' policy path,
// .puppet/<m>/releases.json, without asking its Forge, and answers from the
// next remote member in member order (R5) [I].
func TestAccPuppetVirtualModulePassesOverARefusingMember(t *testing.T) {
	const v = "/artifactory/api/puppet/pvirtual"
	for _, tc := range []struct {
		name string
		tune func(*repo.Config)
	}{
		{"blacked out", func(c *repo.Config) { c.BlackedOut = new(true) }},
		{"policy path excluded", func(c *repo.Config) { c.ExcludesPattern = new(".puppet/**") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refusing, next := newGroupedForge(t), newGroupedForge(t)
			w := groupedRemote(t, refusing, tc.tune)
			second := *repo.Defaults(&repo.Config{
				Key: "remote2", Class: repo.Remote, PackageType: repo.Puppet, URL: next.URL,
			})
			if err := w.rt.Store.Repos().Create(t.Context(), second); err != nil {
				t.Fatal(err)
			}
			puppetVirtual(t, w, "remote", "remote2")
			r := w.request("GET", v+"/v3/modules/acme-pe", "admin", "")
			if asked := refusing.requests(); len(asked) != 0 {
				t.Fatalf("the refusing member's Forge was asked %q", asked)
			}
			if asked := next.requests(); !askedForModules(asked) {
				t.Fatalf("the next member's Forge was not asked for the record: %q", asked)
			}
			want := w.request(
				"GET",
				"/artifactory/api/puppet/remote2/v3/modules/acme-pe",
				"admin",
				"",
			)
			if r.Code != 200 || r.Body.String() != want.Body.String() {
				t.Fatalf("module: %d %s\nwant %d %s", r.Code, r.Body.String(), want.Code,
					want.Body.String())
			}
		})
	}
}
