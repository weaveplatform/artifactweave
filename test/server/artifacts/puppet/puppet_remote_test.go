package puppet_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

func TestAccPuppetRemoteCacheRefreshOfflineAndIntegrity(t *testing.T) {
	w := newRemoteWorld(t)
	data := repopath.Read(t, "test/acceptance/fixtures/puppet/acme-widget-1.0.0.tar.gz")
	manifest := repopath.Read(t, "test/acceptance/fixtures/puppet/widget/metadata.json")
	digest := sha256.Sum256(data)
	var fetches atomic.Int64
	var status atomic.Int64
	var corrupt atomic.Bool
	up := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		if status.Load() != 0 {
			out.WriteHeader(int(status.Load()))
			return
		}
		switch r.URL.Path {
		case "/v3/releases":
			if r.URL.Query().Get("module") != "acme-widget" {
				http.NotFound(out, r)
				return
			}
			out.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(out).
				Encode(map[string]any{"results": []any{map[string]any{"version": "1.0.0", "file_uri": "/v3/files/acme-widget-1.0.0.tar.gz", "file_sha256": hex.EncodeToString(digest[:]), "file_size": len(data), "metadata": json.RawMessage(manifest)}}, "pagination": map[string]any{"offset": 0, "total": 1, "next": nil}})
		case "/v3/files/acme-widget-1.0.0.tar.gz":
			http.Redirect(out, r, "/blob", http.StatusFound)
		case "/blob":
			if corrupt.Load() {
				_, _ = out.Write([]byte(strings.Repeat("x", len(data))))
			} else {
				_, _ = out.Write(data)
			}
		default:
			http.NotFound(out, r)
		}
	}))
	defer up.Close()
	w.c.PackageType = repo.Puppet
	w.c.URL = up.URL
	w.update()
	metadata := "/artifactory/api/puppet/remote/v3/releases?module=acme-widget"
	archive := "/artifactory/api/puppet/remote/v3/files/acme-widget-1.0.0.tar.gz"
	remoteStatus(t, w.request("GET", metadata, "denied", ""), 403)
	if fetches.Load() != 0 {
		t.Fatal("denied metadata reached upstream")
	}
	remoteStatus(t, w.request("GET", archive, "denied", ""), 403)
	remoteStatus(t, w.request("GET", metadata, "reader", ""), 200)
	corrupt.Store(true)
	remoteStatus(t, w.request("GET", archive, "reader", ""), 502)
	corrupt.Store(false)
	r := w.request("GET", archive, "reader", "")
	remoteStatus(t, r, 200)
	if r.Body.String() != string(data) {
		t.Fatal("archive differs")
	}
	before := fetches.Load()
	remoteStatus(t, w.request("GET", archive, "reader", ""), 200)
	if fetches.Load() != before {
		t.Fatal("cache hit refetched")
	}
	w.c.Offline = new(true)
	w.update()
	status.Store(503)
	remoteStatus(t, w.request("GET", metadata, "reader", ""), 200)
	remoteStatus(t, w.request("GET", archive, "reader", ""), 200)
	if fetches.Load() != before {
		t.Fatal("offline contacted upstream")
	}
	w.c.Offline = new(false)
	w.c.RetrievalCachePeriodSecs = new(1)
	w.c.AssumedOfflinePeriodSecs = new(0)
	w.update()
	service := item.Service{Store: w.rt.Store, Blobs: w.rt.Blobs, Formats: ecosystem.For}
	if _, err := service.Zap(
		t.Context(),
		item.Target{Repo: w.c, Cached: true, Path: "v3/releases"},
	); err != nil {
		t.Fatal(err)
	}
	// P9 retains a complete validated response during a transient upstream outage.
	remoteStatus(t, w.request("GET", metadata, "reader", ""), 200)
	if fetches.Load() <= before {
		t.Fatal("zap failed to refresh metadata")
	}
	remoteStatus(
		t,
		w.request(
			"GET",
			"/artifactory/api/puppet/remote/v3/releases?module=acme-missing",
			"reader",
			"",
		),
		503,
	)
	status.Store(0)
	remoteStatus(t, w.request("GET", metadata, "reader", ""), 200)
}

