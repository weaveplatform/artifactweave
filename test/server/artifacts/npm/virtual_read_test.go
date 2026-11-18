package npm_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha1" //nolint:gosec // npm wire shasum/ETag
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	npmfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func virtualNPMWorld(t *testing.T, rules npmfmt.Settings) *harness.RemoteWorld {
	t.Helper()
	w := harness.NewRemoteWorld(t, func(c *config.Config) {
		c.NPM.TagLatestByPublish, c.NPM.MergeDistTagsByVersion = rules.TagLatestByPublish, rules.MergeDistTagsByVersion
		if dsn := os.Getenv("ARTIFACTWEAVE_TEST_POSTGRES_DSN"); dsn != "" {
			c.Database = config.Database{Driver: "postgres", DSN: sqltest.ScratchDSN(t, dsn)}
		}
	})
	w.Repo.PackageType = repo.NPM
	w.Update()
	w.H = harness.Router(
		harness.Config{
			Store:          w.Runtime.Store,
			Blobs:          w.Runtime.Blobs,
			Auth:           w.Runtime.Auth,
			Tokens:         w.Runtime.Tokens,
			Keys:           w.Runtime.Keys,
			RemoteSources:  w.Runtime.Artifacts.RemoteSources(),
			RemoteHTTP:     w.Runtime.Artifacts.RemoteHTTP(),
			VirtualCache:   w.Runtime.Artifacts.VirtualCache(),
			FormatSettings: ecosystem.Settings{NPM: rules},
			Logger:         slog.New(slog.DiscardHandler),
		},
	)
	for _, c := range []repo.Config{
		{Key: "first", Class: repo.Local, PackageType: repo.NPM},
		{Key: "second", Class: repo.Federated, PackageType: repo.NPM},
		{Key: "inner", Class: repo.Virtual, PackageType: repo.NPM, Repositories: []string{"first", "remote"}},
		{Key: "virtual", Class: repo.Virtual, PackageType: repo.NPM, Repositories: []string{"inner", "second", "remote"}},
	} {
		if err := w.Runtime.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

func virtualNPMConfig(t *testing.T, w *harness.RemoteWorld, key string, change func(*repo.Config)) {
	t.Helper()
	c, err := w.Runtime.Store.Repos().Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	change(&c)
	if err = w.Runtime.Store.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
}

func virtualNPMArchive(t *testing.T, name, version, marker string) []byte {
	t.Helper()
	manifest, _ := json.Marshal(map[string]any{"name": name, "version": version, "description": marker, "dependencies": map[string]string{"dependency": "1.0.0"}})
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "package/package.json", Mode: 0o644, Size: int64(len(manifest))}); err != nil {
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

func virtualNPMLocal(t *testing.T, w *harness.RemoteWorld, key, name, version, marker string) []byte {
	t.Helper()
	data := virtualNPMArchive(t, name, version, marker)
	harness.WantStatus(t, w.Request("PUT", "/artifactory/"+key+"/"+npmfmt.CanonicalPath(name, version), "admin", string(data)), 201)
	return data
}

func virtualNPMRemote(t *testing.T, w *harness.RemoteWorld, name string, versions map[string]string, tags map[string]string, mutate ...func(map[string]any)) map[string][]byte {
	t.Helper()
	manifests := map[string]any{}
	data := map[string][]byte{}
	times := map[string]string{"created": "2018-01-01T00:00:00Z", "modified": "2024-01-01T00:00:00Z"}
	for version, marker := range versions {
		body := virtualNPMArchive(t, name, version, marker)
		data[version] = body
		sha := sha1.Sum(body)
		strong := sha512.Sum512(body)
		path := "/" + npmfmt.CanonicalPath(name, version)
		manifests[version] = map[string]any{
			"name":         name,
			"version":      version,
			"description":  marker,
			"deprecated":   true,
			"vendor-field": marker,
			"dist": map[string]any{
				"tarball":     w.Upstream.URL + path,
				"shasum":      hex.EncodeToString(sha[:]),
				"integrity":   "sha512-" + base64.StdEncoding.EncodeToString(strong[:]),
				"signatures":  []map[string]string{{"sig": marker, "keyid": "vendor"}},
				"vendor-dist": marker,
			},
		}
		times[version] = "2023-01-01T00:00:00Z"
		w.Upstream.Set(path, fakeupstream.Resource{Body: body, Header: http.Header{"Content-Type": {"application/octet-stream"}}})
	}
	document := map[string]any{
		"name":        name,
		"_id":         name,
		"_rev":        "upstream",
		"versions":    manifests,
		"dist-tags":   tags,
		"time":        times,
		"readme":      "upstream readme",
		"maintainers": []map[string]string{{"name": "maintainer"}},
	}
	for _, fn := range mutate {
		fn(document)
	}
	doc, _ := json.Marshal(document)
	w.Upstream.Set("/"+name, fakeupstream.Resource{Body: doc, Header: http.Header{"Content-Type": {"application/json"}}})
	return data
}

func virtualNPMHead(t *testing.T, w *harness.RemoteWorld, path string) {
	t.Helper()
	server := httptest.NewServer(w.H)
	defer server.Close()
	var tag string
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		request, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.SetBasicAuth("admin", "pw")
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil || response.StatusCode != 200 {
			t.Fatal(response.StatusCode, string(body), readErr)
		}
		if method == http.MethodGet {
			tag = response.Header.Get("ETag")
		} else if len(body) != 0 || response.Header.Get("ETag") != tag {
			t.Fatal(response.Header, string(body), tag)
		}
	}
}

func virtualNPMJSON(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err, string(raw))
	}
	return out
}

