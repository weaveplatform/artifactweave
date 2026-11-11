package conan_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/md5" //nolint:gosec // Conan manifest wire format uses MD5.
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

func conanRemoteArchive(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var encoded bytes.Buffer
	gz := gzip.NewWriter(&encoded)
	tw := tar.NewWriter(gz)
	for name, content := range entries {
		if err := tw.WriteHeader(
			&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content))},
		); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

// This uses the production remote transport/cache, unlike the protocol's
// isolated source tests. Every upstream response is an independent HTTP body.
func TestAccConanRemoteCacheIntegrityAndOffline(t *testing.T) {
	w := newRemoteWorld(t)
	c := *repo.Defaults(&repo.Config{Key: "conan-up", Class: repo.Remote, PackageType: repo.Conan, URL: w.up.URL})
	if err := w.rt.Store.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	base := "/v2/conans/hello/1.0/_/_/revisions/abc"
	file := []byte("remote recipe")
	sum := md5.Sum(file) //nolint:gosec // Conan manifest wire format uses MD5.
	manifest := []byte(fmt.Sprintf("1\nconanfile.py: %s\n", hex.EncodeToString(sum[:])))
	jsonHeader := http.Header{"Content-Type": []string{"application/json"}}
	w.up.Set(
		"/v2/conans/hello/1.0/_/_/revisions",
		fakeupstream.Resource{
			Body:   []byte(`{"revisions":[{"revision":"abc","time":"2026-10-24T00:00:00Z"}]}`),
			Header: jsonHeader,
		},
	)
	w.up.Set(
		base+"/files",
		fakeupstream.Resource{
			Body:   []byte(`{"files":{"conanfile.py":{},"conanmanifest.txt":{}}}`),
			Header: jsonHeader,
		},
	)
	w.up.Set(base+"/files/conanmanifest.txt", fakeupstream.Resource{Body: manifest})
	w.up.Set(base+"/files/conanfile.py", fakeupstream.Resource{Body: file})
	path := "/artifactory/api/conan/conan-up/v2/conans/hello/1.0/_/_/revisions/abc/files/conanfile.py"
	got := w.request("GET", path, "admin", "")
	if got.Code != 200 || got.Body.String() != string(file) {
		t.Fatalf("remote: %d %s", got.Code, got.Body)
	}
	latestPath := "/artifactory/api/conan/conan-up/v2/conans/hello/1.0/_/_/latest"
	got = w.request("GET", latestPath, "admin", "")
	if got.Code != 200 {
		t.Fatalf("upstream latest metadata: %d %s", got.Code, got.Body)
	}
	if got := w.request(
		"GET",
		"/artifactory/conan-up-cache/_/hello/1.0/_/abc/export/conanfile.py",
		"admin",
		"",
	); got.Code != 200 {
		t.Fatalf("cache: %d %s", got.Code, got.Body)
	}
	w.up.Set(base+"/files/conanfile.py", fakeupstream.Resource{Body: []byte("tampered recipe")})
	got = w.request("GET", path, "admin", "")
	if got.Code != 200 || got.Body.String() != string(file) {
		t.Fatalf("cached read changed: %d %s", got.Code, got.Body)
	}
	c.Offline = new(true)
	if err := w.rt.Store.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	w.up.Delete(base + "/files/conanfile.py")
	w.up.Delete("/v2/conans/hello/1.0/_/_/revisions")
	got = w.request("GET", path, "admin", "")
	if got.Code != 200 || got.Body.String() != string(file) {
		t.Fatalf("offline cached read: %d %s", got.Code, got.Body)
	}
	got = w.request("GET", latestPath, "admin", "")
	if got.Code != 200 {
		t.Fatalf("offline cached revision metadata: %d %s", got.Code, got.Body)
	}
	c.Offline = new(false)
	if err := w.rt.Store.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	bad := "/v2/conans/hello/1.0/_/_/revisions/bad"
	w.up.Set(bad+"/files/conanmanifest.txt", fakeupstream.Resource{Body: manifest})
	w.up.Set(bad+"/files/conanfile.py", fakeupstream.Resource{Body: []byte("tampered recipe")})
	badPath := "/artifactory/api/conan/conan-up/v2/conans/hello/1.0/_/_/revisions/bad/files/conanfile.py"
	got = w.request("GET", badPath, "admin", "")
	if got.Code != 502 {
		t.Fatalf("corrupt upstream accepted: %d %s", got.Code, got.Body)
	}
	if got := w.request(
		"GET",
		"/artifactory/conan-up-cache/_/hello/1.0/_/bad/export/conanfile.py",
		"admin",
		"",
	); got.Code != 404 {
		t.Fatalf("corrupt upstream cached: %d %s", got.Code, got.Body)
	}
	redirect := "/v2/conans/hello/1.0/_/_/revisions/redir"
	w.up.Set(redirect+"/files/conanmanifest.txt", fakeupstream.Resource{Body: manifest})
	w.up.Set(
		redirect+"/files/conanfile.py",
		fakeupstream.Resource{Redirect: "/mirror/conanfile.py", Status: 302},
	)
	w.up.Set("/mirror/conanfile.py", fakeupstream.Resource{Body: file})
	redirectPath := "/artifactory/api/conan/conan-up/v2/conans/hello/1.0/_/_/revisions/redir/files/conanfile.py"
	got = w.request("GET", redirectPath, "admin", "")
	if got.Code != 200 || got.Body.String() != string(file) {
		t.Fatalf("same-origin redirect: %d %s", got.Code, got.Body)
	}
}

