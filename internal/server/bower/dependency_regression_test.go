package bower

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
)

func TestAccBowerExactSemverTagResolution(t *testing.T) {
	f, base := dependencyFixture(t)
	c, err := f.db.Repos().Get(t.Context(), "local")
	if err != nil {
		t.Fatal(err)
	}
	f.deploy(
		c,
		"hello",
		"1.0.0",
		"bower.json",
		fmt.Sprintf(
			`{"name":"hello","version":"1.0.0","dependencies":{"dep":%q}}`,
			f.up.URL+"/git/team/dep.git#2.0.0",
		),
	)
	f.up.Set(
		"/git/team/dep.git/info/refs",
		fakeupstream.Resource{
			Body: []byte(gitPkt(strings.Repeat("a", 40) + " refs/tags/v2.0.0\n")),
		},
	)
	f.up.Set(
		"/git/team/dep/archive/refs/tags/v2.0.0.tar.gz",
		fakeupstream.Resource{
			Body: bowerArchive(
				t,
				[]archiveEntry{{"dep/bower.json", `{"name":"dep","version":"2.0.0"}`, 0}},
			),
		},
	)
	rec := f.get(base+"binaries/virtual/hello.git/v1.0.0", "admin")
	if rec.Code != 200 {
		t.Fatalf(
			"exact semver dependency should resolve v-prefixed tag; got %d %s",
			rec.Code,
			rec.Body,
		)
	}
	files, err := dependencyFiles(rec.Body.Bytes(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct{ Dependencies map[string]string }
	if err := json.Unmarshal(files["bower.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Dependencies["dep"] != "art://team/dep#2.0.0" {
		t.Fatalf("exact range lost: %s", files["bower.json"])
	}
	rec = f.get(base+"refs/team/dep", "admin")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "refs/tags/v2.0.0") {
		t.Fatalf("native tag selection: %d %s", rec.Code, rec.Body)
	}
	rec = f.get(base+"binaries/team/dep.git/v2.0.0", "admin")
	if rec.Code != 200 {
		t.Fatalf("selected binary: %d %s", rec.Code, rec.Body)
	}
}

func TestAccBowerShorthandVersionIdentity(t *testing.T) {
	f, base := dependencyFixture(t)
	f.up.Set(
		"/git/team/dep/archive/refs/tags/v2.0.0.tar.gz",
		fakeupstream.Resource{
			Body: bowerArchive(
				t,
				[]archiveEntry{{"dep/bower.json", `{"name":"dep","version":"9.9.9"}`, 0}},
			),
		},
	)
	rec := f.get(base+"binaries/team/dep.git/v2.0.0", "admin")
	if rec.Code == 200 {
		files, _ := dependencyFiles(rec.Body.Bytes(), "dep")
		t.Fatalf("v2.0.0 request served inconsistent manifest: %s", files["bower.json"])
	}
	f.up.Set(
		"/git/team/dep/archive/refs/tags/v2.0.0.tar.gz",
		fakeupstream.Resource{
			Body: bowerArchive(
				t,
				[]archiveEntry{{"dep/bower.json", `{"name":"dep","version":"2.0.0"}`, 0}},
			),
		},
	)
	rec = f.get(base+"binaries/team/dep.git/v2.0.0", "admin")
	if rec.Code != 200 {
		t.Fatalf("invalid archive was cached or valid retry rejected: %d %s", rec.Code, rec.Body)
	}
}

func TestAccBowerShorthandRefsCurrentAuthority(t *testing.T) {
	f, base := dependencyFixture(t)
	f.up.Set(
		"/git/team/dep.git/info/refs",
		fakeupstream.Resource{
			Body: []byte(gitPkt(strings.Repeat("a", 40) + " refs/tags/v2.0.0\n")),
		},
	)
	checks := 0
	f.sources.Revalidate = func(_ context.Context, q source.Request, _ source.Permit) error {
		if q.Repo.Key == "virtual" {
			checks++
			return source.ErrDenied
		}
		return nil
	}
	rec := f.get(base+"refs/team/dep", "admin")
	if rec.Code != http.StatusForbidden || checks == 0 {
		t.Fatalf("revoked virtual read: status=%d checks=%d body=%s", rec.Code, checks, rec.Body)
	}
}

func TestAccBowerShorthandRefsURLAllowlist(t *testing.T) {
	f, base := dependencyFixture(t)
	f.up.Set(
		"/git/team/dep.git/info/refs",
		fakeupstream.Resource{
			Body: []byte(gitPkt(strings.Repeat("a", 40) + " refs/tags/v2.0.0\n")),
		},
	)
	c, err := f.db.Repos().Get(t.Context(), "virtual")
	if err != nil {
		t.Fatal(err)
	}
	c.ExternalDependenciesPatterns = []string{f.up.URL + "/git/team/dep.git"}
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	rec := f.get(base+"refs/team/dep", "admin")
	if rec.Code != http.StatusForbidden || len(f.up.Requests()) != 0 {
		t.Fatalf(
			"refs URL outside exact virtual allowlist: status=%d requests=%+v",
			rec.Code,
			f.up.Requests(),
		)
	}
}

func TestAccBowerExistingNativeDependencyAddress(t *testing.T) {
	f, base := dependencyFixture(t)
	c, err := f.db.Repos().Get(t.Context(), "local")
	if err != nil {
		t.Fatal(err)
	}
	f.deploy(c, "dep", "2.0.0", "bower.json", `{"name":"dep","version":"2.0.0"}`)
	f.deploy(
		c,
		"hello",
		"1.0.0",
		"bower.json",
		`{"name":"hello","version":"1.0.0","dependencies":{"dep":"art://virtual/dep#2.0.0"}}`,
	)
	rec := f.get(base+"binaries/virtual/hello.git/v1.0.0", "admin")
	if rec.Code != 200 {
		t.Fatalf(
			"native local member address should stay installable; got %d %s",
			rec.Code,
			rec.Body,
		)
	}
	files, err := dependencyFiles(rec.Body.Bytes(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(files["bower.json"]), "art://virtual/dep#2.0.0") {
		t.Fatalf("native address changed: %s", files["bower.json"])
	}
	rec = f.get(base+"binaries/virtual/dep.git/v2.0.0", "admin")
	if rec.Code != 200 {
		t.Fatalf("native dependency install: %d %s", rec.Code, rec.Body)
	}
	for _, req := range f.up.Requests() {
		if strings.HasPrefix(req.Path, "/git/virtual/") {
			t.Fatal("native address escaped to external Git")
		}
	}
}

func TestAccBowerShorthandRefsRechecksEmittedPaths(t *testing.T) {
	for _, scope := range []string{"virtual", "bower-remote"} {
		for _, warm := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/warm=%t", scope, warm), func(t *testing.T) {
				f, base := dependencyFixture(t)
				f.up.Set(
					"/git/team/dep.git/info/refs",
					fakeupstream.Resource{
						Body: []byte(gitPkt(strings.Repeat("a", 40) + " refs/tags/v2.0.0\n")),
					},
				)
				if warm {
					rec := f.get(base+"refs/team/dep", "admin")
					if rec.Code != 200 {
						t.Fatal(rec.Code)
					}
				}
				checks := 0
				f.sources.Revalidate = func(_ context.Context, q source.Request, _ source.Permit) error {
					if q.Repo.Key == scope && q.Resource.Path == "dep/2.0.0/bower.json" {
						checks++
						return source.ErrDenied
					}
					return nil
				}
				rec := f.get(base+"refs/team/dep", "admin")
				if rec.Code != http.StatusForbidden || checks == 0 {
					t.Fatalf(
						"revoked emitted path: status=%d checks=%d body=%s",
						rec.Code,
						checks,
						rec.Body,
					)
				}
			})
		}
	}
}

func TestAccBowerShorthandRefsCachedAllowlistRevocation(t *testing.T) {
	f, base := dependencyFixture(t)
	f.up.Set(
		"/git/team/dep.git/info/refs",
		fakeupstream.Resource{
			Body: []byte(gitPkt(strings.Repeat("a", 40) + " refs/tags/v2.0.0\n")),
		},
	)
	rec := f.get(base+"refs/team/dep", "admin")
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	before := len(f.up.Requests())
	c, err := f.db.Repos().Get(t.Context(), "virtual")
	if err != nil {
		t.Fatal(err)
	}
	c.ExternalDependenciesPatterns = []string{f.up.URL + "/git/team/dep.git"}
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	rec = f.get(base+"refs/team/dep", "admin")
	if rec.Code != http.StatusForbidden || len(f.up.Requests()) != before {
		t.Fatalf("cached refs bypassed endpoint allowlist: %d %s", rec.Code, rec.Body)
	}
}
