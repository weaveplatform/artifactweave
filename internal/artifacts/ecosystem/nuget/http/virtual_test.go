package nugethttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

const virtualV3 = "/artifactory/api/nuget/v3/nuget-virtual/"

func newVirtualFixture(t *testing.T) *remoteFixture {
	t.Helper()
	f := newRemoteFixture(t)
	for _, c := range []repo.Config{{Key: "nuget-nested", Class: repo.Virtual, PackageType: repo.NuGet, Repositories: []string{"nuget-local", "nuget-remote"}}, {Key: "nuget-virtual", Class: repo.Virtual, PackageType: repo.NuGet, Repositories: []string{"nuget-nested", "nuget-local"}}} {
		if err := f.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func virtualJSON(t *testing.T, r *httptest.ResponseRecorder, want int) map[string]any {
	t.Helper()
	if r.Code != want {
		t.Fatalf("status %d want %d: %s", r.Code, want, r.Body)
	}
	var d map[string]any
	if want == 200 && json.Unmarshal(r.Body.Bytes(), &d) != nil {
		t.Fatal(r.Body)
	}
	return d
}

func (f *remoteFixture) virtualRepo(t *testing.T, key string, edit func(*repo.Config)) {
	t.Helper()
	c, err := f.db.Repos().Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	edit(&c)
	if err = f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
}

func assertVirtualURLs(t *testing.T, r *httptest.ResponseRecorder) {
	t.Helper()
	if strings.Contains(r.Body.String(), "/nuget-local/") || strings.Contains(r.Body.String(), "/nuget-remote/") || strings.Contains(r.Body.String(), "/nuget-nested/") ||
		strings.Contains(r.Body.String(), "127.0.0.1") {
		t.Fatal(r.Body)
	}
}

func TestAccVirtualV3UnionRegistrationAndWholeOwner(t *testing.T) {
	f := newVirtualFixture(t)
	local := f.push("Acme.Mixed", "1.0.0", "<description>first local owner</description>")
	f.push("Acme.Mixed", "10.0.0", "")
	entries := f.packageDocs("Acme.Mixed", "1.0.0", "2.0.0", "3.0.0-rc.1")
	entries[1]["catalogEntry"].(map[string]any)["customSignatureMetadata"] = map[string]any{"signatureType": "author", "fingerprint": "preserved"}
	entries[1]["catalogEntry"].(map[string]any)["description"] = "rich upstream fields"
	f.setJSON("/opaque/page/acme.mixed", map[string]any{"items": entries})
	d := virtualJSON(t, f.do("GET", virtualV3+"flatcontainer/acme.mixed/index.json", "reader"), 200)
	if fmt.Sprint(d["versions"]) != "[1.0.0 2.0.0 3.0.0-rc.1 10.0.0]" {
		t.Fatal(d)
	}
	for _, path := range []string{"index.json", "registration/acme.mixed/index.json", "registration-semver2/acme.mixed/index.json", "registration-semver2/acme.mixed/page/1.0.0/10.0.0.json", "registration-semver2/acme.mixed/2.0.0.json"} {
		r := f.do("GET", virtualV3+path, "reader")
		virtualJSON(t, r, 200)
		assertVirtualURLs(t, r)
		if strings.Contains(path, "page/") &&
			(!strings.Contains(r.Body.String(), "customSignatureMetadata") || !strings.Contains(r.Body.String(), "rich upstream fields") || !strings.Contains(r.Body.String(), virtualV3+"registration-semver2/acme.dependency/index.json")) {
			t.Fatal(r.Body)
		}
	}
	r := f.do("GET", virtualV3+"registration/acme.mixed/page/0.0.0/10.0.0.json", "reader")
	virtualJSON(t, r, 200)
	if strings.Contains(r.Body.String(), "3.0.0-rc.1") {
		t.Fatal(r.Body)
	}
	// Member-only grants authorize both source reads and the delivery fence.
	r = f.do("GET", virtualV3+"flatcontainer/acme.mixed/2.0.0/acme.mixed.2.0.0.nupkg", "reader")
	if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), nupkg("Acme.Mixed", "2.0.0", `<dependencies><dependency id="Acme.Dependency" version="[1.0.0]" /></dependencies>`)) {
		t.Fatalf("remote bytes %d %s", r.Code, r.Body)
	}
	before := len(f.up.Requests())
	r = f.do("GET", virtualV3+"flatcontainer/acme.mixed/1.0.0/acme.mixed.1.0.0.nupkg", "reader")
	if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), local) {
		t.Fatalf("first holder %d %s", r.Code, r.Body)
	}
	if len(f.up.Requests()) != before {
		t.Fatal("local download contacted upstream")
	}
	for _, path := range []string{"flatcontainer/acme.mixed/1.0/acme.mixed.1.0.nupkg", "flatcontainer/acme.mixed/1.0.0/acme.mixed.nuspec", "registration/Download/ACME.MIXED/1.0.0"} {
		r = f.do("GET", virtualV3+path, "reader")
		if r.Code != 200 {
			t.Fatalf("%s %d %s", path, r.Code, r.Body)
		}
	}
	for _, path := range []string{"flatcontainer/acme.mixed/2.0.0/acme.mixed.nuspec", "registration-semver2/Download/Acme.Mixed/2.0.0"} {
		if r = f.do("GET", virtualV3+path, "reader"); r.Code != 200 {
			t.Fatalf("%s %d %s", path, r.Code, r.Body)
		}
	}
	if len(f.up.Requests()) == 0 {
		t.Fatal("remote contribution was not exercised")
	}
}

