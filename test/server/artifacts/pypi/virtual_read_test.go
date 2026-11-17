package pypi_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	pypifmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/pypi"
	platsqltest "github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func virtualPythonWorld(t *testing.T, mutate ...func(*config.Config)) *harness.RemoteWorld {
	t.Helper()
	w := harness.NewRemoteWorld(t, func(c *config.Config) {
		c.PyPI.SimpleJSON = true
		c.PyPI.CoreMetadata = true
		for _, change := range mutate {
			change(c)
		}
	})
	w.Repo = *repo.Defaults(&repo.Config{Key: "remote", Class: repo.Remote, PackageType: repo.PyPI, URL: w.Upstream.URL, PyPIRegistryURL: w.Upstream.URL})
	w.Update()
	for _, c := range []repo.Config{
		{Key: "first", Class: repo.Local, PackageType: repo.PyPI},
		{Key: "second", Class: repo.Federated, PackageType: repo.PyPI},
		{Key: "inner", Class: repo.Virtual, PackageType: repo.PyPI, Repositories: []string{"first", "remote"}},
		{Key: "virtual", Class: repo.Virtual, PackageType: repo.PyPI, Repositories: []string{"inner", "second", "remote"}},
	} {
		if err := w.Runtime.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
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
			Work:           w.Runtime.Work,
			FormatSettings: ecosystem.Settings{PyPI: pypifmt.Settings{SimpleJSON: true, CoreMetadata: true}},
		},
	)
	return w
}

