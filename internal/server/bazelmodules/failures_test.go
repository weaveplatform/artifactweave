package bazelmodules_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type remoteCase struct {
	f       *fixture
	up      *fakeupstream.Server
	c       repo.Config
	root    string
	archive []byte
}

func newRemoteCase(t *testing.T) remoteCase {
	t.Helper()
	up := fakeupstream.New(t, fakeupstream.Options{})
	f := newFixtureWith(t, func(items *item.Service) *source.Service { return remoteService(t, items) })
	c := *repo.Defaults(&repo.Config{Key: "bazel-remote", Class: repo.Remote, PackageType: repo.BazelModules, URL: up.URL + "/registry/"})
	if err := f.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	archiveBytes := archive(t)
	sum := sha256.Sum256(archiveBytes)
	sourceDoc, _ := json.Marshal(map[string]any{"type": "archive", "url": up.URL + "/registry/archives/hello.tar.gz", "integrity": "sha256-" + base64.StdEncoding.EncodeToString(sum[:]), "strip_prefix": "hello-1.0.0"})
	up.Set("/registry/modules/hello/metadata.json", fakeupstream.Resource{Body: []byte(`{"versions":["1.0.0"]}`)})
	up.Set("/registry/modules/hello/1.0.0/MODULE.bazel", fakeupstream.Resource{Body: []byte("module(name = \"hello\", version = \"1.0.0\")\n")})
	up.Set("/registry/modules/hello/1.0.0/source.json", fakeupstream.Resource{Body: sourceDoc})
	up.Set("/registry/archives/hello.tar.gz", fakeupstream.Resource{Body: archiveBytes})
	return remoteCase{f: f, up: up, c: c, root: "/artifactory/api/bazelmodules/" + c.Key, archive: archiveBytes}
}

func TestAccRemoteMetadataAndIdentityFailuresAreClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(remoteCase)
		paths  []string
	}{
		{"malformed-version-list", func(x remoteCase) {
			x.up.Set("/registry/modules/hello/metadata.json", fakeupstream.Resource{Body: []byte(`{"versions":["../private"]}`)})
		}, []string{"modules/hello/metadata.json", "modules/hello/1.0.0/MODULE.bazel", "modules/hello/1.0.0/source.json", "archives/hello/1.0.0/source.tar.gz"}},
		{"unlisted-version", func(x remoteCase) {
			x.up.Set("/registry/modules/hello/metadata.json", fakeupstream.Resource{Body: []byte(`{"versions":[]}`)})
		}, []string{"modules/hello/metadata.json", "modules/hello/1.0.0/MODULE.bazel", "modules/hello/1.0.0/source.json", "archives/hello/1.0.0/source.tar.gz"}},
		{"wrong-manifest", func(x remoteCase) {
			x.up.Set("/registry/modules/hello/1.0.0/MODULE.bazel", fakeupstream.Resource{Body: []byte("module(name = \"other\", version = \"1.0.0\")\n")})
		}, []string{"modules/hello/1.0.0/MODULE.bazel", "modules/hello/1.0.0/source.json", "archives/hello/1.0.0/source.tar.gz"}},
		{"bad-source-url", func(x remoteCase) {
			x.up.Set("/registry/modules/hello/1.0.0/source.json", fakeupstream.Resource{Body: []byte(`{"type":"archive","url":"https://evil.example/hello.tar.gz","integrity":"sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}`)})
		}, []string{"modules/hello/1.0.0/source.json", "archives/hello/1.0.0/source.tar.gz"}},
		{"missing-source", func(x remoteCase) { x.up.Delete("/registry/modules/hello/1.0.0/source.json") }, []string{"modules/hello/1.0.0/source.json", "archives/hello/1.0.0/source.tar.gz"}},
		{"oversized-metadata", func(x remoteCase) {
			x.up.Set("/registry/modules/hello/metadata.json", fakeupstream.Resource{Body: []byte(`{"versions":["` + strings.Repeat("x", 4<<20) + `"]}`)})
		}, []string{"modules/hello/metadata.json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newRemoteCase(t)
			tc.change(x)
			for _, tail := range tc.paths {
				w := x.f.request(http.MethodGet, x.root+"/"+tail)
				if w.Code == 200 {
					t.Errorf("invalid upstream published %s: %s", tail, w.Body.String())
				}
			}
		})
	}
}

