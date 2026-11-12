package chefhttp_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// describedArchive is a cookbook archive whose metadata.json names a
// maintainer and a description, which a search looks in.
func describedArchive(t *testing.T, name, version, maintainer, description string) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	manifest, err := json.Marshal(map[string]string{
		"name": name, "version": version, "maintainer": maintainer, "description": description,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range [][2]string{
		{"metadata.json", string(manifest)},
		{"recipes/default.rb", "# controlled recipe"},
	} {
		header := &tar.Header{Name: name + "/" + file[0], Mode: 0o644, Size: int64(len(file[1]))}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(file[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// listed is one cookbook of a catalog page, as the page writes it.
type listed struct{ name, maintainer, description string }

// page returns the exact body of a catalog page at prefix that holds
// entries, from start, of total.
func page(t *testing.T, prefix string, start, total int, entries ...listed) string {
	t.Helper()
	items := []map[string]any{}
	for _, e := range entries {
		items = append(items, map[string]any{
			"cookbook_name":        e.name,
			"cookbook_maintainer":  e.maintainer,
			"cookbook_description": e.description,
			"cookbook":             "http://example.com" + prefix + "/api/v1/cookbooks/" + e.name,
		})
	}
	var b bytes.Buffer
	if err := json.NewEncoder(&b).Encode(map[string]any{
		"total": total, "start": start, "items": items,
	}); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// wantPage fails unless GET path answers 200 with body.
func (f *fixture) wantPage(path, body string) {
	f.t.Helper()
	got := f.do(http.MethodGet, base+path, "admin")
	if got.Code != http.StatusOK || got.Body.String() != body {
		f.t.Fatalf("GET %s: %d %s\nwant 200 %s", path, got.Code, got.Body, body)
	}
}

// wantRefused fails unless GET path answers 400 with message.
func (f *fixture) wantRefused(path, message string) {
	f.t.Helper()
	got := f.do(http.MethodGet, base+path, "admin")
	if got.Code != http.StatusBadRequest || errorMessage(f.t, got.Body.Bytes()) != message {
		f.t.Fatalf("GET %s: %d %s, want 400 %q", path, got.Code, got.Body, message)
	}
}

var (
	widgetListed = listed{"widget", "Acme", "Builds frames"}
	helperListed = listed{"helper", "Bolt Works", "Shared helpers"}
)

// Knife 19.0.134's supermarket list and search, and knife-art's
// artifactory list and search over them, ask items=9999999, and add order
// for --sort-by and user for --owned-by. Every class answers them as it
// answers items=1000, the most a page holds, with order and user changing
// nothing, where it answered 400 before (#1305). A search looks in the
// name, description and maintainer of each cookbook, as 7.x's does for a
// local repository (JFrog, "Search for Chef Cookbooks").
func TestAccChefCatalogTakesKnifeQueries(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated, repo.Virtual} {
		t.Run(string(class), func(t *testing.T) {
			f := newFixture(t, class)
			target := f.c
			if class == repo.Virtual {
				target = f.member("hosted", repo.Local, "")
				// A member deleted since it was listed is passed over, by the
				// catalog and by the bound on start alike.
				f.patch(
					f.c.Key,
					func(c *repo.Config) { c.Repositories = []string{"deleted", "hosted"} },
				)
			}
			f.deploy(
				target,
				"widget",
				"1.0.0",
				describedArchive(
					t,
					"widget",
					"1.0.0",
					widgetListed.maintainer,
					widgetListed.description,
				),
			)
			f.deploy(
				target,
				"helper",
				"1.0.0",
				describedArchive(
					t,
					"helper",
					"1.0.0",
					helperListed.maintainer,
					helperListed.description,
				),
			)

			both := page(t, base, 0, 2, helperListed, widgetListed)
			for _, path := range []string{
				"/api/v1/cookbooks?items=9999999&start=0",
				"/api/v1/cookbooks?items=1001&start=0",
				"/api/v1/cookbooks?items=9999999&start=0&order=recently_updated",
				"/api/v1/cookbooks?items=9999999&start=0&user=someone",
				"/api/v1/cookbooks?items=9999999&start=0&order=most_downloaded&user=someone",
				"/api/v1/cookbooks?items=1000&start=0",
			} {
				f.wantPage(path, both)
			}
			f.wantPage("/api/v1/cookbooks?items=9999999&start=1", page(t, base, 1, 2, widgetListed))
			for path, body := range map[string]string{
				"/api/v1/search?q=wid&items=9999999&start=0":    page(t, base, 0, 1, widgetListed),
				"/api/v1/search?q=frames&items=9999999&start=0": page(t, base, 0, 1, widgetListed),
				"/api/v1/search?q=BOLT&items=9999999&start=0":   page(t, base, 0, 1, helperListed),
				"/api/v1/search?q=absent&items=9999999&start=0": page(t, base, 0, 0),
			} {
				f.wantPage(path, body)
			}

			const bounds, unsupported = "invalid search bounds", "unsupported search query"
			for path, message := range map[string]string{
				"/api/v1/cookbooks?items=0":             bounds,
				"/api/v1/cookbooks?items=-1":            bounds,
				"/api/v1/cookbooks?items=1e3":           bounds,
				"/api/v1/cookbooks?order=a&order=b":     unsupported,
				"/api/v1/cookbooks?user=a&user=b":       unsupported,
				"/api/v1/cookbooks?platforms%5B%5D=deb": unsupported,
				"/api/v1/cookbooks?x=1":                 unsupported,
			} {
				f.wantRefused(path, message)
			}
			// A hosted catalog holds at most 4,096 cookbooks, so a start past
			// that is still refused, as it is by a virtual repository of
			// hosted members alone; one with a remote member takes it
			// (test/server/artifacts/chef, TestAccChefRemoteCatalogPastMaxVersions;
			// #1306).
			f.wantRefused("/api/v1/cookbooks?start=4097", bounds)
		})
	}
}

// A remote's search lists the cookbooks its cache holds that answer the
// query, then its upstream's search, which it forwards once, leaving out a
// cookbook it already listed; its total adds the upstream's. Its catalog
// lists its upstream's catalog alone (#1306; JFrog, "Search for Chef
// Cookbooks").
func TestAccChefRemoteSearchMergesItsCache(t *testing.T) {
	f, up, _ := newSourceFixture(t, repo.Remote)
	f.patch(f.c.Key, func(c *repo.Config) { c.URL = up.URL + "/chef/" })
	if got := f.do(
		http.MethodGet, base+cookbookPath+"/versions/1_0_0/download", "admin",
	); got.Code != http.StatusOK {
		t.Fatalf("download: %d %s", got.Code, got.Body)
	}
	cached := listed{"widget", "", ""}
	search := func(items string) {
		up.Set("/chef/api/v1/search", fakeupstream.Resource{
			Body:   []byte(`{"total":4078,"start":0,"items":[` + items + `]}`),
			Header: http.Header{"Content-Type": {"application/json"}},
		})
	}
	other := `{"cookbook_name":"widgetry","cookbook_maintainer":"acme",` +
		`"cookbook_description":"more widgets","cookbook":"https://supermarket.example/x"}`
	upstreamWidget := `{"cookbook_name":"widget","cookbook_maintainer":"upstream",` +
		`"cookbook_description":"the upstream's widget"}`
	widgetry := listed{"widgetry", "acme", "more widgets"}

	search(other)
	f.wantPage("/api/v1/search?q=wid&items=9999999&start=0",
		page(t, base, 0, 4079, cached, widgetry))
	search(upstreamWidget + "," + other)
	f.wantPage("/api/v1/search?q=wid&items=9999999&start=0",
		page(t, base, 0, 4079, cached, widgetry))
	// The page's one place is the cached cookbook's; the next is the
	// upstream's first, a widget already listed, so it stays empty. The
	// upstream requests a page makes are counted in
	// test/server/artifacts/chef, whose fake Supermarket pages its answers.
	f.wantPage("/api/v1/search?q=wid&items=1&start=0", page(t, base, 0, 4079, cached))
	f.wantPage("/api/v1/search?q=wid&items=1&start=1", page(t, base, 1, 4079))
	f.wantPage(
		"/api/v1/cookbooks?items=9999999&start=0",
		page(t, base, 0, 1, listed{"widget", "", ""}),
	)
}

// A virtual repository lists its hosted members' cookbooks, by name, then
// each remote member's upstream catalog, leaving out a cookbook already
// listed; start counts the upstream's own offsets after the hosted
// cookbooks, and total adds the upstream's total (#1306).
func TestAccChefVirtualCatalogListsHostedThenUpstream(t *testing.T) {
	f, up, _ := newSourceFixture(t, repo.Virtual)
	f.member("chef-remote", repo.Remote, up.URL+"/chef/")
	hosted := f.member("chef-hosted", repo.Local, "")
	f.deploy(hosted, "zeta", "1.0.0", describedArchive(t, "zeta", "1.0.0", "Z", "last"))
	f.deploy(hosted, "widget", "2.0.0", describedArchive(t, "widget", "2.0.0", "W", "hosted"))
	f.patch(f.c.Key, func(c *repo.Config) {
		c.Repositories = []string{"chef-remote", "chef-hosted"}
	})
	up.Set("/chef/api/v1/cookbooks", fakeupstream.Resource{
		Body: []byte(`{"total":2,"start":0,"items":[` +
			`{"cookbook_name":"apple","cookbook_maintainer":"A","cookbook_description":"first"},` +
			`{"cookbook_name":"widget","cookbook_maintainer":"U","cookbook_description":"upstream"}]}`),
		Header: http.Header{"Content-Type": {"application/json"}},
	})
	widget, zeta, apple := listed{"widget", "W", "hosted"}, listed{"zeta", "Z", "last"},
		listed{"apple", "A", "first"}
	f.wantPage("/api/v1/cookbooks?items=9999999&start=0", page(t, base, 0, 4, widget, zeta, apple))
	f.wantPage("/api/v1/cookbooks?start=1&items=2", page(t, base, 1, 4, zeta, apple))
	f.wantPage("/api/v1/cookbooks?start=3&items=1", page(t, base, 3, 4))
	if got := f.do(
		http.MethodGet,
		base+"/api/v1/cookbooks?start=1073741825",
		"admin",
	); got.Code != 400 {
		t.Fatalf("a start past 2^30: %d %s", got.Code, got.Body)
	}
}