func pythonConfig(t *testing.T, w *harness.RemoteWorld, key string, change func(*repo.Config)) {
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

func pythonPut(t *testing.T, w *harness.RemoteWorld, key, path, name, version string) []byte {
	t.Helper()
	body := pypiWheel(t, name, version)
	r := w.Request("PUT", "/artifactory/"+key+"/"+path, "admin", string(body))
	if r.Code != 201 {
		t.Fatal(r.Code, r.Body.String())
	}
	return body
}

func pythonRemote(t *testing.T, w *harness.RemoteWorld, name, version string) (string, []byte) {
	t.Helper()
	file := strings.ReplaceAll(name, "-", "_") + "-" + version + "-py3-none-any.whl"
	body := pypiWheel(t, name, version)
	sum := sha256.Sum256(body)
	w.Upstream.Set("/files/"+file, fakeupstream.Resource{Body: body, Header: http.Header{"Content-Type": {"application/octet-stream"}}})
	page := fmt.Sprintf(`<!DOCTYPE html><html><body><a href="../../files/%s#sha256=%x" data-requires-python=">=3.9">%s</a></body></html>`, file, sum, file)
	w.Upstream.Set("/simple/"+name+"/", fakeupstream.Resource{Body: []byte(page), Header: http.Header{"Content-Type": {"text/html"}}})
	w.Upstream.Set(
		"/simple/",
		fakeupstream.Resource{
			Body:   []byte(`<!DOCTYPE html><html><body><a href="` + name + `/">` + name + `</a></body></html>`),
			Header: http.Header{"Content-Type": {"text/html"}},
		},
	)
	return name + "/" + version + "/" + file, body
}

func TestAccPypiVirtualReadsAndWarehouse(t *testing.T) {
	w := virtualPythonWorld(t)
	const first = "demo/1.0/demo-1.0-py3-none-any.whl"
	body := pythonPut(t, w, "first", first, "demo", "1.0")
	secondBody := zipBytes(
		t,
		map[string]string{"demo-1.0.dist-info/METADATA": "Metadata-Version: 2.1\nName: demo\nVersion: 1.0\nRequires-Python: >=3.11\nSummary: second member info\n"},
	)
	duplicate := w.Request("PUT", "/artifactory/second/other-path/demo-1.0-py3-none-any.whl;pypi.yanked=broken", "admin", string(secondBody))
	if duplicate.Code != 201 {
		t.Fatal(duplicate.Code, duplicate.Body.String())
	}
	pythonPut(t, w, "second", "demo/2.0/demo-2.0-py3-none-any.whl", "demo", "2.0")
	pythonPut(t, w, "second", "demo/1.0/demo-1.0-py3-none-win_amd64.whl", "demo", "1.0")
	remotePath, remoteBody := pythonRemote(t, w, "demo", "3.0")
	const root = "/artifactory/api/pypi/virtual/"
	for _, accept := range []string{"text/html", "application/vnd.pypi.simple.v1+json"} {
		t.Run(accept, func(t *testing.T) {
			r := w.Request("GET", root+"simple/demo/", "admin", "", "Accept", accept)
			if r.Code != 200 || !strings.Contains(r.Body.String(), "demo-3.0-py3-none-any.whl") || !strings.Contains(r.Body.String(), "../../demo/1.0/") {
				t.Fatal(r.Code, r.Body.String())
			}
			sum := sha256.Sum256(body)
			if !strings.Contains(r.Body.String(), hex.EncodeToString(sum[:])) {
				t.Fatal("first member checksum lost", r.Body.String())
			}
			if strings.Contains(r.Body.String(), "other-path") || strings.Contains(r.Body.String(), "broken") || strings.Contains(r.Body.String(), "3.11") {
				t.Fatal("duplicate filename retained", r.Body.String())
			}
			if strings.Contains(r.Body.String(), w.Upstream.URL) {
				t.Fatal("upstream URL leaked")
			}
			cached := w.Request("GET", root+"simple/demo/", "admin", "", "Accept", accept, "If-None-Match", r.Header().Get("ETag"))
			if cached.Code != 304 {
				t.Fatal(cached.Code)
			}
			head := w.Request("HEAD", root+"simple/demo/", "admin", "", "Accept", accept)
			if head.Code != 200 || head.Body.Len() != 0 {
				t.Fatal(head.Code, head.Body.String())
			}
			list := w.Request("GET", root+"simple/", "admin", "", "Accept", accept)
			if list.Code != 200 || strings.Count(list.Body.String(), "demo") < 1 {
				t.Fatal(list.Code, list.Body.String())
			}
		})
	}
	for path, want := range map[string][]byte{first: body, remotePath: remoteBody} {
		r := w.Request("GET", root+path, "admin", "")
		if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), want) {
			t.Fatal(path, r.Code, r.Body.String())
		}
		r = w.Request("GET", root+path, "admin", "", "Range", "bytes=0-3")
		if r.Code != 206 || !bytes.Equal(r.Body.Bytes(), want[:4]) {
			t.Fatal(path, r.Code, r.Body.String())
		}
		r = w.Request("HEAD", root+path, "admin", "")
		if r.Code != 200 || r.Body.Len() != 0 {
			t.Fatal(path, r.Code)
		}
	}
	meta := w.Request("GET", root+first+".metadata", "admin", "")
	if meta.Code != 200 || !strings.Contains(meta.Body.String(), "Name: demo") {
		t.Fatal(meta.Code, meta.Body.String())
	}
	for _, suffix := range []string{"pypi/demo/json", "pypi/demo/1.0/json"} {
		r := w.Request("GET", root+suffix, "admin", "")
		doc := decodeObject(t, r.Body.String())
		if strings.Contains(r.Body.String(), "second member info") {
			t.Fatal("Warehouse info changed owner", doc)
		}
		want := 1
		if strings.Contains(suffix, "/1.0/") {
			want = 2
		}
		if _, ok := doc["releases"]; ok {
			t.Fatal("Warehouse shape includes unsupported field", doc)
		}
		if r.Code != 200 || len(doc["urls"].([]any)) != want {
			t.Fatal(r.Code, doc)
		}
		head := w.Request("HEAD", root+suffix, "admin", "")
		if head.Code != 200 || head.Body.Len() != 0 {
			t.Fatal(head.Code)
		}
	}
	for _, p := range []string{"pypi/missing/json", "pypi/demo/99/json", "simple/absent/", "absent.whl", "packages/packages/aa/bb/hash/demo.whl"} {
		r := w.Request("GET", root+p, "admin", "")
		if r.Code != 404 {
			t.Fatal(p, r.Code)
		}
	}
	for _, method := range []string{"PUT", "POST"} {
		r := w.Request(method, root, "admin", "")
		if r.Code != 405 {
			t.Fatal(method, r.Code, r.Body.String())
		}
	}
}