func TestAccRemotePatchAndPermissionFailures(t *testing.T) {
	x := newRemoteCase(t)
	if w := x.f.request(http.MethodGet, x.root+"/modules/hello/1.0.0/patches/unknown.patch"); w.Code != 404 {
		t.Fatalf("undeclared patch: %d", w.Code)
	}
	if w := x.f.request(http.MethodGet, x.root+"/archives/hello/1.0.0/wrong.tar.gz"); w.Code != 404 {
		t.Fatalf("wrong archive path: %d", w.Code)
	}
	if err := x.f.db.Permissions().Create(t.Context(), types.Permission{Name: "metadata-only", Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {Actions: types.Grants{Users: map[string]types.Actions{"reader": types.ActionRead}}, Targets: map[string]types.Target{x.c.Key: {Include: []string{"modules/hello/metadata.json"}}}}}}); err != nil {
		t.Fatal(err)
	}
	w := x.f.requestAs(http.MethodGet, x.root+"/modules/hello/metadata.json", "reader")
	if w.Code == 200 || strings.Contains(w.Body.String(), "1.0.0") {
		t.Fatalf("metadata leaked unreadable version: %d %s", w.Code, w.Body.String())
	}
	if x.up.Count(http.MethodGet, "/registry/archives/hello.tar.gz") != 0 {
		t.Fatal("denied listing fetched archive")
	}
	// A virtual registry with no readable or present member must not reveal
	// metadata even if its repository itself is readable.
	v := *repo.Defaults(&repo.Config{Key: "bazel-empty-virtual", Class: repo.Virtual, PackageType: repo.BazelModules, Repositories: []string{"missing-member"}})
	if err := x.f.db.Repos().Create(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	if got := x.f.request(http.MethodGet, "/artifactory/api/bazelmodules/"+v.Key+"/modules/hello/metadata.json").Code; got != 404 {
		t.Fatalf("empty virtual listed version: %d", got)
	}
	if got := x.f.request(http.MethodGet, "/artifactory/api/bazelmodules/"+v.Key+"/bazel_registry.json").Code; got != 200 {
		t.Fatalf("virtual registry root: %d", got)
	}
}

func TestAccVirtualSkipsMissingAndIncompleteMembers(t *testing.T) {
	x := newRemoteCase(t)
	x.up.Delete("/registry/modules/hello/1.0.0/source.json")
	incomplete := *repo.Defaults(&repo.Config{Key: "bazel-incomplete", Class: repo.Local, PackageType: repo.BazelModules})
	if err := x.f.db.Repos().Create(t.Context(), incomplete); err != nil {
		t.Fatal(err)
	}
	x.f.publish(incomplete, "modules/hello/1.0.0/MODULE.bazel", []byte("module(name = \"hello\", version = \"1.0.0\")\n"))
	x.f.publish(x.f.local, "modules/hello/1.0.0/MODULE.bazel", []byte("module(name = \"hello\", version = \"1.0.0\")\n"))
	x.f.publish(x.f.local, "modules/hello/1.0.0/hello-1.0.0.tar.gz", x.archive)
	v := *repo.Defaults(&repo.Config{Key: "bazel-fallback", Class: repo.Virtual, PackageType: repo.BazelModules, Repositories: []string{x.c.Key, incomplete.Key, x.f.local.Key}})
	if err := x.f.db.Repos().Create(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	root := "/artifactory/api/bazelmodules/" + v.Key
	for _, tail := range []string{"modules/hello/metadata.json", "modules/hello/1.0.0/MODULE.bazel", "modules/hello/1.0.0/source.json", "archives/hello/1.0.0/hello-1.0.0.tar.gz"} {
		w := x.f.request(http.MethodGet, root+"/"+tail)
		if w.Code != 200 {
			t.Fatalf("fallback %s: %d %s", tail, w.Code, w.Body.String())
		}
	}
	if got := x.up.Count(http.MethodGet, "/registry/archives/hello.tar.gz"); got != 0 {
		t.Fatalf("fallback fetched missing member archive %d times", got)
	}
}

func TestAccVirtualFailsClosedOnMalformedRemoteMember(t *testing.T) {
	x := newRemoteCase(t)
	x.up.Set("/registry/modules/hello/metadata.json", fakeupstream.Resource{Body: []byte(`{"versions":"corrupt"}`)})
	v := *repo.Defaults(&repo.Config{Key: "bazel-invalid-member", Class: repo.Virtual, PackageType: repo.BazelModules, Repositories: []string{x.c.Key, x.f.local.Key}})
	if err := x.f.db.Repos().Create(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	w := x.f.request(http.MethodGet, "/artifactory/api/bazelmodules/"+v.Key+"/modules/hello/metadata.json")
	if w.Code == 200 {
		t.Fatalf("invalid member metadata was silently ignored: %s", w.Body.String())
	}
}