func TestAccNPMVirtualReads(t *testing.T) {
	w := virtualNPMWorld(t, npmfmt.Settings{})
	first := virtualNPMLocal(t, w, "first", "pkg", "1.0.0", "first")
	virtualNPMLocal(t, w, "second", "pkg", "3.0.0", "second")
	remote := virtualNPMRemote(t, w, "pkg", map[string]string{"1.0.0": "duplicate", "2.0.0": "remote"}, map[string]string{"latest": "2.0.0", "custom": "2.0.0"})
	base := npmBase + "virtual/"
	r := w.Request("GET", base+"pkg", "admin", "")
	harness.WantStatus(t, r, 200)
	doc := virtualNPMJSON(t, r.Body.Bytes())
	versions := doc["versions"].(map[string]any)
	if len(versions) != 3 || doc["_id"] != "pkg" || doc["_rev"] != "1-0" || doc["dist-tags"].(map[string]any)["latest"] != "3.0.0" {
		t.Fatal(doc)
	}
	one := versions["1.0.0"].(map[string]any)
	two := versions["2.0.0"].(map[string]any)
	if one["description"] != "first" || two["deprecated"] != true || two["vendor-field"] != "remote" || two["dist"].(map[string]any)["vendor-dist"] != "remote" {
		t.Fatal(doc)
	}
	for _, tc := range []struct {
		version string
		body    []byte
	}{{"1.0.0", first}, {"2.0.0", remote["2.0.0"]}} {
		manifest := versions[tc.version].(map[string]any)
		if !strings.Contains(manifest["dist"].(map[string]any)["tarball"].(string), base+"pkg/-/pkg-"+tc.version+".tgz") {
			t.Fatal(manifest)
		}
		for _, method := range []string{"GET", "HEAD"} {
			got := w.Request(method, base+npmfmt.CanonicalPath("pkg", tc.version), "admin", "")
			harness.WantStatus(t, got, 200)
			if method == "GET" && !bytes.Equal(got.Body.Bytes(), tc.body) || method == "HEAD" && got.Body.Len() != 0 {
				t.Fatal(method, got.Body.String())
			}
		}
	}
	got := w.Request("GET", base+"pkg/2.0.0", "admin", "")
	harness.WantStatus(t, got, 200)
	if selected := virtualNPMJSON(t, got.Body.Bytes()); selected["deprecated"] != true || selected["vendor-field"] != "remote" {
		t.Fatal(selected)
	}
	tag := w.Request("GET", base+"-/package/pkg/dist-tags", "admin", "")
	harness.WantStatus(t, tag, 200)
	if virtualNPMJSON(t, tag.Body.Bytes())["custom"] != "2.0.0" {
		t.Fatal(tag.Body.String())
	}
	harness.WantStatus(t, w.Request("GET", base+"-/package/missing/dist-tags", "admin", ""), 404)
	sha := sha1.Sum(r.Body.Bytes())
	if r.Header().Get("ETag") != hex.EncodeToString(sha[:]) || r.Header().Get("X-Checksum-Sha1") != r.Header().Get("ETag") {
		t.Fatal(r.Header())
	}
	for _, method := range []string{"GET", "HEAD"} {
		conditional := w.Request(method, base+"pkg", "admin", "", "If-None-Match", r.Header().Get("ETag"))
		harness.WantStatus(t, conditional, 304)
		if conditional.Body.Len() != 0 {
			t.Fatal(conditional.Body.String())
		}
	}
	virtualNPMHead(t, w, base+"pkg")
	virtualNPMHead(t, w, npmBase+"first/pkg")
	short := w.Request("GET", base+"pkg", "admin", "", "Accept", "application/vnd.npm.install-v1+json")
	harness.WantStatus(t, short, 200)
	if short.Header().Get("Content-Type") != "application/vnd.npm.install-v1+json" || strings.Contains(short.Body.String(), "description") {
		t.Fatal(short.Header(), short.Body.String())
	}
	write := w.Request("GET", base+"pkg?write=true", "admin", "", "Accept", "application/vnd.npm.install-v1+json")
	if write.Code != 200 || !bytes.Equal(write.Body.Bytes(), r.Body.Bytes()) {
		t.Fatal(write.Code, write.Body.String())
	}
	override := w.Request("GET", base+"pkg", "admin", "", "X-JFrog-Override-Base-URL", "https://override.example")
	harness.WantStatus(t, override, 200)
	if !strings.Contains(override.Body.String(), "https://override.example"+base) || override.Header().Get("ETag") == r.Header().Get("ETag") {
		t.Fatal(override.Body.String())
	}
	again := w.Request("GET", base+"pkg", "admin", "")
	if !bytes.Equal(again.Body.Bytes(), r.Body.Bytes()) {
		t.Fatal("render mutated cached model")
	}
	virtualNPMRemote(t, w, "@scope/only", map[string]string{"1.0.0": "scoped"}, map[string]string{"latest": "1.0.0"})
	scoped := w.Request("GET", base+"@scope%2fonly", "admin", "")
	harness.WantStatus(t, scoped, 200)
	if !strings.Contains(scoped.Body.String(), "upstream readme") || !strings.Contains(scoped.Body.String(), "@scope/only/-/@scope/only-1.0.0.tgz") {
		t.Fatal(scoped.Body.String())
	}
}