func TestAccConanRemoteArchiveIntegrity(t *testing.T) {
	w := newRemoteWorld(t)
	c := *repo.Defaults(&repo.Config{Key: "conan-archive", Class: repo.Remote, PackageType: repo.Conan, URL: w.up.URL})
	if err := w.rt.Store.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	sum := func(data string) string {
		digest := md5.Sum([]byte(data)) //nolint:gosec // Conan manifest wire digest.
		return hex.EncodeToString(digest[:])
	}
	recipe := "/v2/conans/hello/1.0/_/_/revisions/abc"
	packageRoot := recipe + "/packages/id1/revisions/pkg1"
	w.up.Set(recipe+"/files/conanmanifest.txt", fakeupstream.Resource{
		Body: []byte(fmt.Sprintf("1\nconanfile.py: %s\n", sum("recipe"))),
	})
	w.up.Set(packageRoot+"/files/conanmanifest.txt", fakeupstream.Resource{
		Body: []byte(fmt.Sprintf(
			"1\nconaninfo.txt: %s\npayload.bin: %s\n",
			sum("settings"), sum("payload"),
		)),
	})
	for _, tc := range []struct {
		name, root, file, stored string
		body                     []byte
	}{
		{"empty recipe archive", recipe, "conan_export.tgz", "_/hello/1.0/_/abc/export/conan_export.tgz", conanRemoteArchive(t, nil)},
		{"malformed recipe archive", recipe, "conan_export.tgz", "_/hello/1.0/_/abc/export/conan_export.tgz", []byte("not a gzip archive")},
		{"package archive omits declared file", packageRoot, "conan_package.tgz", "_/hello/1.0/_/abc/package/id1/pkg1/conan_package.tgz", conanRemoteArchive(t, map[string]string{"conaninfo.txt": "settings"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w.up.Set(tc.root+"/files/"+tc.file, fakeupstream.Resource{Body: tc.body})
			path := "/artifactory/api/conan/conan-archive" + tc.root + "/files/" + tc.file
			if rec := w.request(
				http.MethodGet,
				path,
				"admin",
				"",
			); rec.Code != http.StatusBadGateway {
				t.Fatalf("invalid Conan archive admitted: %d %s", rec.Code, rec.Body)
			}
			if rec := w.request(
				http.MethodGet,
				"/artifactory/conan-archive-cache/"+tc.stored,
				"admin",
				"",
			); rec.Code != http.StatusNotFound {
				t.Fatalf("invalid Conan archive entered cache: %d %s", rec.Code, rec.Body)
			}
		})
	}
}

// A blacked-out remote refuses its revision and file lists naming the
// request's path, and each file naming the file, without asking its
// upstream; its search keeps main's answer (R2b). A file or manifest its
// patterns reject stays listed, where the file list answered 502 and the
// revision list dropped the revision, and only its own GET is refused
// (R3, R4). A virtual repository's search passes over a blacked-out remote
// member, where the whole search failed (R5).
func TestAccConanDownloadPolicyRemote(t *testing.T) {
	w := newRemoteWorld(t)
	for _, c := range []repo.Config{
		{Key: "conan-up", Class: repo.Remote, PackageType: repo.Conan, URL: w.up.URL},
		{Key: "conan-local", Class: repo.Local, PackageType: repo.Conan},
		{Key: "conan-virt", Class: repo.Virtual, PackageType: repo.Conan, Repositories: []string{"conan-up", "conan-local"}},
	} {
		if err := w.rt.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	const recipe = "/v2/conans/hello/1.0/_/_"
	jsonHeader := http.Header{"Content-Type": []string{"application/json"}}
	w.up.Set(recipe+"/revisions", fakeupstream.Resource{
		Body:   []byte(`{"revisions":[{"revision":"abc","time":"2026-10-24T00:00:00Z"}]}`),
		Header: jsonHeader,
	})
	w.up.Set(recipe+"/revisions/abc/files", fakeupstream.Resource{
		Body:   []byte(`{"files":{"conanfile.py":{},"conanmanifest.txt":{}}}`),
		Header: jsonHeader,
	})
	manifest := fmt.Sprintf("1\nconanfile.py: %s\n", md5Hex("remote recipe"))
	w.up.Set(recipe+"/revisions/abc/files/conanmanifest.txt",
		fakeupstream.Resource{Body: []byte(manifest)})
	w.up.Set(recipe+"/revisions/abc/files/conanfile.py",
		fakeupstream.Resource{Body: []byte("remote recipe")})
	local := "/artifactory/api/conan/conan-local" + recipe + "/revisions/abc/files/"
	for file, body := range map[string]string{
		"conanfile.py":      "local recipe",
		"conanmanifest.txt": fmt.Sprintf("1\nconanfile.py: %s\n", md5Hex("local recipe")),
	} {
		if rec := w.request(
			http.MethodPut,
			local+file,
			"admin",
			body,
		); rec.Code != http.StatusCreated {
			t.Fatalf("PUT %s: %d %s", file, rec.Code, rec.Body)
		}
	}
	get := func(key, route string) *httptest.ResponseRecorder {
		t.Helper()
		return w.request(http.MethodGet, "/artifactory/api/conan/"+key+route, "admin", "")
	}
	stored := func(file string) string { return "_/hello/1.0/_/abc/export/" + file }

	patchConan(t, w.rt.Store.Repos(), "conan-up", func(c *repo.Config) { c.BlackedOut = new(true) })
	for _, route := range []string{"/latest", "/revisions", "/revisions/abc/files"} {
		conanRefused(t, get("conan-up", recipe+route),
			conanBlackedOut("conan-up", strings.TrimPrefix(recipe+route, "/")))
	}
	for _, file := range []string{"conanfile.py", "conanmanifest.txt"} {
		conanRefused(t, get("conan-up", recipe+"/revisions/abc/files/"+file),
			conanBlackedOut("conan-up", stored(file)))
	}
	conanRefused(t, get("conan-up", "/v2/conans/search?q=hello*"),
		conanBlackedOut("conan-up", ""))
	if n := len(w.up.Requests()); n != 0 {
		t.Fatalf("a blacked-out remote asked its upstream %d times", n)
	}
	conanServed(t, get("conan-virt", "/v2/conans/search?q=hello*"), "hello/1.0@_/_")
	conanServed(t, get("conan-virt", recipe+"/latest"), `"revision":"abc"`)
	conanServed(t, get("conan-virt", recipe+"/revisions/abc/files/conanfile.py"), "local recipe")
	if n := len(w.up.Requests()); n != 0 {
		t.Fatalf("a virtual repository asked a blacked-out member's upstream %d times", n)
	}

	patchConan(t, w.rt.Store.Repos(), "conan-up", func(c *repo.Config) {
		c.BlackedOut, c.ExcludesPattern = new(false), new("**/conanfile.py")
	})
	conanServed(t, get("conan-up", recipe+"/latest"), `"revision":"abc"`)
	conanServed(t, get("conan-up", recipe+"/revisions/abc/files"), "conanfile.py")
	conanRefused(t, get("conan-up", recipe+"/revisions/abc/files/conanfile.py"),
		conanRejected("conan-up", stored("conanfile.py")))
	conanServed(t, get("conan-virt", recipe+"/revisions/abc/files/conanfile.py"), "local recipe")

	patchConan(t, w.rt.Store.Repos(), "conan-up", func(c *repo.Config) {
		c.ExcludesPattern = new("**/conanmanifest.txt")
	})
	conanServed(t, get("conan-up", recipe+"/revisions"), `"revision":"abc"`)
	conanServed(t, get("conan-up", recipe+"/revisions/abc/files"), "conanmanifest.txt")
	conanRefused(t, get("conan-up", recipe+"/revisions/abc/files/conanmanifest.txt"),
		conanRejected("conan-up", stored("conanmanifest.txt")))
	conanServed(t, get("conan-up", recipe+"/revisions/abc/files/conanfile.py"), "remote recipe")
	conanServed(t, get("conan-virt", recipe+"/revisions/abc/files/conanfile.py"), "remote recipe")
}
