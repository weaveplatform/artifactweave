package cocoapodshttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	podfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/cocoapods"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

// seedArchive indexes version of pod name in repository key and stores its
// archive with body, as a deploy leaves them.
func (f *routeFixture) seedArchive(key, name, version, body string) {
	f.t.Helper()
	ctx := f.t.Context()
	p, err := f.db.Packages().
		Ensure(ctx, artmodel.Package{RepoKey: key, Type: repo.CocoaPods, Name: name})
	if err != nil {
		f.t.Fatal(err)
	}
	metadata, err := json.Marshal(podfmt.Archive{
		Name: name, Version: version,
		Spec: []byte(`{"name":"` + name + `","version":"` + version + `"}`),
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.db.Packages().PutVersion(ctx, artmodel.PackageVersion{
		PackageID: p.ID, Version: version, Metadata: metadata,
	}); err != nil {
		f.t.Fatal(err)
	}
	u, err := f.blobs.BeginUpload(ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := u.Append(ctx, strings.NewReader(body)); err != nil {
		f.t.Fatal(err)
	}
	info, err := u.Commit(ctx, blob.Digests{})
	if err != nil {
		f.t.Fatal(err)
	}
	sums := kernel.Checksums{
		SHA256: info.Digests.SHA256, SHA1: info.Digests.SHA1, MD5: info.Digests.MD5,
	}
	if _, err := f.db.Blobs().
		Ref(ctx, blob.Blob{Checksums: sums, Size: int64(len(body))}, 1); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.db.Items().Put(ctx, artmodel.Item{
		RepoKey: key, Path: name + "/" + version, Name: name + "-" + version + ".tar.gz",
		Size: int64(len(body)), Actual: sums,
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *routeFixture) patch(key string, change func(*repo.Config)) {
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

// wantRefusal fails unless resp is the Artifactory error document with 404
// and message.
func wantRefusal(t *testing.T, label string, resp *httptest.ResponseRecorder, message string) {
	t.Helper()
	want := `{"errors":[{"message":"` + message + `","status":404}]}` + "\n"
	if resp.Code != http.StatusNotFound || resp.Body.String() != want ||
		resp.Header().Get("ETag") != "" {
		t.Errorf("%s: %d %q (ETag %q), want 404 %q", label, resp.Code, resp.Body,
			resp.Header().Get("ETag"), want)
	}
}

func blackedOut(key, path string) string {
	return "The repository '" + key + "' is blacked out and cannot serve artifact '" +
		key + ":" + path + "'."
}

func rejected(key, path string) string {
	return "The repository '" + key + "' rejected the artifact '" + key + ":" + path +
		"' due to its include/exclude pattern settings."
}

// TestUnitDownloadPolicyRefusesLocalCDNDocumentsAtTheirStoredPath: a local
// repository's CDN documents are refused at .pod/cdn/<path>, where 7.x keeps
// them, by a blackout, administrators included, and by a pattern on that
// path, after read permission; a pattern on one pod's archive leaves the
// pod listed and its spec served, and refuses only the archive (#1237).
func TestUnitDownloadPolicyRefusesLocalCDNDocumentsAtTheirStoredPath(t *testing.T) {
	f := newRouteFixture(t)
	f.seedArchive("pods-local", "LocalPod", "1.0.0", "local archive")
	archive := podfmt.Path("LocalPod", "1.0.0")
	documents := []string{
		"CocoaPods-version.yml", "deprecated_podspecs.txt", "all_pods.txt",
		podfmt.ShardFile("LocalPod"), podfmt.SpecPath("LocalPod", "1.0.0"),
	}
	on, off := true, false
	f.patch("pods-local", func(c *repo.Config) { c.BlackedOut = &on })
	for _, path := range documents {
		wantRefusal(t, "blacked out "+path, f.get("pods-local", path),
			blackedOut("pods-local", ".pod/cdn/"+path))
	}
	wantRefusal(t, "blacked-out archive", f.get("pods-local", "packages/"+archive),
		blackedOut("pods-local", archive))
	if resp := f.getAs("nobody", "pods-local", "all_pods.txt"); resp.Code != http.StatusForbidden {
		t.Errorf("blacked out, unreadable: %d %s", resp.Code, resp.Body)
	}
	excluded := ".pod/**"
	f.patch("pods-local", func(c *repo.Config) { c.BlackedOut, c.ExcludesPattern = &off, &excluded })
	for _, path := range documents {
		wantRefusal(t, "excluded "+path, f.get("pods-local", path),
			rejected("pods-local", ".pod/cdn/"+path))
	}
	if resp := f.get("pods-local", "packages/"+archive); resp.Code != http.StatusOK ||
		resp.Body.String() != "local archive" {
		t.Errorf("archive beside excluded documents: %d %s", resp.Code, resp.Body)
	}
	excluded = archive
	f.patch("pods-local", func(c *repo.Config) { c.ExcludesPattern = &excluded })
	for path, want := range map[string]string{
		"all_pods.txt":                       "LocalPod\n",
		podfmt.ShardFile("LocalPod"):         "LocalPod/1.0.0\n",
		podfmt.SpecPath("LocalPod", "1.0.0"): `"name":"LocalPod"`,
	} {
		if resp := f.get("pods-local", path); resp.Code != http.StatusOK ||
			!strings.Contains(resp.Body.String(), want) {
			t.Errorf("excluded archive, %s: %d %s", path, resp.Code, resp.Body)
		}
	}
	wantRefusal(t, "excluded archive", f.get("pods-local", "packages/"+archive),
		rejected("pods-local", archive))
}

// TestUnitDownloadPolicyThroughAVirtualRepository: a virtual repository's
// index leaves out a blacked-out member and one whose patterns reject the
// document, and keeps a pod whose archive a member's patterns reject; its
// archive download passes over a member that refuses the archive for the
// next one that lists it; and its own patterns refuse what they reject
// with "No repository found to serve the request" (#1237).
func TestUnitDownloadPolicyThroughAVirtualRepository(t *testing.T) {
	f := newRouteFixture(t)
	for _, c := range []repo.Config{
		{Key: "pods-second", Class: repo.Local, PackageType: repo.CocoaPods},
		{
			Key: "pods-locals", Class: repo.Virtual, PackageType: repo.CocoaPods,
			Repositories: []string{"pods-local", "pods-second"},
		},
	} {
		if err := f.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	f.seedArchive("pods-local", "LocalPod", "1.0.0", "first archive")
	f.seedArchive("pods-local", "OnlyFirst", "1.0.0", "only first")
	f.seedArchive("pods-second", "LocalPod", "1.0.0", "second archive")
	archive := "packages/" + podfmt.Path("LocalPod", "1.0.0")
	shard := podfmt.ShardFile("OnlyFirst")
	served := func(label, path, want string) {
		t.Helper()
		resp := f.get("pods-locals", path)
		if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), want) {
			t.Errorf("%s, %s: %d %s", label, path, resp.Code, resp.Body)
		}
	}
	served("both members", archive, "first archive")
	served("both members", "all_pods.txt", "OnlyFirst")
	on, off := true, false
	f.patch("pods-local", func(c *repo.Config) { c.BlackedOut = &on })
	served("first member blacked out", archive, "second archive")
	if resp := f.get("pods-locals", "all_pods.txt"); resp.Code != http.StatusOK ||
		resp.Body.String() != "LocalPod\n" {
		t.Errorf("first member blacked out, index: %d %q", resp.Code, resp.Body)
	}
	if resp := f.get("pods-locals", podfmt.SpecPath("OnlyFirst", "1.0.0")); resp.Code != http.StatusNotFound {
		t.Errorf("first member blacked out, its spec: %d %s", resp.Code, resp.Body)
	}
	f.patch("pods-local", func(c *repo.Config) { c.BlackedOut = &off })
	excluded := podfmt.Path("LocalPod", "1.0.0")
	f.patch("pods-local", func(c *repo.Config) { c.ExcludesPattern = &excluded })
	served("first member excludes the archive", archive, "second archive")
	served("first member excludes the archive", podfmt.ShardFile("LocalPod"), "LocalPod/1.0.0")
	f.patch("pods-second", func(c *repo.Config) { c.ExcludesPattern = &excluded })
	if resp := f.get("pods-locals", archive); resp.Code != http.StatusNotFound ||
		!strings.Contains(resp.Body.String(), "CocoaPods resource not found") {
		t.Errorf("every member excludes the archive: %d %s", resp.Code, resp.Body)
	}
	excluded = ".pod/**"
	f.patch("pods-local", func(c *repo.Config) { c.ExcludesPattern = &excluded })
	f.patch("pods-second", func(c *repo.Config) { c.ExcludesPattern = new(string) })
	if resp := f.get("pods-locals", shard); resp.Code != http.StatusNotFound {
		t.Errorf("first member excludes its documents, its shard: %d %s", resp.Code, resp.Body)
	}
	served("first member excludes its documents", archive, "first archive")
	f.patch("pods-local", func(c *repo.Config) { c.ExcludesPattern = new(string) })
	f.patch("pods-locals", func(c *repo.Config) { c.ExcludesPattern = &excluded })
	wantRefusal(t, "virtual excludes the documents", f.get("pods-locals", "all_pods.txt"),
		"No repository found to serve the request for pods-locals:.pod/cdn/all_pods.txt")
	served("virtual excludes the documents", archive, "first archive")
	excluded = "LocalPod/**"
	f.patch("pods-locals", func(c *repo.Config) { c.ExcludesPattern = &excluded })
	wantRefusal(t, "virtual excludes the archive", f.get("pods-locals", archive),
		"No repository found to serve the request for pods-locals:"+podfmt.Path("LocalPod", "1.0.0"))
	served("virtual excludes the archive", podfmt.ShardFile("LocalPod"), "LocalPod/1.0.0")
}

// TestUnitDownloadPolicyOfARemoteRepository: a remote's own blackout and
// patterns refuse its CDN documents at .pod/cdn/<path> and a source archive
// at the archive's path, before anything is fetched; a pattern on the
// archive leaves the spec served; and a virtual repository leaves a
// blacked-out remote member out of its index and passes over one that
// refuses an archive (#1237).
func TestUnitDownloadPolicyOfARemoteRepository(t *testing.T) {
	f := newRouteFixture(t)
	f.seedRemote(t)
	spec := podfmt.SpecPath("RemotePod", "2.0.0")
	source := "sources/RemotePod/2.0.0/source.tar.gz"
	on, off := true, false
	f.patch("pods-remote", func(c *repo.Config) { c.BlackedOut = &on })
	for _, path := range []string{"all_pods.txt", podfmt.ShardFile("RemotePod"), spec} {
		wantRefusal(t, "blacked out "+path, f.get("pods-remote", path),
			blackedOut("pods-remote", ".pod/cdn/"+path))
	}
	wantRefusal(t, "blacked-out archive", f.get("pods-remote", source),
		blackedOut("pods-remote", "RemotePod/2.0.0/source.tar.gz"))
	for _, path := range []string{"/all_pods.txt", "/" + spec, "/RemotePod.tar.gz"} {
		if n := f.up.Count(http.MethodGet, path); n != 0 {
			t.Errorf("a blacked-out remote fetched %s %d times", path, n)
		}
	}
	if resp := f.get("pods-virtual", "all_pods.txt"); resp.Code != http.StatusOK ||
		strings.Contains(resp.Body.String(), "RemotePod") {
		t.Errorf("blacked-out remote member in the index: %d %q", resp.Code, resp.Body)
	}
	if resp := f.get("pods-virtual", source); resp.Code != http.StatusNotFound ||
		!strings.Contains(resp.Body.String(), "CocoaPods resource not found") {
		t.Errorf("blacked-out remote member's archive: %d %s", resp.Code, resp.Body)
	}
	excluded := ".pod/**"
	f.patch("pods-remote", func(c *repo.Config) { c.BlackedOut, c.ExcludesPattern = &off, &excluded })
	wantRefusal(t, "remote excludes its documents", f.get("pods-remote", spec),
		rejected("pods-remote", ".pod/cdn/"+spec))
	// 7.x caches a CDN remote's documents under .pod/cdn/, so a pattern
	// on the CDN's own layout does not name them ([I], #216).
	excluded = "Specs/**"
	f.patch("pods-remote", func(c *repo.Config) { c.ExcludesPattern = &excluded })
	if resp := f.get("pods-remote", spec); resp.Code != http.StatusOK {
		t.Errorf("pattern on the CDN's layout: %d %s", resp.Code, resp.Body)
	}
	excluded = "RemotePod/2.0.0/source.tar.gz"
	f.patch("pods-remote", func(c *repo.Config) { c.ExcludesPattern = &excluded })
	if resp := f.get("pods-remote", spec); resp.Code != http.StatusOK ||
		!strings.Contains(resp.Body.String(), "sources/RemotePod/2.0.0/source.tar.gz") {
		t.Errorf("spec of an excluded archive: %d %s", resp.Code, resp.Body)
	}
	wantRefusal(t, "excluded archive", f.get("pods-remote", source),
		rejected("pods-remote", "RemotePod/2.0.0/source.tar.gz"))
	if resp := f.get("pods-virtual", source); resp.Code != http.StatusNotFound ||
		!strings.Contains(resp.Body.String(), "CocoaPods resource not found") {
		t.Errorf("remote member excludes the archive: %d %s", resp.Code, resp.Body)
	}
	f.patch("pods-remote", func(c *repo.Config) { c.ExcludesPattern = new(string) })
	if resp := f.get("pods-virtual", source); resp.Code != http.StatusOK {
		t.Errorf("remote member's archive: %d %s", resp.Code, resp.Body)
	}
	f.patch("pods-virtual", func(c *repo.Config) { c.ExcludesPattern = &excluded })
	wantRefusal(t, "virtual excludes the source archive", f.get("pods-virtual", source),
		"No repository found to serve the request for pods-virtual:RemotePod/2.0.0/source.tar.gz")
}