func TestAccNPMVirtualPriorityAndCache(t *testing.T) {
	w := virtualNPMWorld(t, npmfmt.Settings{})
	virtualNPMLocal(t, w, "first", "pkg", "1.0.0", "first")
	virtualNPMRemote(t, w, "pkg", map[string]string{"2.0.0": "remote"}, map[string]string{"latest": "2.0.0"})
	virtualNPMConfig(t, w, "first", func(c *repo.Config) { c.PriorityResolution = repo.Flex(true) })
	get := func() map[string]any {
		t.Helper()
		r := w.Request("GET", npmBase+"virtual/pkg", "admin", "")
		harness.WantStatus(t, r, 200)
		return virtualNPMJSON(t, r.Body.Bytes())
	}
	if len(get()["versions"].(map[string]any)) != 1 || len(w.Upstream.Requests()) != 0 {
		t.Fatal("priority hit read ordinary members", w.Upstream.Requests())
	}
	virtualNPMConfig(t, w, "first", func(c *repo.Config) { c.PriorityResolution = repo.Flex(false) })
	// Visibility/membership stays fixed across writes: R8 keeps the old model.
	virtualNPMConfig(t, w, "virtual", func(c *repo.Config) { c.VirtualRetrievalCachePeriodSecs = new(-1) })
	if len(get()["versions"].(map[string]any)) != 2 {
		t.Fatal("missing remote")
	}
	virtualNPMConfig(t, w, "virtual", func(c *repo.Config) { c.VirtualRetrievalCachePeriodSecs = new(7200) })
	before := w.Upstream.Count("GET", "/pkg")
	virtualNPMLocal(t, w, "first", "pkg", "4.0.0", "new")
	if len(get()["versions"].(map[string]any)) != 2 || w.Upstream.Count("GET", "/pkg") != before {
		t.Fatal("warm metadata cache read members")
	}
	virtualNPMConfig(t, w, "virtual", func(c *repo.Config) { c.VirtualRetrievalCachePeriodSecs = new(-1) })
	if len(get()["versions"].(map[string]any)) != 3 {
		t.Fatal("-1 did not remerge")
	}
	virtualNPMLocal(t, w, "first", "pkg", "5.0.0", "newer")
	if len(get()["versions"].(map[string]any)) != 4 {
		t.Fatal("-1 reused a model")
	}
	virtualNPMConfig(t, w, "first", func(c *repo.Config) { c.PriorityResolution = repo.Flex(true) })
	virtualNPMRemote(t, w, "remote-only", map[string]string{"1.0.0": "remote"}, map[string]string{"latest": "1.0.0"})
	harness.WantStatus(t, w.Request("GET", npmBase+"virtual/remote-only", "admin", ""), 200)
}