func TestAccVirtualV3PriorityHidesWholeNonPriorityID(t *testing.T) {
	for _, kind := range []string{"remote", "local"} {
		t.Run(kind, func(t *testing.T) {
			f := newVirtualFixture(t)
			local := f.push("Acme.Mixed", "1.0.0", "")
			f.packageDocs("Acme.Mixed", "1.0.0", "2.0.0")
			f.virtualRepo(t, "nuget-"+kind, func(c *repo.Config) { c.PriorityResolution = repo.Flex(true) })
			d := virtualJSON(t, f.do("GET", virtualV3+"flatcontainer/acme.mixed/index.json", "reader"), 200)
			r := f.do("GET", virtualV3+"flatcontainer/acme.mixed/1.0.0/acme.mixed.1.0.0.nupkg", "reader")
			if r.Code != 200 {
				t.Fatal(r.Code, r.Body)
			}
			if kind == "remote" {
				if bytes.Equal(local, r.Body.Bytes()) || fmt.Sprint(d["versions"]) != "[1.0.0 2.0.0]" {
					t.Fatal(d)
				}
			} else {
				if !bytes.Equal(local, r.Body.Bytes()) || fmt.Sprint(d["versions"]) != "[1.0.0]" {
					t.Fatal(d)
				}
				virtualJSON(t, f.do("GET", virtualV3+"flatcontainer/acme.mixed/2.0.0/acme.mixed.2.0.0.nupkg", "reader"), 404)
				if len(f.up.Requests()) != 0 {
					t.Fatal("priority local contacted remote")
				}
			}
		})
	}
	f := newVirtualFixture(t)
	f.push("Acme.Local", "1.0.0", "")
	f.virtualRepo(t, "nuget-remote", func(c *repo.Config) { c.PriorityResolution = repo.Flex(true) })
	if r := f.do("GET", virtualV3+"flatcontainer/acme.local/1.0.0/acme.local.1.0.0.nupkg", "reader"); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
}

