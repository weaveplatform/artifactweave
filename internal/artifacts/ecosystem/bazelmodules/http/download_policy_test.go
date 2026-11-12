package bazelmoduleshttp_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

// refusedWith asserts the download policy's 404 in the Artifactory error
// envelope, with message.
func refusedWith(t *testing.T, w *httptest.ResponseRecorder, message string) {
	t.Helper()
	var body struct {
		Errors []struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if w.Code != http.StatusNotFound || json.Unmarshal(w.Body.Bytes(), &body) != nil ||
		len(body.Errors) != 1 || body.Errors[0].Message != message {
		t.Fatalf("%d %s, want 404 %q", w.Code, w.Body, message)
	}
}

func blackedOut(key, path string) string {
	return fmt.Sprintf("The repository '%s' is blacked out and cannot serve artifact '%s:%s'.",
		key, key, path)
}

func rejected(key, path string) string {
	return fmt.Sprintf("The repository '%s' rejected the artifact '%s:%s' due to its "+
		"include/exclude pattern settings.", key, key, path)
}

func (f *fixture) patch(key string, change func(*repo.Config)) {
	f.t.Helper()
	c, err := f.db.Repos().Get(f.t.Context(), key)
	if err != nil {
		f.t.Fatal(err)
	}
	change(&c)
	if err := f.db.Repos().Update(f.t.Context(), c); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) served(location, contains string) *httptest.ResponseRecorder {
	f.t.Helper()
	w := f.request(http.MethodGet, location)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), contains) {
		f.t.Fatalf("GET %s: %d %s, want 200 with %q", location, w.Code, w.Body, contains)
	}
	return w
}

