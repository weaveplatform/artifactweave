package puppet_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// groupedForge is an upstream Forge with two modules of one release each:
// acme-widget in the base module group and acme-pe in pe_only, as
// puppetlabs-sqlserver is (RTFACT-19130). Its release list holds only the
// groups module_groups names, and only base when it names none, as its v3
// API description says of the module list; that the release list does the
// same is [I]. It serves either release's own record and archive, and
// either module's record, whatever its group. It records every request's
// path and query.
type groupedForge struct {
	*httptest.Server
	archives map[string][]byte
	mu       sync.Mutex
	asked    []string
}

// forgeGroups are the module groups of groupedForge's modules.
var forgeGroups = map[string]string{"acme-widget": "base", "acme-pe": "pe_only"}

func newGroupedForge(t *testing.T) *groupedForge {
	t.Helper()
	f := &groupedForge{archives: map[string][]byte{}}
	for name := range forgeGroups {
		f.archives[name+"-1.0.0"] = groupArchive(t, name, "1.0.0")
	}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			f.asked = append(f.asked, r.URL.RequestURI())
			f.mu.Unlock()
			next.ServeHTTP(w, r)
		})
	})
	r.Get("/v3/releases", f.releases)
	r.Get("/v3/releases/{slug}", func(w http.ResponseWriter, r *http.Request) {
		slug := chi.URLParam(r, "slug")
		if _, ok := f.archives[slug]; !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"404 Not Found","errors":["Release not found"]}`))
			return
		}
		writeForgeJSON(w, f.record(slug))
	})
	r.Get("/v3/modules/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		group, ok := forgeGroups[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeForgeJSON(w, map[string]any{"slug": name, "module_group": group})
	})
	r.Get("/v3/files/{file}", func(w http.ResponseWriter, r *http.Request) {
		data, ok := f.archives[strings.TrimSuffix(chi.URLParam(r, "file"), ".tar.gz")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	})
	f.Server = httptest.NewServer(r)
	t.Cleanup(f.Close)
	return f
}

func (f *groupedForge) releases(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("module")
	groups := strings.Fields(r.URL.Query().Get("module_groups"))
	if len(groups) == 0 {
		groups = []string{"base"}
	}
	results := []any{}
	if group, ok := forgeGroups[name]; ok && slices.Contains(groups, group) {
		results = append(results, f.record(name+"-1.0.0"))
	}
	writeForgeJSON(w, map[string]any{
		"pagination": map[string]any{
			"limit": 20, "offset": 0, "total": len(results), "next": nil,
		},
		"results": results,
	})
}

// record is the Forge's record of release slug.
func (f *groupedForge) record(slug string) map[string]any {
	name, version := slug[:strings.LastIndex(slug, "-")], slug[strings.LastIndex(slug, "-")+1:]
	data := f.archives[slug]
	_, module, _ := strings.Cut(name, "-")
	return map[string]any{
		"uri": "/v3/releases/" + slug, "slug": slug,
		"module": map[string]any{
			"uri":           "/v3/modules/" + name,
			"slug":          name,
			"name":          module,
			"deprecated_at": nil,
			"owner": ownerRecord{
				"/v3/users/acme",
				"acme",
				"acme",
				"fdd009b7c1ec96e088b389f773e87aec",
			},
		},
		"version":  version,
		"metadata": groupManifest(name, version),
		"tags":     []string{module}, "supported": true,
		"file_uri": "/v3/files/" + slug + ".tar.gz", "file_size": len(data),
		"file_md5": md5Hex(data), "file_sha256": checksum(data), "downloads": 7,
		"readme": "# " + name, "changelog": nil, "license": "Proprietary",
		"created_at": "2019-03-26 11:15:38 -0700", "updated_at": "2019-03-26 11:15:38 -0700",
		"deleted_at": nil,
	}
}

// want is the record a remote answers for release slug, relaying the
// Forge's, with the whole metadata.json in 7.x's order (#1325).
func (f *groupedForge) want(slug string) releaseRecord {
	name, version := slug[:strings.LastIndex(slug, "-")], slug[strings.LastIndex(slug, "-")+1:]
	data := f.archives[slug]
	_, module, _ := strings.Cut(name, "-")
	return releaseRecord{
		URI: "/v3/releases/" + slug, Slug: slug,
		Module: moduleRef{
			URI:  "/v3/modules/" + name,
			Slug: name,
			Name: module,
			Owner: ownerRecord{
				"/v3/users/acme",
				"acme",
				"acme",
				"fdd009b7c1ec96e088b389f773e87aec",
			},
		},
		Version: version, Metadata: groupMetadata(name, version), Tags: []string{module},
		Supported: true, FileURI: "/v3/files/" + slug + ".tar.gz", FileSize: len(data),
		FileMD5: md5Hex(data), FileSHA256: checksum(data), Downloads: 7,
		Readme: "# " + name, License: "Proprietary",
		CreatedAt: "2019-03-26 11:15:38 -0700", UpdatedAt: "2019-03-26 11:15:38 -0700",
	}
}

// requests returns the requests the Forge has had since the last call.
func (f *groupedForge) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.asked
	f.asked = nil
	return out
}

func writeForgeJSON(w http.ResponseWriter, doc any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}

// groupManifest is the metadata.json of module name at version.
func groupManifest(name, version string) map[string]any {
	return map[string]any{
		"name": name, "version": version, "summary": name, "dependencies": []any{},
	}
}

// groupMetadata is the metadata a record answers for groupManifest.
func groupMetadata(name, version string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"name":%q,"version":%q,"source":null,"author":null,`+
		`"license":null,"summary":%q,"description":null,"project_page":null,"issues_url":null,`+
		`"tags":null,"dependencies":[]}`, name, version, name))
}