func TestAccPypiVirtualPriorityAndVisibility(t *testing.T) {
	w := virtualPythonWorld(t)
	pythonPut(t, w, "first", "demo/1.0/demo-1.0-py3-none-any.whl", "demo", "1.0")
	pythonPut(t, w, "second", "demo/2.0/demo-2.0-py3-none-any.whl", "demo", "2.0")
	pythonRemote(t, w, "remote-only", "1.0")
	pythonConfig(t, w, "remote", func(c *repo.Config) { c.PriorityResolution = repo.Flex(true) })
	const page = "/artifactory/api/pypi/virtual/simple/demo/"
	r := w.Request("GET", page, "admin", "")
	if r.Code != 200 || !strings.Contains(r.Body.String(), "demo-1.0") || !strings.Contains(r.Body.String(), "demo-2.0") {
		t.Fatal("absent priority hid local project", r.Code, r.Body.String())
	}
	pythonConfig(t, w, "second", func(c *repo.Config) { c.PriorityResolution = repo.Flex(true) })
	r = w.Request("GET", page, "admin", "")
	if r.Code != 200 || strings.Contains(r.Body.String(), "demo-1.0") || !strings.Contains(r.Body.String(), "demo-2.0") {
		t.Fatal(r.Code, r.Body.String())
	}
	// Reader can read only the remote by default. Give it first, leaving the
	// priority second unreadable: unreadable priority contributes no project.
	if err := w.Runtime.Store.Permissions().
		Create(t.Context(), accesstest.Permission{Name: "first-reader", Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]api.Actions{"reader": api.ActionRead}}, Targets: map[string]accesstest.Target{"first": {}}}}}); err != nil {
		t.Fatal(err)
	}
	r = w.Request("GET", page, "reader", "")
	if r.Code != 200 || !strings.Contains(r.Body.String(), "demo-1.0") || strings.Contains(r.Body.String(), "demo-2.0") {
		t.Fatal("unreadable priority hid readable project", r.Code, r.Body.String())
	}
	pythonConfig(t, w, "second", func(c *repo.Config) { c.BlackedOut = new(true) })
	r = w.Request("GET", page, "admin", "")
	if r.Code != 200 || !strings.Contains(r.Body.String(), "demo-1.0") || strings.Contains(r.Body.String(), "demo-2.0") {
		t.Fatal(r.Code, r.Body.String())
	}
	pythonConfig(t, w, "virtual", func(c *repo.Config) { c.ExcludesPattern = new("demo/**") })
	r = w.Request("GET", page, "admin", "")
	if r.Code != 404 {
		t.Fatal("virtual file excludes ignored", r.Code, r.Body.String())
	}
	pythonConfig(t, w, "virtual", func(c *repo.Config) { c.ExcludesPattern = new(".pypi/demo.*") })
	r = w.Request("GET", page, "admin", "")
	if r.Code != 404 {
		t.Fatal("virtual page excludes ignored", r.Code, r.Body.String())
	}
	pythonConfig(t, w, "virtual", func(c *repo.Config) { c.ExcludesPattern = new("") })
	pythonConfig(t, w, "first", func(c *repo.Config) { c.ExcludesPattern = new("demo/**") })
	r = w.Request("GET", page, "admin", "")
	if r.Code != 200 || !strings.Contains(r.Body.String(), "demo-1.0") {
		t.Fatal("member file policy filtered metadata", r.Code, r.Body.String())
	}
	r = w.Request("GET", "/artifactory/api/pypi/virtual/demo/1.0/demo-1.0-py3-none-any.whl", "admin", "")
	if r.Code != 404 {
		t.Fatal("member file policy ignored", r.Code)
	}
}

func TestAccPypiVirtualRemoteStorageModes(t *testing.T) {
	for _, store := range []bool{true, false} {
		t.Run(fmt.Sprint(store), func(t *testing.T) {
			w := virtualPythonWorld(t)
			w.Repo.StoreArtifactsLocally = &store
			w.Update()
			p, body := pythonRemote(t, w, "only", "1.0")
			for range 2 {
				r := w.Request("GET", "/artifactory/api/pypi/virtual/"+p, "reader", "")
				if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), body) {
					t.Fatal(r.Code, r.Body.String())
				}
			}
			pythonConfig(t, w, "remote", func(c *repo.Config) { c.Offline = new(true) })
			r := w.Request("GET", "/artifactory/api/pypi/virtual/simple/only/", "reader", "")
			want := 200
			if !store {
				want = 404
			}
			if r.Code != want {
				t.Fatal(r.Code, r.Body.String())
			}
		})
	}
}