func TestAccNPMVirtualDeferredRoutes(t *testing.T) {
	w := virtualNPMWorld(t, npmfmt.Settings{})
	for _, tc := range []struct{ method, path, body string }{
		{"PUT", "pkg", "not json"},
		{"DELETE", "pkg/-rev/bad", "not json"},
		{"PUT", "-/user/org.couchdb.user:alice", "not json"},
		{"DELETE", "-/user/token/invalid", ""},
		{"GET", "-/ping", ""},
		{"GET", "-/whoami", ""},
		{"GET", "-/v1/search?size=not-a-number", ""},
		{"GET", "-/npm/v1/keys", ""},
		{"PUT", "-/package/pkg/dist-tags/not%20valid", "not json"},
		{"GET", "-/package/pkg/not-dist-tags", ""},
	} {
		r := w.Request(tc.method, npmBase+"virtual/"+tc.path, "reader", tc.body)
		if r.Code != 501 || !strings.Contains(r.Body.String(), "'virtual' is a virtual repository, which artifactweave does not serve through the npm API yet") {
			t.Fatal(tc, r.Code, r.Body.String())
		}
	}
}

func TestAccNPMVirtualVisibilityAndPolicies(t *testing.T) {
	w := virtualNPMWorld(t, npmfmt.Settings{})
	virtualNPMLocal(t, w, "first", "pkg", "1.0.0", "first")
	virtualNPMLocal(t, w, "second", "pkg", "2.0.0", "second")
	virtualNPMLocal(t, w, "second", "private", "1.0.0", "private")
	for _, name := range []string{"left", "right"} {
		if err := w.Runtime.Store.Users().Create(t.Context(), accesstest.User{Name: name, PasswordHash: support.CheapHash}); err != nil {
			t.Fatal(err)
		}
	}
	for _, entry := range []struct{ user, member string }{{"left", "first"}, {"right", "second"}} {
		if err := w.Runtime.Store.Permissions().
			Create(t.Context(), accesstest.Permission{Name: entry.user, Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]api.Actions{entry.user: api.ActionRead}}, Targets: map[string]accesstest.Target{entry.member: {}}}}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, entry := range []struct{ user, version string }{{"left", "1.0.0"}, {"right", "2.0.0"}, {"left", "1.0.0"}} {
		r := w.Request("GET", npmBase+"virtual/pkg", entry.user, "")
		harness.WantStatus(t, r, 200)
		versions := virtualNPMJSON(t, r.Body.Bytes())["versions"].(map[string]any)
		if len(versions) != 1 || versions[entry.version] == nil {
			t.Fatal(entry, versions)
		}
	}
	harness.WantStatus(t, w.Request("GET", npmBase+"virtual/private", "left", ""), 404)
	harness.WantStatus(t, w.Request("GET", npmBase+"virtual/pkg", "denied", ""), 403)
	for _, change := range []func(*repo.Config){func(c *repo.Config) { c.BlackedOut = new(true) }, func(c *repo.Config) { c.BlackedOut = new(false); c.ExcludesPattern = new(".npm/pkg/**") }} {
		virtualNPMConfig(t, w, "first", change)
		r := w.Request("GET", npmBase+"virtual/pkg", "admin", "")
		harness.WantStatus(t, r, 200)
		if versions := virtualNPMJSON(t, r.Body.Bytes())["versions"].(map[string]any); len(versions) != 1 || versions["2.0.0"] == nil {
			t.Fatal(versions)
		}
	}
	virtualNPMConfig(t, w, "virtual", func(c *repo.Config) { c.ExcludesPattern = new(".npm/pkg/**") })
	r := w.Request("GET", npmBase+"virtual/pkg", "admin", "")
	if r.Code != 404 || !strings.Contains(r.Body.String(), "No repository found to serve the request for virtual:.npm/pkg/package.json") {
		t.Fatal(r.Code, r.Body.String())
	}
	harness.WantStatus(t, w.Request("GET", npmBase+"virtual/pkg", "denied", ""), 403)
}