func TestAccVirtualV3VisibilityPoliciesAndAbsentResources(t *testing.T) {
	for _, mode := range []string{"unreadable", "blackout", "pattern", "nested-pattern", "virtual-pattern", "offline", "missing-hive", "upstream-403"} {
		t.Run(mode, func(t *testing.T) {
			f := newVirtualFixture(t)
			f.push("Acme.Local", "1.0.0", "")
			f.packageDocs("Acme.Remote", "2.0.0")
			user := "reader"
			switch mode {
			case "unreadable":
				user = "nobody"
			case "blackout":
				f.virtualRepo(t, "nuget-remote", func(c *repo.Config) { b := true; c.BlackedOut = &b })
			case "pattern":
				f.virtualRepo(t, "nuget-remote", func(c *repo.Config) { s := "other/**"; c.IncludesPattern = &s })
			case "nested-pattern":
				f.virtualRepo(t, "nuget-nested", func(c *repo.Config) { s := "other/**"; c.IncludesPattern = &s })
			case "virtual-pattern":
				f.virtualRepo(t, "nuget-virtual", func(c *repo.Config) { s := "other/**"; c.IncludesPattern = &s })
			case "offline":
				f.virtualRepo(t, "nuget-remote", func(c *repo.Config) { b := true; c.Offline = &b })
			case "missing-hive":
				f.setJSON("/v3/index.json", map[string]any{"version": "3.0.0", "resources": []map[string]string{{"@id": f.up.URL + "/flat/", "@type": "PackageBaseAddress/3.0.0"}}})
			case "upstream-403":
				f.up.Set("/flat/acme.remote/index.json", fakeupstream.Resource{Status: 403})
				f.up.Set("/reg/acme.remote/index.json", fakeupstream.Resource{Status: 403})
			}
			for _, path := range []string{"registration-semver2/acme.remote/index.json", "flatcontainer/acme.remote/2.0.0/acme.remote.2.0.0.nupkg"} {
				want := 404
				if mode == "missing-hive" && strings.HasPrefix(path, "flatcontainer/") {
					want = 404
				}
				r := f.do("GET", virtualV3+path, user)
				if r.Code != want {
					t.Fatalf("%s %d %s", path, r.Code, r.Body)
				}
			}
			if mode == "unreadable" || mode == "blackout" || mode == "offline" {
				if len(f.up.Requests()) != 0 {
					t.Fatal("ineligible member reached upstream")
				}
			}
			if mode == "missing-hive" {
				r := f.do("GET", virtualV3+"flatcontainer/acme.remote/index.json", "reader")
				virtualJSON(t, r, 200)
			}
		})
	}
	// A priority member with no readable project does not shadow local fallback.
	f := newVirtualFixture(t)
	f.push("Acme.Mixed", "1.0.0", "")
	f.packageDocs("Acme.Mixed", "2.0.0")
	f.virtualRepo(t, "nuget-remote", func(c *repo.Config) { c.PriorityResolution = repo.Flex(true) })
	if err := f.db.Permissions().
		Create(t.Context(), accesstest.Permission{Name: "local-only", Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]api.Actions{"localonly": api.ActionRead}}, Targets: map[string]accesstest.Target{"nuget-local": {}}}}}); err != nil {
		t.Fatal(err)
	}
	virtualJSON(t, f.do("GET", virtualV3+"flatcontainer/acme.mixed/index.json", "localonly"), 200)
	virtualJSON(t, f.do("GET", virtualV3+"flatcontainer/acme.mixed/2.0.0/acme.mixed.2.0.0.nupkg", "localonly"), 404)
	if len(f.up.Requests()) != 0 {
		t.Fatal("unreadable priority remote reached upstream")
	}
}

func TestAccVirtualV3DeferredOperationsAndMalformedReads(t *testing.T) {
	f := newVirtualFixture(t)
	text := "'nuget-virtual' is a virtual repository, which artifactweave does not serve through the nuget/v3 API yet"
	for _, row := range []struct{ method, path string }{{"PUT", "symbols/%2e%2e/bad"}, {"POST", "query"}, {"DELETE", "flatcontainer/bad"}} {
		r := f.doBody(row.method, virtualV3+row.path, "admin", bytes.NewReader([]byte("malformed")))
		if r.Code != 501 || row.method != "HEAD" && !strings.Contains(r.Body.String(), text) {
			t.Fatalf("%+v %d %s", row, r.Code, r.Body)
		}
	}
	for _, method := range []string{"PUT", "DELETE", "POST"} {
		r := f.do(method, "/artifactory/api/nuget/nuget-virtual/", "admin")
		want := 404
		if method == "PUT" {
			want = 405
		}
		if r.Code != want || want == 404 && r.Body.Len() != 0 || want == 405 && r.Header().Get("Allow") != "GET" {
			t.Fatal(r.Code, r.Body)
		}
	}
	for _, method := range []string{"GET", "HEAD"} {
		if r := f.do(method, virtualV3+"a.pdb/invalid/a.pdb", "admin"); r.Code != 404 {
			t.Fatal(r.Code, r.Body)
		}
	}
	f.virtualRepo(t, "nuget-virtual", func(c *repo.Config) { b := true; c.ForceNugetAuthentication = &b })
	if r := f.do("PUT", virtualV3+"symbols/%2e%2e/bad", "anonymous"); r.Code != 401 || r.Header().Get("WWW-Authenticate") == "" {
		t.Fatal(r.Code, r.Header(), r.Body)
	}
	for _, path := range []string{"flatcontainer/bad", "flatcontainer/a/bad/a.nupkg", "flatcontainer/a/1.0.0/wrong", "registration/a/bad.json", "registration/a/page/x/y.json", "registration/a/page/1/2.json", "registration/Download/a/bad", "registration/a/nope/nope", "flatcontainer/invalid!/index.json", "query/extra"} {
		r := f.do("GET", virtualV3+path, "admin")
		if r.Code != 404 {
			t.Fatalf("%s %d %s", path, r.Code, r.Body)
		}
	}
}