func TestAccPypiVirtualWarehouseKeepsRemoteOwnerInfo(t *testing.T) {
	w := virtualPythonWorld(t)
	pythonPut(t, w, "first", "demo/1.0/demo-1.0-py3-none-any.whl", "demo", "1.0")
	_, body := pythonRemote(t, w, "demo", "3.0")
	sum := sha256.Sum256(body)
	document := map[string]any{
		"info": map[string]any{"name": "demo", "version": "3.0", "summary": "selected remote owner", "requires_dist": []string{"dep>=2"}},
		"urls": []any{
			map[string]any{
				"filename": "demo-3.0-py3-none-any.whl",
				"url":      w.Upstream.URL + "/files/demo-3.0-py3-none-any.whl",
				"digests":  map[string]string{"sha256": hex.EncodeToString(sum[:])},
			},
		},
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	w.Upstream.Set("/pypi/demo/3.0/json", fakeupstream.Resource{Body: encoded, Header: http.Header{"Content-Type": {"application/json"}}})
	const path = "/artifactory/api/pypi/virtual/pypi/demo/json"
	r := w.Request("GET", path, "admin", "")
	if r.Code != 200 || !strings.Contains(r.Body.String(), "selected remote owner") || !strings.Contains(r.Body.String(), "dep>=2") {
		t.Fatal(r.Code, r.Body.String())
	}
	// An upstream credential refusal remains a refusal, not a model fallback.
	w.Upstream.Set("/pypi/demo/3.0/json", fakeupstream.Resource{Status: http.StatusUnauthorized})
	zapPolicy := w.Request("POST", "/artifactory/api/zap/remote-cache", "admin", "")
	if zapPolicy.Code != 200 {
		t.Fatal(zapPolicy.Code, zapPolicy.Body.String())
	}
	r = w.Request("GET", path, "admin", "")
	if r.Code != 401 {
		t.Fatal("upstream refusal fell back", r.Code, r.Body.String())
	}
	// Warehouse metadata retains the shared source admission contract. A
	// Read-only caller gets the same direct and virtual source answer.
	if err = w.Runtime.Store.Users().Create(t.Context(), accesstest.User{Name: "viewer", PasswordHash: support.CheapHash}); err != nil {
		t.Fatal(err)
	}
	if err = w.Runtime.Store.Permissions().
		Create(t.Context(), accesstest.Permission{Name: "viewer-remote", Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]api.Actions{"viewer": api.ActionRead}}, Targets: map[string]accesstest.Target{"remote": {}}}}}); err != nil {
		t.Fatal(err)
	}
	w.Upstream.Set("/pypi/demo/3.0/json", fakeupstream.Resource{Body: encoded, Header: http.Header{"Content-Type": {"application/json"}}})
	// Zap only metadata before warming the simple page again with admin.
	zap := w.Request("POST", "/artifactory/api/zap/remote-cache", "admin", "")
	if zap.Code != 200 {
		t.Fatal(zap.Code, zap.Body.String())
	}
	warm := w.Request("GET", "/artifactory/api/pypi/remote/simple/demo/", "admin", "")
	if warm.Code != 200 {
		t.Fatal(warm.Code)
	}
	direct := w.Request("GET", "/artifactory/api/pypi/remote/pypi/demo/3.0/json", "viewer", "")
	r = w.Request("GET", path, "viewer", "")
	if r.Code != direct.Code || r.Code != 200 || !strings.Contains(r.Body.String(), "selected remote owner") {
		t.Fatal(direct.Code, r.Code, r.Body.String())
	}
}

func TestAccPypiVirtualDiscoveryPassesUpstreamRefusal(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			w := virtualPythonWorld(t)
			const path = "demo/1.0/demo-1.0-py3-none-any.whl"
			body := pythonPut(t, w, "first", path, "demo", "1.0")
			pythonConfig(t, w, "remote", func(c *repo.Config) { c.PriorityResolution = repo.Flex(true) })
			for _, p := range []string{"/simple/", "/simple/demo/"} {
				w.Upstream.Set(p, fakeupstream.Resource{Status: status})
			}
			for _, p := range []string{"simple/", "simple/demo/", "pypi/demo/json", path} {
				r := w.Request("GET", "/artifactory/api/pypi/virtual/"+p, "admin", "")
				if r.Code != 200 {
					t.Fatal(p, r.Code, r.Body.String())
				}
				if p == path && !bytes.Equal(r.Body.Bytes(), body) {
					t.Fatal("later member bytes lost")
				}
			}
			direct := w.Request("GET", "/artifactory/api/pypi/remote/simple/demo/", "admin", "")
			if direct.Code != status {
				t.Fatal("direct remote answer changed", direct.Code)
			}
		})
	}
}