// archiveSaying is a module archive whose message.txt holds message, so two
// repositories' archives of one version differ.
func archiveSaying(t *testing.T, message string) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: "hello-1.0.0/message.txt", Mode: 0o644, Size: int64(len(message)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(message)); err != nil {
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

func integrity(body []byte) string {
	digest := sha256.Sum256(body)
	return "sha256-" + base64.StdEncoding.EncodeToString(digest[:])
}

// Every route of a local repository checks the policy after read
// permission, at the path 7.x keeps the file or document at, an archive at
// modules/<name>/<version>/<file>, and answers its 404 and message (R1,
// R10). A version whose archive the patterns reject stays listed and keeps
// its source.json (R3). A virtual repository leaves a blacked-out member
// and one whose patterns reject metadata.json out of its metadata, passes
// over a member that refuses a file, and its own patterns refuse the file
// (R5).
func TestAccBazelDownloadPolicyLocalAndVirtual(t *testing.T) {
	f := newFixture(t)
	two := *repo.Defaults(&repo.Config{Key: "bazel-two", Class: repo.Local, PackageType: repo.BazelModules})
	virtual := *repo.Defaults(&repo.Config{
		Key: "bazel-virtual", Class: repo.Virtual, PackageType: repo.BazelModules,
		Repositories: []string{f.local.Key, two.Key},
	})
	for _, c := range []repo.Config{two, virtual} {
		if err := f.db.Repos().Create(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	archives := map[string][]byte{
		f.local.Key: archiveSaying(t, "local\n"), two.Key: archiveSaying(t, "two\n"),
	}
	for _, c := range []repo.Config{f.local, two} {
		f.publish(c, "modules/hello/1.0.0/MODULE.bazel",
			[]byte("module(name = \"hello\", version = \"1.0.0\")\n"))
		f.publish(c, "modules/hello/1.0.0/hello-1.0.0.tar.gz", archives[c.Key])
	}
	local := "/artifactory/api/bazelmodules/" + f.local.Key + "/"
	via := "/artifactory/api/bazelmodules/" + virtual.Key + "/"
	const stored = "modules/hello/1.0.0/hello-1.0.0.tar.gz"
	archiveRoute := "archives/hello/1.0.0/hello-1.0.0.tar.gz"

	f.patch(f.local.Key, func(c *repo.Config) { c.BlackedOut = new(true) })
	for route, named := range map[string]string{
		"bazel_registry.json":              "bazel_registry.json",
		"modules/hello/metadata.json":      "modules/hello/metadata.json",
		"modules/hello/1.0.0/MODULE.bazel": "modules/hello/1.0.0/MODULE.bazel",
		"modules/hello/1.0.0/source.json":  "modules/hello/1.0.0/source.json",
		archiveRoute:                       stored,
	} {
		refusedWith(t, f.request(http.MethodGet, local+route), blackedOut(f.local.Key, named))
	}
	if w := f.requestAs(
		http.MethodGet,
		local+"modules/hello/metadata.json",
		"reader",
	); w.Code != 403 {
		t.Fatalf("read permission before the blackout: %d %s", w.Code, w.Body)
	}
	f.served(via+"modules/hello/metadata.json", `"1.0.0"`)
	f.served(via+"modules/hello/1.0.0/source.json", integrity(archives[two.Key]))
	if w := f.served(via+archiveRoute, ""); !bytes.Equal(w.Body.Bytes(), archives[two.Key]) {
		t.Fatal("the virtual repository served a blacked-out member's archive")
	}

	f.patch(f.local.Key, func(c *repo.Config) {
		c.BlackedOut, c.ExcludesPattern = new(false), new("**/*.tar.gz")
	})
	f.served(local+"modules/hello/metadata.json", `"1.0.0"`)
	f.served(local+"modules/hello/1.0.0/MODULE.bazel", "hello")
	f.served(local+"modules/hello/1.0.0/source.json", integrity(archives[f.local.Key]))
	refusedWith(t, f.request(http.MethodGet, local+archiveRoute), rejected(f.local.Key, stored))
	if w := f.served(via+archiveRoute, ""); !bytes.Equal(w.Body.Bytes(), archives[two.Key]) {
		t.Fatal("the virtual repository did not pass over the refusing member")
	}

	f.patch(f.local.Key, func(c *repo.Config) {
		c.ExcludesPattern = new("bazel_registry.json, **/metadata.json, **/source.json")
	})
	for _, route := range []string{
		"bazel_registry.json", "modules/hello/metadata.json", "modules/hello/1.0.0/source.json",
	} {
		refusedWith(t, f.request(http.MethodGet, local+route), rejected(f.local.Key, route))
	}
	f.served(local+archiveRoute, "")
	f.served(via+"modules/hello/metadata.json", `"1.0.0"`)
	f.served(via+"modules/hello/1.0.0/source.json", integrity(archives[two.Key]))
	f.patch(two.Key, func(c *repo.Config) { c.BlackedOut = new(true) })
	if w := f.request(http.MethodGet, via+"modules/hello/metadata.json"); w.Code != 404 {
		t.Fatalf("metadata from members that refuse it: %d %s", w.Code, w.Body)
	}

	f.patch(f.local.Key, func(c *repo.Config) { c.ExcludesPattern = new("") })
	f.patch(virtual.Key, func(c *repo.Config) { c.ExcludesPattern = new("**/*.tar.gz") })
	f.served(via+"modules/hello/metadata.json", `"1.0.0"`)
	f.served(via+"modules/hello/1.0.0/source.json", integrity(archives[f.local.Key]))
	refusedWith(t, f.request(http.MethodGet, via+archiveRoute),
		"No repository found to serve the request for "+virtual.Key+":"+stored)
}

// A remote repository's blackout refuses every route naming its path,
// without asking the upstream, and its patterns refuse the file they
// reject, at the path the read checks, rather than "Could not find Bazel
// module resource"; a version whose MODULE.bazel they reject keeps its
// source.json, whose lookups check the blackout alone (R4).
func TestAccBazelDownloadPolicyRemote(t *testing.T) {
	up := fakeupstream.New(t, fakeupstream.Options{})
	f := newFixtureWith(t, func(db *inmem.Store, items *item.Service) *source.Service {
		return remoteService(t, db, items)
	})
	c := *repo.Defaults(&repo.Config{
		Key: "bazel-remote", Class: repo.Remote, PackageType: repo.BazelModules,
		URL: up.URL + "/registry/",
	})
	if err := f.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	archiveBytes := archive(t)
	sourceBytes, _ := json.Marshal(map[string]any{
		"type": "archive", "url": up.URL + "/registry/archives/hello.tar.gz",
		"integrity": integrity(archiveBytes), "strip_prefix": "hello-1.0.0",
	})
	up.Set("/registry/modules/hello/metadata.json",
		fakeupstream.Resource{Body: []byte(`{"versions":["1.0.0"]}`)})
	up.Set("/registry/modules/hello/1.0.0/MODULE.bazel",
		fakeupstream.Resource{Body: []byte("module(name = \"hello\", version = \"1.0.0\")\n")})
	up.Set("/registry/modules/hello/1.0.0/source.json", fakeupstream.Resource{Body: sourceBytes})
	up.Set("/registry/archives/hello.tar.gz", fakeupstream.Resource{Body: archiveBytes})
	root := "/artifactory/api/bazelmodules/" + c.Key + "/"
	const archiveRoute = "archives/hello/1.0.0/source.tar.gz"

	f.patch(c.Key, func(c *repo.Config) { c.BlackedOut = new(true) })
	for _, route := range []string{
		"bazel_registry.json", "modules/hello/metadata.json", "modules/hello/1.0.0/MODULE.bazel",
		"modules/hello/1.0.0/source.json", archiveRoute, "modules/hello/1.0.0/patches/fix.patch",
	} {
		refusedWith(t, f.request(http.MethodGet, root+route), blackedOut(c.Key, route))
	}
	if n := len(up.Requests()); n != 0 {
		t.Fatalf("a blacked-out remote asked its upstream %d times", n)
	}

	f.patch(c.Key, func(c *repo.Config) {
		c.BlackedOut, c.ExcludesPattern = new(false), new("archives/**")
	})
	f.served(root+"modules/hello/metadata.json", `"1.0.0"`)
	f.served(root+"modules/hello/1.0.0/source.json", integrity(archiveBytes))
	refusedWith(t, f.request(http.MethodGet, root+archiveRoute), rejected(c.Key, archiveRoute))

	f.patch(c.Key, func(c *repo.Config) { c.ExcludesPattern = new("**/MODULE.bazel") })
	refusedWith(t, f.request(http.MethodGet, root+"modules/hello/1.0.0/MODULE.bazel"),
		rejected(c.Key, "modules/hello/1.0.0/MODULE.bazel"))
	f.served(root+"modules/hello/metadata.json", `"1.0.0"`)
	f.served(root+"modules/hello/1.0.0/source.json", integrity(archiveBytes))
	if w := f.served(root+archiveRoute, ""); !bytes.Equal(w.Body.Bytes(), archiveBytes) {
		t.Fatal("the remote archive changed")
	}
}