func TestAccPuppetRegisteredRoutesAndReindex(t *testing.T) {
	w := newRemoteWorld(t)
	data := repopath.Read(t, "test/acceptance/fixtures/puppet/acme-widget-1.0.0.tar.gz")
	c := *repo.Defaults(&repo.Config{Key: "puppet", Class: repo.Local, PackageType: repo.Puppet})
	if err := w.rt.Store.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	path := "/artifactory/puppet/acme-widget/acme-widget-1.0.0.tar.gz"
	remoteStatus(t, w.request("PUT", path, "denied", string(data)), 403)
	remoteStatus(t, w.request("PUT", path, "admin", "bad"), 400)
	remoteStatus(t, w.request("PUT", path, "admin", string(data)), 201)
	for _, route := range []string{"/v3/modules?query=acme-widget", "/v3/modules/acme-widget", "/v3/releases?module=acme-widget", "/v3/releases/acme-widget-1.0.0"} {
		remoteStatus(t, w.request("GET", "/artifactory/api/puppet/puppet"+route, "admin", ""), 200)
	}
	remoteStatus(
		t,
		w.request("POST", "/artifactory/api/puppet/puppet/reindex?bad=1", "admin", ""),
		400,
	)
	remoteStatus(t, w.request("POST", "/artifactory/api/puppet/puppet/reindex", "denied", ""), 403)
	remoteStatus(t, w.request("POST", "/artifactory/api/puppet/puppet/reindex", "admin", ""), 200)
	remoteStatus(t, w.request("DELETE", path, "admin", ""), 204)
	remoteStatus(
		t,
		w.request("GET", "/artifactory/api/puppet/puppet/v3/modules/acme-widget", "admin", ""),
		404,
	)
}

// puppetArchive returns an acme-widget module archive at version that holds
// only its metadata.json, and that manifest, which a Forge page advertises
// with the archive; summary tells two builds of one version apart.
func puppetArchive(t *testing.T, version, summary string) ([]byte, []byte) {
	t.Helper()
	manifest, err := json.Marshal(map[string]any{
		"name": "acme-widget", "version": version, "summary": summary,
		"dependencies": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: "acme-widget-" + version + "/metadata.json", Mode: 0o644,
		Size: int64(len(manifest)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(manifest); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes(), manifest
}

// forge is an upstream Forge that serves acme-widget's release page,
// archives and module record, and counts the requests for each path. It
// answers 404 for the module record while module is nil.
type forge struct {
	*httptest.Server
	archives map[string][]byte
	mu       sync.Mutex
	hits     map[string]int
	module   map[string]any
	// moduleQueries are the queries the module record was asked with.
	moduleQueries []string
}

func newForge(t *testing.T, summary string, versions ...string) *forge {
	t.Helper()
	f := &forge{
		archives: map[string][]byte{},
		hits:     map[string]int{},
		module: map[string]any{
			"uri":  "/v3/modules/acme-widget",
			"slug": "acme-widget",
			"name": "widget",
		},
	}
	results := []any{}
	for _, v := range versions {
		data, manifest := puppetArchive(t, v, summary)
		digest := sha256.Sum256(data)
		file := "/v3/files/acme-widget-" + v + ".tar.gz"
		f.archives[v] = data
		results = append(results, map[string]any{
			"version": v, "file_uri": file, "file_sha256": hex.EncodeToString(digest[:]),
			"file_size": len(data), "metadata": json.RawMessage(manifest),
		})
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits[r.URL.Path]++
		module := f.module
		if r.URL.Path == "/v3/modules/acme-widget" {
			f.moduleQueries = append(f.moduleQueries, r.URL.RawQuery)
		}
		f.mu.Unlock()
		if r.URL.Path == "/v3/modules/acme-widget" && module != nil {
			out.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(out).Encode(module)
			return
		}
		if r.URL.Path == "/v3/releases" && r.URL.Query().Get("module") == "acme-widget" {
			out.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(out).Encode(map[string]any{
				"results":    results,
				"pagination": map[string]any{"offset": 0, "total": len(results), "next": nil},
			})
			return
		}
		version := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v3/files/acme-widget-"),
			".tar.gz")
		if data, ok := f.archives[version]; ok {
			_, _ = out.Write(data)
			return
		}
		http.NotFound(out, r)
	}))
	t.Cleanup(f.Close)
	return f
}

// requests returns how many requests reached path, or every path when it
// is empty.
func (f *forge) requests(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for p, hits := range f.hits {
		if path == "" || p == path {
			n += hits
		}
	}
	return n
}