// R10 keeps virtual policy on the canonical URL and the remote's own policy
// on its advertised file URL, including when nested or restricted to cache.
func TestAccPypiVirtualRemoteFilePolicyPaths(t *testing.T) {
	for _, store := range []bool{false, true} {
		t.Run(fmt.Sprintf("store-%t", store), func(t *testing.T) {
			w := virtualPythonWorld(t)
			path, body := pythonRemote(t, w, "demo", "1.0")
			pythonConfig(t, w, "virtual", func(c *repo.Config) { c.Repositories = []string{"inner"} })
			pythonConfig(t, w, "remote", func(c *repo.Config) { c.StoreArtifactsLocally = &store })
			const root = "/artifactory/api/pypi/"
			want := func(key string, status int, headers ...string) {
				t.Helper()
				r := w.Request("GET", root+key+"/"+path, "reader", "", headers...)
				if r.Code != status || status == http.StatusOK && !bytes.Equal(r.Body.Bytes(), body) {
					t.Fatalf("%s: %d %s", key, r.Code, r.Body.String())
				}
			}
			// The origin marker permits no cold upstream access through the virtual.
			want("virtual", http.StatusNotFound, "X-Artifactory-Originated", "other-node")
			if n := w.Upstream.Count("", ""); n != 0 {
				t.Fatalf("cache-only virtual contacted upstream %d times", n)
			}
			if store {
				want("remote", http.StatusOK)
			}
			pythonConfig(t, w, "remote", func(c *repo.Config) { c.IncludesPattern = new("files/**") })
			want("remote", http.StatusOK)
			want("virtual", http.StatusOK)
			cached := http.StatusNotFound
			if store {
				cached = http.StatusOK
			}
			before := w.Upstream.Count("", "")
			want("virtual", cached, "X-Artifactory-Originated", "other-node")
			if n := w.Upstream.Count("", ""); n != before {
				t.Fatalf("cache-only virtual contacted upstream %d more times", n-before)
			}
			pythonConfig(t, w, "inner", func(c *repo.Config) { c.ExcludesPattern = new("demo/**") })
			want("remote", http.StatusOK)
			want("virtual", http.StatusNotFound)
			pythonConfig(t, w, "inner", func(c *repo.Config) { c.ExcludesPattern = new("") })
			pythonConfig(t, w, "virtual", func(c *repo.Config) { c.ExcludesPattern = new("demo/**") })
			want("virtual", http.StatusNotFound)
			pythonConfig(t, w, "virtual", func(c *repo.Config) { c.ExcludesPattern = new("") })
			pythonConfig(t, w, "remote", func(c *repo.Config) { c.ExcludesPattern = new("files/**") })
			want("remote", http.StatusNotFound)
			want("virtual", http.StatusNotFound)
		})
	}
}

func TestAccPypiVirtualPostgresReads(t *testing.T) {
	dsn := platsqltest.ScratchDSN(t, platsqltest.PostgresDSN(t))
	w := virtualPythonWorld(t, func(c *config.Config) { c.Database.Driver = "postgres"; c.Database.DSN = dsn })
	const local = "demo/1.0/demo-1.0-py3-none-any.whl"
	pythonPut(t, w, "first", local, "demo", "1.0")
	remote, _ := pythonRemote(t, w, "demo", "3.0")
	for _, path := range []string{"simple/", "simple/demo/", "pypi/demo/json", "pypi/demo/1.0/json", local, local + ".metadata", remote} {
		r := w.Request("GET", "/artifactory/api/pypi/virtual/"+path, "admin", "")
		if r.Code != http.StatusOK {
			t.Fatal(path, r.Code, r.Body.String())
		}
	}
	pythonPut(t, w, "second", "demo/2.0/demo-2.0-py3-none-any.whl", "demo", "2.0")
	r := w.Request("GET", "/artifactory/api/pypi/virtual/simple/demo/", "admin", "", "Accept", "application/vnd.pypi.simple.v1+json")
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "demo-2.0") {
		t.Fatal("PostgreSQL member write missing from virtual", r.Code, r.Body.String())
	}
	pythonConfig(t, w, "virtual", func(c *repo.Config) { c.ExcludesPattern = new("demo/**") })
	r = w.Request("GET", "/artifactory/api/pypi/virtual/pypi/demo/json", "admin", "")
	if r.Code != http.StatusNotFound {
		t.Fatal("PostgreSQL virtual filtering ignored", r.Code, r.Body.String())
	}
}