func TestAccVirtualSearchFiltersCandidatesBeforePaging(t *testing.T) {
	for _, mode := range []string{"policy", "query-newest-mismatch"} {
		t.Run(mode, func(t *testing.T) {
			f := newVirtualFixture(t)
			f.push("A.First", "1.0.0", "<description>needle</description>")
			f.push("B.Second", "1.0.0", "<description>needle</description>")
			path := "query?take=1"
			if mode == "policy" {
				f.virtualRepo(t, "nuget-virtual", func(c *repo.Config) { s := "A.**"; c.ExcludesPattern = &s })
			} else {
				f.push("A.First", "2.0.0", "<description>different</description>")
				path += "&q=needle"
			}
			r := f.do("GET", virtualV3+path, "reader")
			virtualJSON(t, r, 200)
			if !strings.Contains(r.Body.String(), "B.Second") || strings.Contains(r.Body.String(), "A.First") {
				t.Fatal(r.Body)
			}
		})
	}
}

func TestAccVirtualSearchAndAutocompleteUnion(t *testing.T) {
	f := newVirtualFixture(t)
	f.push("Acme.Mixed", "1.0.0", "<description>local metadata</description>")
	f.push("Zeta.Local", "1.0.0", "")
	f.packageDocs("Acme.Mixed", "2.0.0")
	f.setJSON(
		"/query",
		map[string]any{
			"totalHits": 900,
			"data": []any{
				map[string]any{
					"id":          "ACME.MIXED",
					"version":     "2.0.0",
					"description": "remote newest metadata",
					"richField":   "preserved",
					"versions":    []any{map[string]string{"version": "2.0.0"}},
				},
			},
		},
	)
	f.setJSON("/autocomplete", map[string]any{"totalHits": 900, "data": []string{"Acme.Mixed"}})
	r := f.do("GET", virtualV3+"query?skip=0&take=2", "reader")
	d := virtualJSON(t, r, 200)
	assertVirtualURLs(t, r)
	if d["totalHits"] != float64(2) || !strings.Contains(r.Body.String(), "remote newest metadata") || !strings.Contains(r.Body.String(), `"richField":"preserved"`) ||
		!strings.Contains(r.Body.String(), `"version":"1.0.0"`) {
		t.Fatal(r.Body)
	}
	r = f.do("GET", virtualV3+"query?skip=1&take=1", "reader")
	virtualJSON(t, r, 200)
	if !strings.Contains(r.Body.String(), "Zeta.Local") || strings.Contains(r.Body.String(), "ACME.MIXED") {
		t.Fatal(r.Body)
	}
	r = f.do("GET", virtualV3+"autocomplete?q=acme", "reader")
	d = virtualJSON(t, r, 200)
	if fmt.Sprint(d["data"]) != "[acme.mixed]" {
		t.Fatal(d)
	}
	f.setJSON("/autocomplete", map[string]any{"totalHits": 900, "data": []string{"2.0.0"}})
	// Invalidate only metadata caches by advancing the fixture's configured TTL.
	f.clock.Advance(6 * time.Second)
	r = f.do("GET", virtualV3+"autocomplete?id=Acme.Mixed", "reader")
	d = virtualJSON(t, r, 200)
	if fmt.Sprint(d["data"]) != "[1.0.0 2.0.0]" || d["totalHits"] != float64(2) {
		t.Fatal(d)
	}
	r = f.do("GET", virtualV3+"autocomplete?id=Acme.Mixed&skip=1&take=1", "reader")
	d = virtualJSON(t, r, 200)
	if fmt.Sprint(d["data"]) != "[2.0.0]" {
		t.Fatal(d)
	}
	f.virtualRepo(t, "nuget-remote", func(c *repo.Config) { c.PriorityResolution = repo.Flex(true) })
	r = f.do("GET", virtualV3+"query", "reader")
	d = virtualJSON(t, r, 200)
	if strings.Contains(r.Body.String(), `"version":"1.0.0","`) && !strings.Contains(r.Body.String(), "Zeta.Local") {
		t.Fatal(d)
	}
	for _, path := range []string{"query?take=0", "query?skip=9223372036854775807&take=1000", "autocomplete?take=0", "autocomplete?id=invalid!"} {
		r = f.do("GET", virtualV3+path, "reader")
		if r.Code != 200 && r.Code != 404 {
			t.Fatal(path, r.Code, r.Body)
		}
	}
}

