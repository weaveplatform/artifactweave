package bower

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
)

func dependencyFixture(t *testing.T) (*remoteFixture, string) {
	t.Helper()
	f := newRemoteFixture(t, true)
	local := f.repo("local", repo.Local)
	virtual := f.repo("virtual", repo.Virtual, local.Key, "bower-remote")
	virtual.ExternalDependenciesEnabled = new(true)
	virtual.ExternalDependenciesPatterns = []string{f.up.URL + "/**"}
	virtual.ExternalDependenciesRemoteRepo = "bower-remote"
	if err := f.db.Repos().Update(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	f.deploy(
		local,
		"hello",
		"1.0.0",
		"bower.json",
		`{"name":"hello","version":"1.0.0","dependencies":{"dep":"`+f.up.URL+`/external/dep.tgz"}}`,
	)
	f.up.Set(
		"/external/dep.tgz",
		fakeupstream.Resource{
			Body: bowerArchive(
				t,
				[]archiveEntry{
					{"dep/bower.json", `{"name":"dep","version":"2.0.0"}`, 0},
					{"dep/index.js", "external-marker", 0},
				},
			),
		},
	)
	return f, "/artifactory/api/bower/virtual/"
}

func TestAccBowerVirtualDependencyRewriteCacheAndPolicy(t *testing.T) {
	f, base := dependencyFixture(t)
	rec := f.get(base+"binaries/virtual/hello.git/v1.0.0", "admin")
	if rec.Code != 200 {
		t.Fatalf("rewritten parent: %d %s", rec.Code, rec.Body)
	}
	files, err := dependencyFiles(rec.Body.Bytes(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(files["bower.json"], &m); err != nil {
		t.Fatal(err)
	}
	rewritten := m.Dependencies["dep"]
	if !strings.HasPrefix(rewritten, "art://external/dep#"+dependencyTargetPrefix) {
		t.Fatalf("dependency: %q", rewritten)
	}
	_, tag, _ := strings.Cut(rewritten, "#")
	path := base + "binaries/external/dep.git/" + url.PathEscape(tag)
	rec = f.get(path, "admin")
	if rec.Code != 200 {
		t.Fatalf("dependency: %d %s", rec.Code, rec.Body)
	}
	files, err = dependencyFiles(rec.Body.Bytes(), "dep")
	if err != nil || string(files["index.js"]) != "external-marker" {
		t.Fatalf("dependency files: %v %v", files, err)
	}
	if rec = f.get(path, "guest"); rec.Code != http.StatusForbidden {
		t.Fatalf("denied dependency: %d", rec.Code)
	}
	f.configure(func(c *repo.Config) { c.Offline = new(true) })
	f.up.Close()
	if rec = f.get(path, "admin"); rec.Code != 200 {
		t.Fatalf("offline dependency: %d %s", rec.Code, rec.Body)
	}
	virtual, err := f.db.Repos().Get(t.Context(), "virtual")
	if err != nil {
		t.Fatal(err)
	}
	virtual.ExternalDependenciesPatterns = []string{"https://elsewhere.invalid/**"}
	if err := f.db.Repos().Update(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	if rec = f.get(path, "admin"); rec.Code != 403 {
		t.Fatalf("cached dependency after allowlist revocation: %d", rec.Code)
	}
}

func TestAccBowerShorthandUsesApprovedCacheMember(t *testing.T) {
	f, base := dependencyFixture(t)
	f.up.Set(
		"/git/team/dep.git/info/refs",
		fakeupstream.Resource{
			Body: []byte(gitPkt(strings.Repeat("a", 40) + " refs/tags/v2.0.0\n")),
		},
	)
	f.up.Set(
		"/git/team/dep/archive/refs/tags/v2.0.0.tar.gz",
		fakeupstream.Resource{
			Body: bowerArchive(t, []archiveEntry{{"dep/bower.json", `{"name":"dep"}`, 0}}),
		},
	)
	for _, path := range []string{"refs/team/dep", "binaries/team/dep.git/v2.0.0"} {
		rec := f.get(base+path, "admin")
		if rec.Code != 200 {
			t.Fatalf("shorthand %s: %d %s", path, rec.Code, rec.Body)
		}
	}
	virtual, err := f.db.Repos().Get(t.Context(), "virtual")
	if err != nil {
		t.Fatal(err)
	}
	virtual.Repositories = []string{"local"}
	if err := f.db.Repos().Update(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	if rec := f.get(base+"binaries/team/dep.git/v2.0.0", "admin"); rec.Code == 200 {
		t.Fatal("removed cache member remained accessible")
	}
}

func TestUnitBowerExternalURLAndArchiveRejections(t *testing.T) {
	c := repo.Config{
		Class:                        repo.Virtual,
		ExternalDependenciesEnabled:  new(true),
		ExternalDependenciesPatterns: []string{"https://allowed.example/**"},
	}
	for _, raw := range []string{"https://foreign.example/archive.tgz", "http://allowed.example/archive.tgz", "https://user:secret@allowed.example/archive.tgz", "https://allowed.example/../archive.tgz", "https://allowed.example/%2e%2e/archive.tgz", "file:///tmp/archive.tgz", "ssh://allowed.example/a.git", "https://allowed.example/a.tgz#tag"} {
		if _, err := dependencyURL(c, raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	for _, tag := range []string{"bad", dependencyTargetPrefix + "%%%" + ".tar.gz", strings.Repeat("x", 6001)} {
		if _, err := decodeDependencyTarget(tag); err == nil {
			t.Errorf("accepted target %q", tag)
		}
	}
	for _, entries := range [][]archiveEntry{
		{{"dep/bower.json", `{"name":"other"}`, 0}},
		{{"../dep/bower.json", `{"name":"dep"}`, 0}},
		{{"dep/bower.json", `{"name":"dep"}`, 0}, {"dep/bower.json", `{"name":"dep"}`, 0}},
		{{"dep/bower.json", `{"name":"dep"}`, 0}, {"elsewhere/file", "bad", 0}},
	} {
		if _, err := dependencyFiles(bowerArchive(t, entries), "dep"); err == nil {
			t.Fatal("accepted invalid dependency archive")
		}
	}
	var zipBytes bytes.Buffer
	zw := zip.NewWriter(&zipBytes)
	entry, err := zw.Create("bower.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte(`{"name":"dep"}`)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := dependencyFiles(zipBytes.Bytes(), "dep"); err != nil {
		t.Fatalf("rootless ZIP refused: %v", err)
	}
	data := bowerArchive(t, []archiveEntry{{"dep/bower.json", `{"name":"dep"}`, 0}})
	data[len(data)-1] ^= 0xff
	if _, err := dependencyFiles(data, "dep"); err == nil {
		t.Fatal("corrupt gzip accepted")
	}
}
