package bazelmodules_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestAccRemoteCacheOfflineIntegrityAndRedirect(t *testing.T) {
	up := fakeupstream.New(t, fakeupstream.Options{})
	f := newFixtureWith(
		t,
		func(items *item.Service) *source.Service { return remoteService(t, items) },
	)
	c := *repo.Defaults(&repo.Config{Key: "bazel-remote", Class: repo.Remote, PackageType: repo.BazelModules, URL: up.URL + "/registry/"})
	if err := f.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	module := []byte("module(name = \"hello\", version = \"1.0.0\")\n")
	archiveBytes := archive(t)
	digest := sha256.Sum256(archiveBytes)
	patchBytes := []byte(
		"--- a/message.txt\n+++ b/message.txt\n@@ -1 +1 @@\n-hello from Bazel\n+hello from patch\n",
	)
	patchDigest := sha256.Sum256(patchBytes)
	sourceDoc := map[string]any{
		"type":         "archive",
		"url":          up.URL + "/registry/archives/hello.tar.gz",
		"integrity":    "sha256-" + base64.StdEncoding.EncodeToString(digest[:]),
		"strip_prefix": "hello-1.0.0",
		"patches": map[string]string{
			"message.patch": "sha256-" + base64.StdEncoding.EncodeToString(patchDigest[:]),
		},
	}
	sourceBytes, _ := json.Marshal(sourceDoc)
	up.Set(
		"/registry/modules/hello/metadata.json",
		fakeupstream.Resource{Body: []byte(`{"versions":["1.0.0"]}`)},
	)
	up.Set("/registry/modules/hello/1.0.0/MODULE.bazel", fakeupstream.Resource{Body: module})
	up.Set("/registry/modules/hello/1.0.0/source.json", fakeupstream.Resource{Body: sourceBytes})
	up.Set("/registry/archives/hello.tar.gz", fakeupstream.Resource{Body: archiveBytes})
	up.Set(
		"/registry/modules/hello/1.0.0/patches/message.patch",
		fakeupstream.Resource{Body: patchBytes},
	)
	root := "/artifactory/api/bazelmodules/" + c.Key
	for _, tail := range []string{"modules/hello/metadata.json", "modules/hello/1.0.0/MODULE.bazel", "modules/hello/1.0.0/source.json"} {
		w := f.request(http.MethodGet, root+"/"+tail)
		if w.Code != 200 {
			t.Fatalf("remote %s: %d %s", tail, w.Code, w.Body.String())
		}
		if strings.HasSuffix(tail, "source.json") {
			var doc struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(doc.URL, up.URL) ||
				!strings.Contains(doc.URL, "/archives/hello/1.0.0/source.tar.gz") {
				t.Fatalf("unrewritten source: %s", doc.URL)
			}
			w = f.request(http.MethodGet, doc.URL)
			if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), archiveBytes) {
				t.Fatalf("remote archive: %d %s", w.Code, w.Body.String())
			}
		}
	}
	if w := f.request(
		http.MethodGet,
		root+"/modules/hello/1.0.0/patches/message.patch",
	); w.Code != 200 ||
		!bytes.Equal(w.Body.Bytes(), patchBytes) {
		t.Fatalf("remote patch: %d %s", w.Code, w.Body.String())
	}
	if w := f.requestAs(
		http.MethodGet,
		root+"/modules/hello/metadata.json",
		"reader",
	); w.Code != 403 {
		t.Fatalf("denied metadata: %d", w.Code)
	}
	virtual := *repo.Defaults(&repo.Config{Key: "bazel-virtual-remote", Class: repo.Virtual, PackageType: repo.BazelModules, Repositories: []string{c.Key}})
	if err := f.db.Repos().Create(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	vroot := "/artifactory/api/bazelmodules/" + virtual.Key
	for _, tail := range []string{"modules/hello/metadata.json", "modules/hello/1.0.0/MODULE.bazel", "modules/hello/1.0.0/source.json", "archives/hello/1.0.0/source.tar.gz", "modules/hello/1.0.0/patches/message.patch"} {
		w := f.request(http.MethodGet, vroot+"/"+tail)
		if w.Code != 200 {
			t.Fatalf("virtual remote %s: %d %s", tail, w.Code, w.Body.String())
		}
	}
	before := up.Count(http.MethodGet, "")
	offline := true
	c.Offline = &offline
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	for _, tail := range []string{"modules/hello/metadata.json", "modules/hello/1.0.0/source.json", "archives/hello/1.0.0/source.tar.gz"} {
		w := f.request(http.MethodGet, root+"/"+tail)
		if w.Code != 200 {
			t.Fatalf("offline %s: %d %s", tail, w.Code, w.Body.String())
		}
	}
	if got := up.Count(http.MethodGet, ""); got != before {
		t.Fatalf("offline fetched upstream: %d -> %d", before, got)
	}
	badRepo := *repo.Defaults(&repo.Config{Key: "bazel-bad", Class: repo.Remote, PackageType: repo.BazelModules, URL: up.URL + "/registry/"})
	if err := f.db.Repos().Create(t.Context(), badRepo); err != nil {
		t.Fatal(err)
	}
	up.Set(
		"/registry/modules/hello/metadata.json",
		fakeupstream.Resource{Body: []byte(`{"versions":["2.0.0"]}`)},
	)
	up.Set(
		"/registry/modules/hello/2.0.0/MODULE.bazel",
		fakeupstream.Resource{Body: []byte("module(name = \"hello\", version = \"2.0.0\")\n")},
	)
	up.Set(
		"/registry/modules/hello/2.0.0/source.json",
		fakeupstream.Resource{Redirect: "/registry/modules/hello/2.0.0/redirect.json"},
	)
	up.Set("/registry/modules/hello/2.0.0/redirect.json", fakeupstream.Resource{Body: sourceBytes})
	up.Set("/registry/archives/hello.tar.gz", fakeupstream.Resource{Body: []byte("tampered")})
	badRoot := "/artifactory/api/bazelmodules/" + badRepo.Key
	if w := f.request(http.MethodGet, badRoot+"/modules/hello/2.0.0/source.json"); w.Code != 200 {
		t.Fatalf("same-origin source redirect %d %s", w.Code, w.Body.String())
	}
	w := f.request(http.MethodGet, badRoot+"/archives/hello/2.0.0/source.tar.gz")
	if w.Code == 200 {
		t.Fatal("tampered upstream archive was served")
	}
	if f.db.RemoteCache() == nil {
		t.Fatal("remote cache unavailable")
	}
}