func TestAccNPMVirtualRemoteChecksumSpelling(t *testing.T) {
	w := virtualNPMWorld(t, npmfmt.Settings{})
	var declared string
	data := virtualNPMRemote(t, w, "pkg", map[string]string{"1.0.0": "remote"}, map[string]string{"latest": "1.0.0"}, func(doc map[string]any) {
		dist := doc["versions"].(map[string]any)["1.0.0"].(map[string]any)["dist"].(map[string]any)
		declared = "  " + strings.ToUpper(dist["shasum"].(string)) + " "
		dist["shasum"] = declared
	})
	r := w.Request("GET", npmBase+"virtual/pkg", "admin", "")
	harness.WantStatus(t, r, 200)
	if virtualNPMJSON(t, r.Body.Bytes())["versions"].(map[string]any)["1.0.0"].(map[string]any)["dist"].(map[string]any)["shasum"] != declared {
		t.Fatal(r.Body.String())
	}
	r = w.Request("GET", npmBase+"virtual/"+npmfmt.CanonicalPath("pkg", "1.0.0"), "admin", "")
	harness.WantStatus(t, r, 200)
	if !bytes.Equal(r.Body.Bytes(), data["1.0.0"]) {
		t.Fatal("wrong tarball")
	}
}

func TestAccNPMVirtualWinnerAndEviction(t *testing.T) {
	for _, same := range []bool{false, true} {
		t.Run(strconv.FormatBool(same), func(t *testing.T) {
			w := virtualNPMWorld(t, npmfmt.Settings{})
			body := virtualNPMLocal(t, w, "second", "pkg", "1.0.0", "winner")
			get := func() *httptest.ResponseRecorder { return w.Request("GET", npmBase+"virtual/pkg", "admin", "") }
			harness.WantStatus(t, get(), 200)
			// Adding an earlier duplicate does not change the cached advertised bytes.
			virtualNPMLocal(t, w, "first", "pkg", "1.0.0", "new duplicate")
			r := w.Request("GET", npmBase+"virtual/"+npmfmt.CanonicalPath("pkg", "1.0.0"), "admin", "")
			harness.WantStatus(t, r, 200)
			if !bytes.Equal(r.Body.Bytes(), body) {
				t.Fatal("cached winner changed")
			}
			if same {
				harness.WantStatus(t, w.Request("PUT", "/artifactory/first/"+npmfmt.CanonicalPath("pkg", "1.0.0"), "admin", string(body)), 201)
			}
			harness.WantStatus(t, w.Request("DELETE", "/artifactory/second/"+npmfmt.CanonicalPath("pkg", "1.0.0"), "admin", ""), 204)
			r = w.Request("GET", npmBase+"virtual/"+npmfmt.CanonicalPath("pkg", "1.0.0"), "admin", "")
			if same {
				harness.WantStatus(t, r, 200)
				if !bytes.Equal(r.Body.Bytes(), body) {
					t.Fatal("equal digest fallback changed bytes")
				}
			} else {
				harness.WantStatus(t, r, 404)
			}
			// A contributing 404 drops the old model even before its 7200-second TTL.
			r = get()
			harness.WantStatus(t, r, 200)
			version := virtualNPMJSON(t, r.Body.Bytes())["versions"].(map[string]any)["1.0.0"].(map[string]any)
			if !same && version["description"] != "new duplicate" {
				t.Fatal("contributing 404 did not evict", version)
			}
		})
	}
}

