package bowerhttp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
)

func TestAccBowerPartialSemverRanges(t *testing.T) {
	for _, version := range []string{"2", "2.0", "2.x", "2.X", "2.0.x", "2.0.X", "*", "~2", ">=2 <3", "2.0.0 - 2.9.0", "master"} {
		t.Run(version, func(t *testing.T) {
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
					f.up.URL+"/git/team/dep.git#"+version,
				),
			)
			rec := f.get(base+"binaries/virtual/hello.git/v1.0.0", "admin")
			if rec.Code != 200 {
				t.Fatalf(
					"native target %q was converted into literal branch; status=%d body=%s",
					version,
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
			if manifest.Dependencies["dep"] != "art://team/dep#"+version {
				t.Fatalf("target changed: %s", files["bower.json"])
			}
			for _, req := range f.up.Requests() {
				if strings.Contains(req.Path, "/archive/") {
					t.Fatal("parent fetched an unresolved target")
				}
			}
		})
	}
}

func TestAccBowerBinaryMemberVersionAuthority(t *testing.T) {
	for _, warm := range []bool{false, true} {
		t.Run(fmt.Sprintf("warm=%t", warm), func(t *testing.T) {
			f, base := dependencyFixture(t)
			f.up.Set(
				"/git/team/dep/archive/refs/tags/v2.0.0.tar.gz",
				fakeupstream.Resource{
					Body: bowerArchive(
						t,
						[]archiveEntry{
							{"dep/bower.json", `{"name":"dep","version":"2.0.0"}`, 0},
							{"dep/private.js", "denied-version-content", 0},
						},
					),
				},
			)
			if warm {
				rec := f.get(base+"binaries/team/dep.git/v2.0.0", "admin")
				if rec.Code != 200 {
					t.Fatal(rec.Code)
				}
			}
			checks := 0
			f.sources.Revalidate = func(_ context.Context, q source.Request, _ source.Permit) error {
				if q.Repo.Key == "bower-remote" && q.Resource.Path == "dep/2.0.0/bower.json" {
					checks++
					return source.ErrDenied
				}
				return nil
			}
			rec := f.get(base+"binaries/team/dep.git/v2.0.0", "admin")
			if rec.Code != 403 || checks == 0 {
				t.Fatalf(
					"member version denied but binary delivered: status=%d version checks=%d",
					rec.Code,
					checks,
				)
			}
		})
	}
}

func TestAccBowerBinaryStaticPermission(t *testing.T) {
	f, base := dependencyFixture(t)
	f.up.Set(
		"/git/team/dep/archive/refs/tags/v2.0.0.tar.gz",
		fakeupstream.Resource{
			Body: bowerArchive(
				t,
				[]archiveEntry{
					{"dep/bower.json", `{"name":"dep","version":"2.0.0"}`, 0},
					{"dep/private.js", "denied-version-content", 0},
				},
			),
		},
	)
	err := f.db.Permissions().
		Create(t.Context(), model.Permission{Name: "exclude-version", Resources: map[api.ResourceType]model.Resource{api.ResourceArtifact: {Actions: model.Grants{Users: map[string]api.Actions{"limited": api.ActionRead}}, Targets: map[string]model.Target{"virtual": {Include: []string{"dep/**"}}, "bower-remote": {Include: []string{"dep/**"}, Exclude: []string{"dep/2.0.0/**"}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	rec := f.get(base+"binaries/team/dep.git/v2.0.0", "limited")
	if rec.Code == 200 {
		t.Fatalf("explicit remote version exclusion still returns private bytes: %d", rec.Code)
	}
}

func TestAccBowerBinaryVersionRevocationDuringRewrite(t *testing.T) {
	for _, scope := range []string{"virtual", "bower-remote"} {
		t.Run(scope, func(t *testing.T) {
			f, base := dependencyFixture(t)
			f.up.Set(
				"/git/team/dep/archive/refs/tags/v2.0.0.tar.gz",
				fakeupstream.Resource{
					Body: bowerArchive(
						t,
						[]archiveEntry{
							{
								"dep/bower.json",
								fmt.Sprintf(
									`{"name":"dep","version":"2.0.0","dependencies":{"child":%q}}`,
									f.up.URL+"/child.tgz",
								),
								0,
							},
						},
					),
				},
			)
			f.up.Set(
				"/child.tgz",
				fakeupstream.Resource{
					Body: bowerArchive(
						t,
						[]archiveEntry{{"child/bower.json", `{"name":"child"}`, 0}},
					),
				},
			)
			revoked := false
			checks := 0
			f.sources.Revalidate = func(_ context.Context, q source.Request, _ source.Permit) error {
				if strings.HasPrefix(q.Resource.Path, "child/") {
					revoked = true
				}
				if revoked && q.Repo.Key == scope && q.Resource.Path == "dep/2.0.0/bower.json" {
					checks++
					return source.ErrDenied
				}
				return nil
			}
			rec := f.get(base+"binaries/team/dep.git/v2.0.0", "admin")
			if rec.Code != 403 || checks == 0 {
				t.Fatalf(
					"selected version revoked during rewrite: status=%d checks=%d body=%s",
					rec.Code,
					checks,
					rec.Body,
				)
			}
		})
	}
}