// wantRefusal fails unless r is the download policy's 404 with message.
func wantRefusal(t *testing.T, r *httptest.ResponseRecorder, message string) {
	t.Helper()
	var doc struct {
		Errors []struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if r.Code != 404 || json.Unmarshal(r.Body.Bytes(), &doc) != nil || len(doc.Errors) != 1 ||
		doc.Errors[0].Message != message {
		t.Fatalf("want 404 %q, got %d %s", message, r.Code, r.Body.String())
	}
}

// listed returns each version a release list answers with its advertised
// archive checksum.
func listed(t *testing.T, r *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var doc struct {
		Results []struct {
			Version    string `json:"version"`
			FileSHA256 string `json:"file_sha256"`
		} `json:"results"`
	}
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &doc) != nil {
		t.Fatalf("releases: %d %s", r.Code, r.Body.String())
	}
	out := map[string]string{}
	for _, v := range doc.Results {
		out[v.Version] = v.FileSHA256
	}
	return out
}

func checksum(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// searched returns the release versions the module search's one result
// lists, joined in its order, or "" when it finds nothing.
func searched(t *testing.T, r *httptest.ResponseRecorder) string {
	t.Helper()
	var doc struct {
		Results []struct {
			Releases []struct {
				Version string `json:"version"`
			} `json:"releases"`
		} `json:"results"`
	}
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &doc) != nil || len(doc.Results) > 1 {
		t.Fatalf("search: %d %s", r.Code, r.Body.String())
	}
	var out []string
	for _, result := range doc.Results {
		for _, rel := range result.Releases {
			out = append(out, rel.Version)
		}
	}
	return strings.Join(out, ",")
}

// A Puppet remote checks its download policy where 7.x caches a module's
// release pages, .puppet/<m>/releases.json (RTFACT-13711), for every
// listing built from them, while Read permission stays on the module
// folder, and checks each archive at its own path before anything is
// fetched (R1, R3, R4, R10). A pattern on the archives leaves the releases
// listed; one on the release pages refuses the listings but not an
// archive, whose lookup only the blackout refuses. The module search, a
// native search route, is refused by the blackout alone, at the module
// folder (R2b).
func TestAccPuppetRemotePolicyAtItsCachedPaths(t *testing.T) {
	const (
		remote  = "/artifactory/api/puppet/remote"
		pages   = "remote:.puppet/acme-widget/releases.json"
		archive = "remote:acme-widget/acme-widget-1.1.0.tar.gz"
		search  = "/v3/modules?query=acme-widget"
	)
	metadata := []string{
		"/v3/releases?module=acme-widget", "/v3/releases/acme-widget-1.1.0",
		"/v3/modules/acme-widget",
	}
	blackedOut := func(path string) string {
		return "The repository 'remote' is blacked out and cannot serve artifact '" + path + "'."
	}
	rejected := func(path string) string {
		return "The repository 'remote' rejected the artifact '" + path +
			"' due to its include/exclude pattern settings."
	}
	setup := func(t *testing.T, tune func(*repo.Config)) (*remoteWorld, *forge) {
		t.Helper()
		w := newRemoteWorld(t)
		up := newForge(t, "remote", "1.0.0", "1.1.0")
		w.c.PackageType = repo.Puppet
		w.c.URL = up.URL
		tune(&w.c)
		w.update()
		return w, up
	}
	t.Run("blacked out", func(t *testing.T) {
		w, up := setup(t, func(c *repo.Config) { c.BlackedOut = new(true) })
		remoteStatus(t, w.request("GET", remote+metadata[0], "denied", ""), 403)
		for _, route := range metadata {
			wantRefusal(t, w.request("GET", remote+route, "reader", ""), blackedOut(pages))
		}
		wantRefusal(t, w.request("GET", remote+search, "reader", ""),
			blackedOut("remote:acme-widget"))
		remoteStatus(t, w.request("GET", remote+search, "denied", ""), 403)
		wantRefusal(t, w.request("GET", remote+"/v3/files/acme-widget-1.1.0.tar.gz", "reader", ""),
			blackedOut(archive))
		if up.requests("") != 0 {
			t.Fatal("a blacked-out remote reached upstream")
		}
	})
	t.Run("release pages excluded", func(t *testing.T) {
		w, _ := setup(t, func(c *repo.Config) { c.ExcludesPattern = new(".puppet/**") })
		for _, route := range metadata {
			wantRefusal(t, w.request("GET", remote+route, "reader", ""), rejected(pages))
		}
		if got := searched(t, w.request("GET", remote+search, "reader", "")); got != "1.1.0,1.0.0" {
			t.Fatalf("search: %q", got)
		}
		r := w.request("GET", remote+"/v3/files/acme-widget-1.1.0.tar.gz", "reader", "")
		remoteStatus(t, r, 200)
	})
	t.Run("module folder excluded", func(t *testing.T) {
		w, up := setup(t, func(c *repo.Config) { c.ExcludesPattern = new("acme-widget/**") })
		if got := listed(t, w.request("GET", remote+metadata[0], "reader", "")); len(got) != 2 {
			t.Fatalf("listing: %v", got)
		}
		if got := searched(t, w.request("GET", remote+search, "reader", "")); got != "1.1.0,1.0.0" {
			t.Fatalf("search: %q", got)
		}
		wantRefusal(t, w.request("GET", remote+"/v3/files/acme-widget-1.1.0.tar.gz", "reader", ""),
			rejected(archive))
		if up.requests("/v3/files/acme-widget-1.1.0.tar.gz") != 0 {
			t.Fatal("a rejected archive was fetched")
		}
	})
	t.Run("one archive excluded", func(t *testing.T) {
		w, up := setup(t, func(c *repo.Config) {
			c.ExcludesPattern = new("**/acme-widget-1.1.0.tar.gz")
		})
		if got := listed(t, w.request("GET", remote+metadata[0], "reader", "")); len(got) != 2 ||
			got["1.1.0"] != checksum(up.archives["1.1.0"]) {
			t.Fatalf("listing: %v", got)
		}
		for _, route := range metadata[1:] {
			remoteStatus(t, w.request("GET", remote+route, "reader", ""), 200)
		}
		if got := searched(t, w.request("GET", remote+search, "reader", "")); got != "1.1.0,1.0.0" {
			t.Fatalf("search: %q", got)
		}
		wantRefusal(t, w.request("GET", remote+"/v3/files/acme-widget-1.1.0.tar.gz", "reader", ""),
			rejected(archive))
		r := w.request("GET", remote+"/v3/files/acme-widget-1.0.0.tar.gz", "reader", "")
		remoteStatus(t, r, 200)
		if !bytes.Equal(r.Body.Bytes(), up.archives["1.0.0"]) ||
			up.requests("/v3/files/acme-widget-1.1.0.tar.gz") != 0 {
			t.Fatal("archives")
		}
	})
}