func TestAccNPMVirtualDirectFilesAndRemotePolicy(t *testing.T) {
	w := virtualNPMWorld(t, npmfmt.Settings{})
	body := virtualNPMLocal(t, w, "first", "pkg", "1.0.0", "local")
	virtualNPMConfig(t, w, "virtual", func(c *repo.Config) { c.ExcludesPattern = new(".npm/**") })
	r := w.Request("GET", npmBase+"virtual/"+npmfmt.CanonicalPath("pkg", "1.0.0"), "admin", "")
	harness.WantStatus(t, r, 200)
	if !bytes.Equal(r.Body.Bytes(), body) {
		t.Fatal("document pattern hid strict-layout file")
	}
	virtualNPMConfig(t, w, "virtual", func(c *repo.Config) { c.ExcludesPattern = new(""); c.VirtualRetrievalCachePeriodSecs = new(-1) })
	for _, status := range []int{502, 504} {
		w.Upstream.Set("/pkg", fakeupstream.Resource{Status: status})
		r = w.Request("GET", npmBase+"virtual/pkg", "admin", "")
		harness.WantStatus(t, r, 200)
		if len(virtualNPMJSON(t, r.Body.Bytes())["versions"].(map[string]any)) != 1 {
			t.Fatal(r.Body.String())
		}
	}
	data := virtualNPMRemote(t, w, "remote-only", map[string]string{"1.0.0": "remote"}, map[string]string{"latest": "1.0.0"})
	path := npmBase + "virtual/" + npmfmt.CanonicalPath("remote-only", "1.0.0")
	r = w.Request("GET", path, "reader", "")
	harness.WantStatus(t, r, 200)
	if !bytes.Equal(r.Body.Bytes(), data["1.0.0"]) {
		t.Fatal("remote bytes")
	}
	before := len(w.Upstream.Requests())
	virtualNPMConfig(t, w, "remote", func(c *repo.Config) { c.Offline = new(true) })
	harness.WantStatus(t, w.Request("GET", path, "reader", ""), 200)
	harness.WantStatus(t, w.Request("GET", npmBase+"virtual/missing", "reader", ""), 404)
	if len(w.Upstream.Requests()) != before {
		t.Fatal("offline member contacted origin")
	}
	// Source offline policy permits immutable cached bytes, but no cold file.
	virtualNPMConfig(t, w, "remote", func(c *repo.Config) { c.URL = w.Upstream.URL + "/changed" })
	harness.WantStatus(t, w.Request("GET", npmBase+"virtual/"+npmfmt.CanonicalPath("cold", "1.0.0"), "reader", ""), 404)
}

func TestAccNPMVirtualTagSettings(t *testing.T) {
	for _, byPublish := range []bool{false, true} {
		for _, byVersion := range []bool{false, true} {
			t.Run(fmt.Sprintf("%t-%t", byPublish, byVersion), func(t *testing.T) {
				w := virtualNPMWorld(t, npmfmt.Settings{TagLatestByPublish: byPublish, MergeDistTagsByVersion: byVersion})
				virtualNPMLocal(t, w, "first", "pkg", "1.0.0", "newest publish")
				harness.WantStatus(t, w.Request("PUT", npmBase+"first/-/package/pkg/dist-tags/custom", "admin", `"1.0.0"`), 200)
				virtualNPMRemote(t, w, "pkg", map[string]string{"3.0.0": "old publish"}, map[string]string{"latest": "3.0.0", "custom": "3.0.0"})
				r := w.Request("GET", npmBase+"virtual/-/package/pkg/dist-tags", "admin", "")
				harness.WantStatus(t, r, 200)
				tags := virtualNPMJSON(t, r.Body.Bytes())
				latest, custom := "3.0.0", "1.0.0"
				if byPublish {
					latest = "1.0.0"
				}
				if byVersion {
					custom = "3.0.0"
				}
				if tags["latest"] != latest || tags["custom"] != custom {
					t.Fatal(tags, latest, custom)
				}
			})
		}
	}
}

func TestAccNPMVirtualUpstreamCredentialRefusalFallsThrough(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			w := virtualNPMWorld(t, npmfmt.Settings{})
			body := virtualNPMLocal(t, w, "first", "pkg", "1.0.0", "same")
			virtualNPMConfig(t, w, "remote", func(c *repo.Config) { c.PriorityResolution = repo.Flex(true) })
			w.Upstream.Set("/pkg", fakeupstream.Resource{Status: status})
			r := w.Request("GET", npmBase+"virtual/pkg", "admin", "")
			harness.WantStatus(t, r, 200)
			virtualNPMRemote(t, w, "remote-file", map[string]string{"1.0.0": "same"}, map[string]string{"latest": "1.0.0"})
			body = virtualNPMLocal(t, w, "first", "remote-file", "1.0.0", "same")
			harness.WantStatus(t, w.Request("GET", npmBase+"virtual/remote-file", "admin", ""), 200)
			file := "/" + npmfmt.CanonicalPath("remote-file", "1.0.0")
			w.Upstream.Set(file, fakeupstream.Resource{Status: status})
			r = w.Request("GET", npmBase+"virtual"+file, "admin", "")
			harness.WantStatus(t, r, 200)
			if !bytes.Equal(r.Body.Bytes(), body) || r.Header().Get("WWW-Authenticate") != "" {
				t.Fatal(r.Header(), "fallback bytes/challenge")
			}
		})
	}
}
