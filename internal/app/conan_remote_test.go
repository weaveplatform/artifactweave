package app

import (
	"crypto/md5" //nolint:gosec // Conan manifest wire format uses MD5.
	"encoding/hex"
	"fmt"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// This uses the production remote transport/cache, unlike the protocol's
// isolated source tests. Every upstream response is an independent HTTP body.
func TestConanRemoteCacheIntegrityAndOffline(t *testing.T) {
	w := newRemoteWorld(t)
	c := *repo.Defaults(&repo.Config{Key: "conan-up", Class: repo.Remote, PackageType: repo.Conan, URL: w.up.URL})
	if err := w.rt.store.Repos().Create(t.Context(), c); err != nil {
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
	if err := w.rt.store.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	w.up.Delete(base + "/files/conanfile.py")
	got = w.request("GET", path, "admin", "")
	if got.Code != 200 || got.Body.String() != string(file) {
		t.Fatalf("offline cached read: %d %s", got.Code, got.Body)
	}
	c.Offline = new(false)
	if err := w.rt.store.Repos().Update(t.Context(), c); err != nil {
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