func TestAccVirtualSearchBoundsEveryUpstreamPageAndAutocompleteAuthority(t *testing.T) {
	f := newVirtualFixture(t)
	router := chi.NewRouter()
	var upstreamURL string
	var mu sync.Mutex
	calls := []string{}
	router.Get("/v3/index.json", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).
			Encode(map[string]any{"version": "3.0.0", "resources": []map[string]string{{"@id": upstreamURL + "/query", "@type": "SearchQueryService/3.5.0"}, {"@id": upstreamURL + "/autocomplete", "@type": "SearchAutocompleteService/3.5.0"}, {"@id": upstreamURL + "/flat/", "@type": "PackageBaseAddress/3.0.0"}}})
	})
	serve := func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		skip, _ := strconv.Atoi(q.Get("skip"))
		take, _ := strconv.Atoi(q.Get("take"))
		mu.Lock()
		calls = append(calls, r.URL.RequestURI())
		mu.Unlock()
		if take > 100 || take < 0 {
			t.Errorf("unbounded upstream take %d", take)
			w.WriteHeader(400)
			return
		}
		data := []any{}
		for i := skip; i < min(skip+take, 200); i++ {
			id := fmt.Sprintf("Pkg.%03d", i)
			if r.URL.Path == "/query" {
				data = append(data, map[string]any{"id": id, "version": "1.0.0", "versions": []map[string]string{{"version": "1.0.0"}}})
			} else {
				data = append(data, id)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"totalHits": 999999, "data": data})
	}
	router.Get("/query", serve)
	router.Get("/autocomplete", serve)
	router.Get("/flat/{id}/index.json", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"versions": []string{"1.0.0"}})
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	upstreamURL = server.URL
	f.virtualRepo(t, "nuget-remote", func(c *repo.Config) {
		c.URL = server.URL
		c.V3FeedURL = server.URL + "/v3/index.json"
		zero := 0
		c.RetrievalCachePeriodSecs = &zero
	})
	r := f.do("GET", virtualV3+"query?skip=100&take=25", "reader")
	d := virtualJSON(t, r, 200)
	if d["totalHits"] != float64(125) || len(d["data"].([]any)) != 25 {
		t.Fatal(r.Body)
	}
	mu.Lock()
	got := append([]string(nil), calls...)
	calls = nil
	mu.Unlock()
	if len(got) != 2 || !strings.Contains(got[0], "take=100") || !strings.Contains(got[1], "take=25") {
		t.Fatal(got)
	}
	// The ID authority reads consume the same graph budget. Keep the first
	// 125 admitted IDs instead of dropping the entire member at read 129.
	r = f.do("GET", virtualV3+"autocomplete?take=1000", "reader")
	d = virtualJSON(t, r, 200)
	if d["totalHits"] != float64(125) || len(d["data"].([]any)) != 125 {
		t.Fatal(r.Body)
	}
	mu.Lock()
	got = append([]string(nil), calls...)
	mu.Unlock()
	if len(got) != 2 || !strings.Contains(got[1], "take=25") {
		t.Fatal(got)
	}
	r = f.do("GET", virtualV3+"query?skip=9223372036854775807&take=1000", "reader")
	d = virtualJSON(t, r, 200)
	if len(d["data"].([]any)) != 0 || d["totalHits"] != float64(200) {
		t.Fatal(r.Body)
	}
}