// A virtual repository over a Puppet remote leaves out the remote when it
// is blacked out or its patterns reject its release pages, and keeps its
// releases when its patterns reject only their archives, with the checksum
// of the first member that serves each archive. An archive is checked in
// each member at its own path, never at the release pages a member reads
// to find it, so a remote whose pages are refused still serves it (R5 and
// its addendum). The module search, a native search route, leaves out only
// a blacked-out remote (R2b).
func TestAccPuppetVirtualOverARemoteMember(t *testing.T) {
	const virtual = "/artifactory/api/puppet/pvirtual"
	local, _ := puppetArchive(t, "1.1.0", "local")
	for _, tc := range []struct {
		name                         string
		tune                         func(*repo.Config)
		versions                     map[string]string
		fromLocal, fromRemote, unset []string
		search                       string
	}{
		{
			"archive excluded",
			func(c *repo.Config) { c.ExcludesPattern = new("**/acme-widget-1.1.0.tar.gz") },
			map[string]string{"1.0.0": "remote", "1.1.0": "local"},
			[]string{"1.1.0"},
			[]string{"1.0.0"},
			nil,
			"1.1.0,1.0.0",
		},
		{
			"blacked out",
			func(c *repo.Config) { c.BlackedOut = new(true) },
			map[string]string{"1.1.0": "local"},
			[]string{"1.1.0"},
			nil,
			[]string{"1.0.0"},
			"1.1.0",
		},
		{
			"release pages excluded",
			func(c *repo.Config) { c.ExcludesPattern = new(".puppet/**") },
			map[string]string{"1.1.0": "local"},
			nil,
			[]string{"1.0.0", "1.1.0"},
			nil,
			"1.1.0,1.0.0",
		},
		{
			"module folder excluded",
			func(c *repo.Config) { c.ExcludesPattern = new("acme-widget/**") },
			map[string]string{"1.0.0": "remote", "1.1.0": "local"},
			[]string{"1.1.0"},
			nil,
			[]string{"1.0.0"},
			"1.1.0,1.0.0",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newRemoteWorld(t)
			up := newForge(t, "remote", "1.0.0", "1.1.0")
			w.c.PackageType = repo.Puppet
			w.c.URL = up.URL
			tc.tune(&w.c)
			w.update()
			second := *repo.Defaults(&repo.Config{
				Key: "second", Class: repo.Local,
				PackageType: repo.Puppet,
			})
			if err := w.rt.Store.Repos().Create(t.Context(), second); err != nil {
				t.Fatal(err)
			}
			remoteStatus(t, w.request("PUT",
				"/artifactory/second/acme-widget/acme-widget-1.1.0.tar.gz", "admin", string(local)),
				201)
			v := *repo.Defaults(&repo.Config{
				Key: "pvirtual", Class: repo.Virtual,
				PackageType: repo.Puppet, Repositories: []string{w.c.Key, second.Key},
			})
			if err := w.rt.Store.Repos().Create(t.Context(), v); err != nil {
				t.Fatal(err)
			}
			got := listed(
				t,
				w.request("GET", virtual+"/v3/releases?module=acme-widget", "admin", ""),
			)
			want := map[string]string{}
			for version, from := range tc.versions {
				want[version] = checksum(local)
				if from == "remote" {
					want[version] = checksum(up.archives[version])
				}
			}
			if len(got) != len(want) {
				t.Fatalf("listing %v, want %v", got, want)
			}
			for version, sum := range want {
				if got[version] != sum {
					t.Fatalf("listing %v, want %v", got, want)
				}
			}
			if found := searched(t, w.request("GET", virtual+"/v3/modules?query=acme-widget",
				"admin", "")); found != tc.search {
				t.Fatalf("search %q, want %q", found, tc.search)
			}
			file := func(version string) *httptest.ResponseRecorder {
				return w.request(
					"GET",
					virtual+"/v3/files/acme-widget-"+version+".tar.gz",
					"admin",
					"",
				)
			}
			for _, version := range tc.fromLocal {
				if r := file(version); r.Code != 200 || !bytes.Equal(r.Body.Bytes(), local) {
					t.Fatalf("%s from the local member: %d %s", version, r.Code, r.Body.String())
				}
			}
			for _, version := range tc.fromRemote {
				if r := file(version); r.Code != 200 ||
					!bytes.Equal(r.Body.Bytes(), up.archives[version]) {
					t.Fatalf("%s from the remote member: %d %s", version, r.Code, r.Body.String())
				}
			}
			for _, version := range tc.unset {
				if r := file(version); r.Code != 404 ||
					!strings.Contains(r.Body.String(), "Puppet release not found") {
					t.Fatalf("%s served: %d %s", version, r.Code, r.Body.String())
				}
			}
		})
	}
}