func TestAccYankedMetadataFollowsSelectedMember(t *testing.T) {
	x := newRemoteCase(t)
	x.up.Set(
		"/registry/modules/hello/metadata.json",
		fakeupstream.Resource{
			Body: []byte(
				`{"versions":["1.0.0"],"yanked_versions":{"1.0.0":"security regression"},"deprecated":"use successor"}`,
			),
		},
	)
	x.f.publish(
		x.f.local,
		"modules/hello/1.0.0/MODULE.bazel",
		[]byte("module(name = \"hello\", version = \"1.0.0\")\n"),
	)
	x.f.publish(x.f.local, "modules/hello/1.0.0/hello-1.0.0.tar.gz", x.archive)
	for _, tc := range []struct {
		key     string
		members []string
		yanked  bool
	}{
		{"bazel-yanked-remote-first", []string{x.c.Key, x.f.local.Key}, true},
		{"bazel-yanked-local-first", []string{x.f.local.Key, x.c.Key}, false},
	} {
		v := *repo.Defaults(&repo.Config{Key: tc.key, Class: repo.Virtual, PackageType: repo.BazelModules, Repositories: tc.members})
		if err := x.f.db.Repos().Create(t.Context(), v); err != nil {
			t.Fatal(err)
		}
		w := x.f.request(
			http.MethodGet,
			"/artifactory/api/bazelmodules/"+v.Key+"/modules/hello/metadata.json",
		)
		if w.Code != 200 {
			t.Fatalf("%s metadata: %d %s", tc.key, w.Code, w.Body.String())
		}
		if got := strings.Contains(w.Body.String(), `"yanked_versions"`); got != tc.yanked {
			t.Fatalf("%s selected wrong member's yank status: %s", tc.key, w.Body.String())
		}
	}
	remote := x.f.request(http.MethodGet, x.root+"/modules/hello/metadata.json")
	if remote.Code != 200 ||
		!strings.Contains(remote.Body.String(), `"deprecated":"use successor"`) ||
		!strings.Contains(remote.Body.String(), `"security regression"`) {
		t.Fatalf(
			"remote metadata lost yank or deprecation: %d %s",
			remote.Code,
			remote.Body.String(),
		)
	}
}

func remoteService(t *testing.T, items *item.Service) *source.Service {
	t.Helper()
	key := []byte(strings.Repeat("k", 32))
	resolver, err := remote.NewResolver(items.Store, nil, remote.ResolverOptions{RevisionKey: key})
	if err != nil {
		t.Fatal(err)
	}
	policy := &remote.Policy{
		Store:   items.Store,
		Blobs:   items.Blobs,
		Items:   items,
		Pool:    new(remote.Pool),
		Options: resolver.Options,
		Key:     key,
	}
	t.Cleanup(policy.Pool.Close)
	return &source.Service{
		HTTP:  policy,
		Cache: policy,
		Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
			return items.Store.RemoteCache().Source(ctx, c.Key)
		},
	}
}