func TestAccVirtualV3StaleHostedPackageFallsThroughBeforeDelivery(t *testing.T) {
	f := newVirtualFixture(t)
	it := f.deploy("Acme.Mixed.1.0.0.nupkg", nupkg("Acme.Mixed", "1.0.0", "<description>stale</description>"))
	f.packageDocs("Acme.Mixed", "1.0.0")
	if _, err := f.db.Items().Delete(t.Context(), "nuget-local", false, it.FullPath()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"acme.mixed.1.0.0.nupkg", "acme.mixed.nuspec"} {
		r := f.do("GET", virtualV3+"flatcontainer/acme.mixed/1.0.0/"+name, "reader")
		if r.Code != 200 || strings.Contains(r.Body.String(), "<description>stale</description>") {
			t.Fatal(name, r.Code, r.Body)
		}
	}
}

func TestAccVirtualNuGetStorageFaultsNeverLookLikeMisses(t *testing.T) {
	for _, mode := range []string{"find", "versions", "metadata", "list", "stats", "item"} {
		t.Run(mode, func(t *testing.T) {
			f := newVirtualFixture(t)
			f.push("Acme.Mixed", "1.0.0", "")
			f.opts.Store = failingStore{Store: f.db, failing: mode}
			f.mount(t, base)
			paths := []string{"flatcontainer/acme.mixed/index.json", "registration-semver2/acme.mixed/index.json", "flatcontainer/acme.mixed/1.0.0/acme.mixed.1.0.0.nupkg"}
			if mode == "list" || mode == "page" || mode == "stats" {
				paths = []string{"query", "query?q=acme", "autocomplete", "autocomplete?id=acme.mixed"}
			}
			if mode == "item" {
				paths = []string{"flatcontainer/acme.mixed/1.0.0/acme.mixed.1.0.0.nupkg", "flatcontainer/acme.mixed/1.0.0/acme.mixed.nuspec"}
			}
			for _, path := range paths {
				r := f.do("GET", virtualV3+path, "admin")
				if r.Code != 500 {
					t.Fatalf("%s %d %s", path, r.Code, r.Body)
				}
			}
		})
	}
}