// searchPage is the module search's answer for acme-widget with results,
// total of them.
func searchPage(total int, results ...string) string {
	const link = `"/v3/modules?query=acme-widget&limit=20&offset=0"`
	return `{"pagination":{"limit":20,"offset":0,"first":` + link + `,"previous":null,` +
		`"current":` + link + `,"next":null,"total":` + strconv.Itoa(total) + `},` +
		`"results":[` + strings.Join(results, ",") + `]}` + "\n"
}

// A Puppet remote's module record relays the module-wide fields of the
// upstream's own record, v3/modules/<m>, asked without its current release
// and release list, which the remote answers from its release pages
// (#1326) [I]. When the upstream answers 404 for the record, the remote's
// v3/modules/<m> answers 404 and its search finds nothing, while the
// module's releases stay listed: RTFACT-19130 reports a 6.8.6 remote
// answering 404 for a module the Forge served, a fault rather than an
// answer to keep.
func TestAccPuppetRemoteModuleRecordRelaysTheForge(t *testing.T) {
	const remote = "/artifactory/api/puppet/remote"
	setup := func(t *testing.T, module map[string]any) (*remoteWorld, *forge) {
		t.Helper()
		w := newRemoteWorld(t)
		up := newForge(t, "remote", "1.0.0", "1.1.0")
		up.module = module
		w.c.PackageType = repo.Puppet
		w.c.URL = up.URL
		w.update()
		return w, up
	}
	t.Run("relayed", func(t *testing.T) {
		w, up := setup(t, map[string]any{
			"uri": "/v3/modules/acme-widget", "slug": "acme-widget", "name": "widget",
			"downloads": 90210, "created_at": "2010-03-04 05:06:07 -0800",
			"updated_at": "2026-10-29 01:02:03 -0700", "deprecated_at": nil, "supported": false,
			"endorsement": "approved", "module_group": "base",
			"owner": map[string]any{
				"uri":         "/v3/users/acme",
				"slug":        "acme",
				"username":    "acme",
				"gravatar_id": "0badc0de",
			},
			"homepage_url": "https://example.invalid/acme/puppet-widget?tab=a&b", "issues_url": nil,
		})
		data := up.archives["1.1.0"]
		current := `{"uri":"/v3/releases/acme-widget-1.1.0","slug":"acme-widget-1.1.0",` +
			`"module":{"uri":"/v3/modules/acme-widget","slug":"acme-widget","name":"widget",` +
			`"owner":{"uri":"/v3/users/acme","slug":"acme","username":"acme","gravatar_id":null},` +
			`"deprecated_at":null},"version":"1.1.0","metadata":{"name":"acme-widget",` +
			`"version":"1.1.0","source":null,"author":null,"license":null,"summary":"remote",` +
			`"description":null,"project_page":null,"issues_url":null,"tags":null,` +
			`"dependencies":[]},"tags":[],"supported":false,` +
			`"file_uri":"/v3/files/acme-widget-1.1.0.tar.gz","file_size":` + strconv.Itoa(len(data)) +
			`,"file_md5":null,"file_sha256":"` + checksum(data) + `","downloads":0,` +
			`"readme":null,"changelog":null,"license":null,"created_at":null,"updated_at":null,` +
			`"deleted_at":null}`
		module := `{"uri":"/v3/modules/acme-widget","slug":"acme-widget","name":"widget",` +
			`"downloads":90210,"created_at":"2010-03-04 05:06:07 -0800",` +
			`"updated_at":"2026-10-29 01:02:03 -0700","supported":false,"endorsement":"approved",` +
			`"module_group":"base","owner":{"uri":"/v3/users/acme","slug":"acme","username":"acme",` +
			`"gravatar_id":"0badc0de"},"current_release":` + current + `,` +
			`"releases":[{"uri":"/v3/releases/acme-widget-1.1.0","version":"1.1.0"},` +
			`{"uri":"/v3/releases/acme-widget-1.0.0","version":"1.0.0"}],` +
			`"homepage_url":"https://example.invalid/acme/puppet-widget?tab=a&b","issues_url":null}`
		for route, want := range map[string]string{
			"/v3/modules/acme-widget":       module + "\n",
			"/v3/modules?query=acme-widget": searchPage(1, module),
		} {
			r := w.request("GET", remote+route, "reader", "")
			if r.Code != 200 || r.Body.String() != want {
				t.Fatalf("%s: got %d %s\nwant 200 %s", route, r.Code, r.Body.String(), want)
			}
		}
		up.mu.Lock()
		defer up.mu.Unlock()
		if len(up.moduleQueries) == 0 {
			t.Fatal("the module record was not asked for")
		}
		for _, query := range up.moduleQueries {
			if query != "exclude_fields=current_release+releases" {
				t.Fatalf("module record asked with %q", query)
			}
		}
	})
	t.Run("not served", func(t *testing.T) {
		w, _ := setup(t, nil)
		r := w.request("GET", remote+"/v3/modules/acme-widget", "reader", "")
		const missing = `{"errors":[{"message":"Puppet module not found","status":404}]}` + "\n"
		if r.Code != 404 || r.Body.String() != missing {
			t.Fatalf("module: %d %s", r.Code, r.Body.String())
		}
		r = w.request("GET", remote+"/v3/modules?query=acme-widget", "reader", "")
		if r.Code != 200 || r.Body.String() != searchPage(0) {
			t.Fatalf("search: %d %s", r.Code, r.Body.String())
		}
		if got := listed(
			t,
			w.request("GET", remote+"/v3/releases?module=acme-widget", "reader", ""),
		); len(
			got,
		) != 2 {
			t.Fatalf("releases: %v", got)
		}
	})
}