// groupArchive returns an archive of module name at version that holds
// groupManifest.
func groupArchive(t *testing.T, name, version string) []byte {
	t.Helper()
	manifest, err := json.Marshal(groupManifest(name, version))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: name + "-" + version + "/metadata.json", Mode: 0o644, Size: int64(len(manifest)),
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
	return out.Bytes()
}

// encodeAnswer is doc as the Puppet routes write it: "&", "<" and ">" as
// they are.
func encodeAnswer(t *testing.T, doc any) string {
	t.Helper()
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(doc); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// groupedRemote returns a world whose remote is a Puppet remote over up,
// tuned by tune, and a local Puppet repository, plocal, that holds
// acme-widget 1.0.0.
func groupedRemote(t *testing.T, up *groupedForge, tune func(*repo.Config)) *remoteWorld {
	t.Helper()
	w := newRemoteWorld(t)
	w.c.PackageType = repo.Puppet
	w.c.URL = up.URL
	if tune != nil {
		tune(&w.c)
	}
	w.update()
	local := *repo.Defaults(&repo.Config{Key: "plocal", Class: repo.Local, PackageType: repo.Puppet})
	if err := w.rt.Store.Repos().Create(t.Context(), local); err != nil {
		t.Fatal(err)
	}
	remoteStatus(t, w.request("PUT", "/artifactory/plocal/acme-widget/acme-widget-1.0.0.tar.gz",
		"admin", string(up.archives["acme-widget-1.0.0"])), 201)
	return w
}

// The Puppet client's own fetch and search, as forge.rb sends them on
// Puppet Enterprise with module_groups=base%20pe_only (PUP-1890), answer
// 200 on a local repository and on a remote, as they answer without it
// (#1343) [I]. The remote does not pass module_groups to the Forge.
func TestAccPuppetEnterpriseClientQueries(t *testing.T) {
	up := newGroupedForge(t)
	w := groupedRemote(t, up, nil)
	const fetch = "/v3/releases?module=acme-widget&sort_by=version&exclude_fields=readme%2C" +
		"changelog%2Clicense%2Curi%2Cmodule%2Ctags%2Csupported%2Cfile_size%2Cdownloads%2C" +
		"created_at%2Cupdated_at%2Cdeleted_at"
	const search = "/v3/modules?query=acme-widget"
	type trimmed struct {
		Slug       string          `json:"slug"`
		Version    string          `json:"version"`
		Metadata   json.RawMessage `json:"metadata"`
		FileURI    string          `json:"file_uri"`
		FileMD5    string          `json:"file_md5"`
		FileSHA256 string          `json:"file_sha256"`
	}
	rec := up.want("acme-widget-1.0.0")
	wantFetch := encodeAnswer(t, struct {
		Pagination pageObject `json:"pagination"`
		Results    []trimmed  `json:"results"`
	}{
		newPageObject("/v3/releases", strings.TrimPrefix(fetch, "/v3/releases?"), 20, 0, 1),
		[]trimmed{{rec.Slug, rec.Version, rec.Metadata, rec.FileURI, rec.FileMD5, rec.FileSHA256}},
	})
	for _, key := range []string{"plocal", "remote"} {
		t.Run(key, func(t *testing.T) {
			prefix := "/artifactory/api/puppet/" + key
			r := w.request("GET", prefix+fetch+"&module_groups=base%20pe_only", "admin", "")
			if r.Code != 200 || r.Body.String() != wantFetch {
				t.Fatalf("fetch: %d %s\nwant 200 %s", r.Code, r.Body.String(), wantFetch)
			}
			plain := w.request("GET", prefix+search, "admin", "")
			r = w.request("GET", prefix+search+"&module_groups=base%20pe_only", "admin", "")
			if plain.Code != 200 || r.Code != 200 || r.Body.String() != plain.Body.String() ||
				!strings.Contains(
					r.Body.String(),
					`"total":1},"results":[{"uri":"/v3/modules/acme-widget"`,
				) {
				t.Fatalf("search: %d %s\nwant 200 %s", r.Code, r.Body.String(), plain.Body.String())
			}
		})
	}
	for _, asked := range up.requests() {
		if strings.Contains(asked, "module_groups") {
			t.Fatalf("the remote passed module_groups on: %s", asked)
		}
	}
}

// A remote answers v3/releases/<slug> for a release its Forge's release
// list leaves out, a Puppet Enterprise module's here, from the Forge's
// record of that release, as RTFACT-19130 shows a 6.8.6 remote answering
// puppetlabs-sqlserver-2.5.0 (#1344) [I]. The list still leaves it out, a
// release the Forge does not hold answers 404, and the record is read
// under the release pages' authority: Read before any request upstream, and
// the download policy at .puppet/<m>/releases.json (DV-057).
func TestAccPuppetRemoteReleaseTheForgeListLeavesOut(t *testing.T) {
	const remote = "/artifactory/api/puppet/remote"
	const exclusions = "exclude_fields=reference+tasks+plans+pdk+validation_score+malware_scan+deleted_for"
	up := newGroupedForge(t)
	w := groupedRemote(t, up, nil)
	r := w.request("GET", remote+"/v3/releases?module=acme-pe", "reader", "")
	if want := encodeAnswer(t, struct {
		Pagination pageObject `json:"pagination"`
		Results    []any      `json:"results"`
	}{newPageObject("/v3/releases", "module=acme-pe", 20, 0, 0), []any{}}); r.Code != 200 ||
		r.Body.String() != want {
		t.Fatalf("list: %d %s\nwant 200 %s", r.Code, r.Body.String(), want)
	}
	up.requests()
	want := encodeAnswer(t, up.want("acme-pe-1.0.0"))
	r = w.request("GET", remote+"/v3/releases/acme-pe-1.0.0", "reader", "")
	if r.Code != 200 || r.Body.String() != want {
		t.Fatalf("release: %d %s\nwant 200 %s", r.Code, r.Body.String(), want)
	}
	if asked := up.requests(); !slices.Contains(asked, "/v3/releases/acme-pe-1.0.0?"+exclusions) {
		t.Fatalf("the Forge was asked %q", asked)
	}
	r = w.request("HEAD", remote+"/v3/releases/acme-pe-1.0.0", "reader", "")
	if r.Code != 200 || r.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %s", r.Code, r.Body.String())
	}
	wantRefusalStatus := func(r *httptest.ResponseRecorder, status int, message string) {
		t.Helper()
		body := fmt.Sprintf(`{"errors":[{"message":%q,"status":%d}]}`+"\n", message, status)
		if r.Code != status || r.Body.String() != body {
			t.Fatalf("got %d %s\nwant %d %s", r.Code, r.Body.String(), status, body)
		}
	}
	wantRefusalStatus(w.request("GET", remote+"/v3/releases/acme-pe-9.9.9", "reader", ""), 404,
		"Puppet release not found")
	up.requests()
	remoteStatus(t, w.request("GET", remote+"/v3/releases/acme-pe-1.1.0", "denied", ""), 403)
	if asked := up.requests(); len(asked) != 0 {
		t.Fatalf("a denied caller reached the Forge: %q", asked)
	}

	refused := newGroupedForge(t)
	w = groupedRemote(t, refused, func(c *repo.Config) { c.ExcludesPattern = new(".puppet/**") })
	wantRefusal(t, w.request("GET", remote+"/v3/releases/acme-pe-1.0.0", "reader", ""),
		"The repository 'remote' rejected the artifact 'remote:.puppet/acme-pe/releases.json'"+
			" due to its include/exclude pattern settings.")
	if asked := refused.requests(); len(asked) != 0 {
		t.Fatalf("a refused read reached the Forge: %q", asked)
	}
}