func TestAccVirtualNuGetMissingRemoteBinaryFallsThrough(t *testing.T) {
	f := newVirtualFixture(t)
	entries := f.packageDocs("Acme.Mixed", "1.0.0")
	first := f.up.URL + "/objects/acme.mixed-1.0.0.zip"
	f.up.Set("/objects/acme.mixed-1.0.0.zip", fakeupstream.Resource{Status: 404})
	good := nupkg("Acme.Mixed", "1.0.0", `<dependencies><dependency id="Acme.Dependency" version="[1.0.0]" /></dependencies>`)
	f.up.Set("/good.nupkg", fakeupstream.Resource{Body: good, Header: http.Header{"Content-Type": []string{"application/octet-stream"}}})
	replacement := strings.ReplaceAll(string(mustJSON(t, entries)), first, f.up.URL+"/good.nupkg")
	var secondEntries []map[string]any
	if err := json.Unmarshal([]byte(replacement), &secondEntries); err != nil {
		t.Fatal(err)
	}
	f.setJSON(
		"/second/index.json",
		map[string]any{
			"version": "3.0.0",
			"resources": []map[string]string{
				{"@id": f.up.URL + "/flat/", "@type": "PackageBaseAddress/3.0.0"},
				{"@id": f.up.URL + "/second/reg/", "@type": "RegistrationsBaseUrl/3.6.0"},
			},
		},
	)
	f.setJSON(
		"/second/reg/acme.mixed/index.json",
		map[string]any{"count": 1, "items": []any{map[string]any{"@id": f.up.URL + "/second/page", "count": 1, "lower": "1.0.0", "upper": "1.0.0", "items": secondEntries}}},
	)
	c := *repo.Defaults(&repo.Config{Key: "nuget-other", Class: repo.Remote, PackageType: repo.NuGet, URL: f.up.URL, V3FeedURL: f.up.URL + "/second/index.json"})
	if err := f.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	f.virtualRepo(t, "nuget-virtual", func(c *repo.Config) { c.Repositories = []string{"nuget-remote", "nuget-other"} })
	for _, name := range []string{"acme.mixed.1.0.0.nupkg", "acme.mixed.nuspec"} {
		r := f.do("GET", virtualV3+"flatcontainer/acme.mixed/1.0.0/"+name, "admin")
		if r.Code != 200 {
			t.Fatal(name, r.Code, r.Body)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAccVirtualNuGetDeliveryRevocationIsHidden(t *testing.T) {
	for _, path := range []string{"flatcontainer/acme.mixed/index.json", "registration-semver2/acme.mixed/1.0.0.json", "query", "autocomplete?id=Acme.Mixed", "flatcontainer/acme.mixed/1.0.0/acme.mixed.1.0.0.nupkg", "flatcontainer/acme.mixed/1.0.0/acme.mixed.nuspec"} {
		t.Run(path, func(t *testing.T) {
			f := newVirtualFixture(t)
			f.packageDocs("Acme.Mixed", "1.0.0")
			f.setJSON("/query", map[string]any{"data": []any{map[string]any{"id": "Acme.Mixed", "version": "1.0.0", "versions": []map[string]string{{"version": "1.0.0"}}}}})
			f.setJSON("/autocomplete", map[string]any{"data": []string{"1.0.0"}})
			original := f.svc.HTTP
			revoked := false
			f.svc.HTTP = backendFunc(func(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
				result, err := original.Read(ctx, q, p)
				if err == nil && q.Resource.Path != ".nuget/index.json" && result.Stream != nil {
					result.Stream.Body = &closeAction{ReadCloser: result.Stream.Body, after: func() { revoked = true }}
				}
				return result, err
			})
			f.svc.Revalidate = func(_ context.Context, q source.Request, _ source.Permit) error {
				if revoked && q.Resource.Path == "acme.mixed/1.0.0/acme.mixed.1.0.0.nupkg" {
					return source.ErrDenied
				}
				return nil
			}
			r := f.do("GET", virtualV3+path, "reader")
			if r.Code != 404 || strings.Contains(r.Body.String(), `"versions"`) || r.Header().Get("WWW-Authenticate") != "" {
				t.Fatal(r.Code, r.Header(), r.Body)
			}
		})
	}
}

func TestAccVirtualRemoteSearchFiltersEveryVersionBeforePaging(t *testing.T) {
	f := newVirtualFixture(t)
	f.packageDocs("Acme.Mixed", "1.0.0", "2.0.0")
	f.setJSON("/query", map[string]any{"totalHits": 999, "data": []any{
		map[string]any{"id": "Acme.Mixed", "version": "1.0.0", "versions": []map[string]string{{"version": "1.0.0"}, {"version": "2.0.0"}, {"version": "1.1.0-beta"}}},
		map[string]any{"id": "Acme.Mixed", "version": "2.0.0", "versions": []map[string]string{{"version": "2.0.0"}}},
		map[string]any{"id": "Secret.Package", "version": "1.0.0", "versions": []map[string]string{{"version": "1.0.0"}}},
		map[string]any{"id": "Acme.Mixed", "version": "1.1.0-beta", "versions": []map[string]string{{"version": "1.1.0-beta"}}},
		map[string]any{"id": "Acme.Mixed", "version": "1.0.0", "listed": false, "versions": []map[string]string{{"version": "1.0.0"}}},
	}})
	r := f.do("GET", virtualV3+"query?take=10", "partial")
	d := virtualJSON(t, r, 200)
	if d["totalHits"] != float64(1) || strings.Contains(r.Body.String(), "Secret") || strings.Contains(r.Body.String(), "2.0.0") || strings.Contains(r.Body.String(), "beta") ||
		strings.Contains(r.Body.String(), "999") {
		t.Fatal(r.Body)
	}
	f.setJSON("/autocomplete", map[string]any{"data": []string{"Secret.Package", "Acme.Missing", "Acme.Mixed"}})
	r = f.do("GET", virtualV3+"autocomplete", "partial")
	d = virtualJSON(t, r, 200)
	if fmt.Sprint(d["data"]) != "[acme.mixed]" {
		t.Fatal(r.Body)
	}
	f.setJSON("/autocomplete", map[string]any{"data": []string{"1.0.0", "2.0.0"}})
	f.clock.Advance(6 * time.Second)
	r = f.do("GET", virtualV3+"autocomplete?id=Acme.Mixed", "partial")
	d = virtualJSON(t, r, 200)
	if fmt.Sprint(d["data"]) != "[1.0.0]" {
		t.Fatal(r.Body)
	}
}
