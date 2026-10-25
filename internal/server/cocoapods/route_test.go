package cocoapods

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	podfmt "github.com/weaveplatform/artifactweave/internal/format/cocoapods"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type routeFixture struct {
	t      *testing.T
	router http.Handler
	db     *inmem.Store
	up     *fakeupstream.Server
}

func newRouteFixture(t *testing.T) *routeFixture {
	t.Helper()
	db := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	queue, err := work.New(db, work.Options{})
	if err != nil {
		t.Fatal(err)
	}
	items := &item.Service{Store: db, Blobs: blobs, Work: queue}
	if err := items.RegisterWork(queue); err != nil {
		t.Fatal(err)
	}
	up := fakeupstream.New(t, fakeupstream.Options{})
	enabled := true
	for _, c := range []repo.Config{
		{Key: "pods-local", Class: repo.Local, PackageType: repo.CocoaPods},
		{
			Key: "pods-remote", Class: repo.Remote, PackageType: repo.CocoaPods,
			URL: up.URL, PodsCdnURL: up.URL, ExternalDependenciesEnabled: &enabled,
			ExternalDependenciesPatterns: []string{up.URL + "/**"},
		},
		{
			Key: "pods-virtual", Class: repo.Virtual, PackageType: repo.CocoaPods,
			Repositories: []string{"pods-local", "pods-remote"},
		},
	} {
		if err := db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	key := []byte(strings.Repeat("k", 32))
	resolver, err := remote.NewResolver(db, nil, remote.ResolverOptions{
		RevisionKey: key, AllowedPrivateOrigins: []string{up.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := &remote.Policy{
		Store: db, Blobs: blobs, Items: items,
		Pool: new(remote.Pool), Options: resolver.Options, Key: key,
	}
	t.Cleanup(policy.Pool.Close)
	service := &source.Service{
		HTTP: policy, Cache: policy,
		Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
			return db.RemoteCache().Source(ctx, c.Key)
		},
	}
	r := chi.NewRouter()
	r.Use(baseurl.Middleware(""))
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := r.Header.Get("X-User")
			if name == "" {
				name = "admin"
			}
			principal, err := auth.Authorize(r.Context(), authz.Sources{
				Permissions: db.Permissions(), Groups: db.Groups(), AnonymousEnabled: true,
			}, auth.Principal{Name: name, Admin: name == "admin"})
			if err != nil {
				t.Fatal(err)
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
		})
	})
	pkgroute.Mount(r, pkgroute.Options{
		Store: db, Items: func() *item.Service { return items },
		ServeItem: func(w http.ResponseWriter, r *http.Request, _ repo.Config, it types.Item) {
			body, _, err := blobs.Open(r.Context(), it.Actual.SHA256)
			if err != nil {
				t.Fatal(err)
			}
			defer body.Close()
			if r.Method != http.MethodHead {
				if _, err := io.Copy(w, body); err != nil {
					t.Fatal(err)
				}
			}
		},
	}, Protocol(Options{RemoteSources: service}))
	return &routeFixture{t: t, router: r, db: db, up: up}
}

func (f *routeFixture) get(key, path string) *httptest.ResponseRecorder {
	return f.getAs("admin", key, path)
}

func (f *routeFixture) getAs(user, key, path string) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/artifactory/api/pods/"+key+"/"+path, nil)
	r.Header.Set("X-User", user)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}

func (f *routeFixture) seedLocal(name, version string) {
	f.t.Helper()
	ctx := f.t.Context()
	p, err := f.db.Packages().
		Ensure(ctx, types.Package{RepoKey: "pods-local", Type: repo.CocoaPods, Name: name})
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
	if _, err := f.db.Packages().PutVersion(ctx, types.PackageVersion{
		PackageID: p.ID, Version: version, Metadata: metadata,
	}); err != nil {
		f.t.Fatal(err)
	}
}

func sourceTar(t *testing.T) []byte {
	t.Helper()
	var body bytes.Buffer
	gz := gzip.NewWriter(&body)
	tw := tar.NewWriter(gz)
	data := []byte("source code")
	if err := tw.WriteHeader(
		&tar.Header{Name: "Sources/RemotePod.m", Mode: 0o644, Size: int64(len(data))},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

func (f *routeFixture) seedRemote(t *testing.T) {
	f.up.Set("/all_pods.txt", fakeupstream.Resource{Body: []byte("RemotePod\n")})
	f.up.Set(
		"/"+podfmt.ShardFile("RemotePod"),
		fakeupstream.Resource{Body: []byte("RemotePod/2.0.0\n")},
	)
	f.up.Set("/"+podfmt.SpecPath("RemotePod", "2.0.0"), fakeupstream.Resource{Body: []byte(
		`{"name":"RemotePod","version":"2.0.0","source":{"http":"` + f.up.URL + `/RemotePod.tar.gz"}}`,
	)})
	f.up.Set("/RemotePod.tar.gz", fakeupstream.Resource{Body: sourceTar(t)})
}

func TestUnitVirtualComposesLocalAndRemoteCDNRoutes(t *testing.T) {
	f := newRouteFixture(t)
	f.seedLocal("LocalPod", "1.0.0")
	f.seedRemote(t)
	for _, tc := range []struct {
		path, contains string
	}{
		{"CocoaPods-version.yml", "prefix_lengths"},
		{"deprecated_podspecs.txt", ""},
		{"all_pods.txt", "RemotePod"},
		{podfmt.ShardFile("LocalPod"), "LocalPod/1.0.0"},
		{podfmt.ShardFile("RemotePod"), "RemotePod/2.0.0"},
		{podfmt.SpecPath("LocalPod", "1.0.0"), `"name":"LocalPod"`},
		{podfmt.SpecPath("RemotePod", "2.0.0"), `"name":"RemotePod"`},
		{"sources/RemotePod/2.0.0/source.tar.gz", ""},
	} {
		resp := f.get("pods-virtual", tc.path)
		if resp.Code != http.StatusOK ||
			(tc.path != "deprecated_podspecs.txt" && resp.Body.Len() == 0) ||
			!strings.Contains(resp.Body.String(), tc.contains) {
			t.Errorf("virtual %q: %d %s", tc.path, resp.Code, resp.Body.String())
		}
	}
	for _, path := range []string{
		podfmt.SpecPath("AbsentPod", "1.0.0"),
		"sources/RemotePod/2.0.0/source.zip",
		"packages/AbsentPod/1.0.0/AbsentPod-1.0.0.tar.gz",
		"bad/request",
	} {
		resp := f.get("pods-virtual", path)
		if resp.Code != http.StatusNotFound {
			t.Errorf("virtual absence %q: %d %s", path, resp.Code, resp.Body.String())
		}
	}
}

func TestUnitRemoteCDNRouteBoundaries(t *testing.T) {
	f := newRouteFixture(t)
	f.seedRemote(t)
	for _, tc := range []struct {
		path string
		want int
	}{
		{"all_pods.txt", http.StatusOK},
		{podfmt.ShardFile("RemotePod"), http.StatusOK},
		{podfmt.SpecPath("RemotePod", "2.0.0"), http.StatusOK},
		{"sources/RemotePod/2.0.0/source.tar.gz", http.StatusOK},
		{"all_pods.txt?source=https://evil.example/", http.StatusBadRequest},
		{"bad/request", http.StatusNotFound},
		{"sources/RemotePod/2.0.0/source.zip", http.StatusNotFound},
	} {
		resp := f.get("pods-remote", tc.path)
		if resp.Code != tc.want {
			t.Errorf("remote %q: %d %s; want %d", tc.path, resp.Code, resp.Body.String(), tc.want)
		}
	}
	if got := f.up.Count(http.MethodGet, "/bad/request"); got != 0 {
		t.Errorf("invalid remote route fetched upstream %d times", got)
	}
}

func TestUnitVirtualRemoteAbsenceFallsThroughToLocal(t *testing.T) {
	f := newRouteFixture(t)
	f.seedLocal("LocalPod", "1.0.0")
	c, err := f.db.Repos().Get(t.Context(), "pods-virtual")
	if err != nil {
		t.Fatal(err)
	}
	c.Repositories = []string{"pods-remote", "pods-local"}
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{podfmt.ShardFile("LocalPod"), podfmt.SpecPath("LocalPod", "1.0.0")} {
		resp := f.get("pods-virtual", path)
		if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "LocalPod") {
			t.Errorf("remote 404 blocked local member %q: %d %s", path, resp.Code, resp.Body)
		}
	}
	for _, path := range []string{
		"CocoaPods-version.yml", "deprecated_podspecs.txt", "all_pods.txt",
		"packages/AbsentPod/1.0.0/AbsentPod-1.0.0.tar.gz",
	} {
		resp := f.get("pods-local", path)
		if path == "packages/AbsentPod/1.0.0/AbsentPod-1.0.0.tar.gz" &&
			resp.Code != http.StatusNotFound {
			t.Errorf("missing local package %q: %d %s", path, resp.Code, resp.Body)
		} else if path != "packages/AbsentPod/1.0.0/AbsentPod-1.0.0.tar.gz" &&
			resp.Code != http.StatusOK {
			t.Errorf("local CDN route %q: %d %s", path, resp.Code, resp.Body)
		}
	}
}

func TestUnitRemoteRejectsMalformedOrUnapprovedCDNResponses(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		body func(*routeFixture) []byte
	}{
		{"Git source", podfmt.SpecPath("RemotePod", "2.0.0"), func(f *routeFixture) []byte {
			return []byte(`{"name":"RemotePod","version":"2.0.0","source":{"git":"` + f.up.URL + `/repo.git"}}`)
		}},
		{"unapproved source", podfmt.SpecPath("RemotePod", "2.0.0"), func(_ *routeFixture) []byte {
			return []byte(`{"name":"RemotePod","version":"2.0.0","source":{"http":"https://unapproved.example.org/pod.tar.gz"}}`)
		}},
		{"invalid name index", "all_pods.txt", func(_ *routeFixture) []byte { return []byte("../escape\n") }},
		{"wrong shard index", podfmt.ShardFile("RemotePod"), func(_ *routeFixture) []byte { return []byte("WrongPod/2.0.0\n") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRouteFixture(t)
			f.seedRemote(t)
			f.up.Set("/"+tc.path, fakeupstream.Resource{Body: tc.body(f)})
			resp := f.get("pods-remote", tc.path)
			if resp.Code == http.StatusOK {
				t.Fatalf("unsafe CDN response served: %s", resp.Body)
			}
			if tc.name == "unapproved source" {
				if got := f.up.Count(http.MethodGet, "/pod.tar.gz"); got != 0 {
					t.Errorf("unapproved source fetched %d times", got)
				}
			}
		})
	}
}

func TestUnitRemoteRejectsCorruptSourceBeforeCachePublication(t *testing.T) {
	f := newRouteFixture(t)
	f.seedRemote(t)
	f.up.Set("/RemotePod.tar.gz", fakeupstream.Resource{Body: []byte("corrupt archive")})
	path := "sources/RemotePod/2.0.0/source.tar.gz"
	for range 2 {
		resp := f.get("pods-remote", path)
		if resp.Code == http.StatusOK {
			t.Fatal("corrupt source was served or cached")
		}
	}
	if got := f.up.Count(http.MethodGet, "/RemotePod.tar.gz"); got != 2 {
		t.Errorf("corrupt source unexpectedly cached: %d upstream requests", got)
	}
}

func TestUnitLocalCDNListsOnlyIndexedPodVersions(t *testing.T) {
	f := newRouteFixture(t)
	f.seedLocal("LocalPod", "1.0.0")
	f.seedLocal("LocalPod", "1.1.0")
	for _, tc := range []struct {
		path, contains string
		want           int
	}{
		{"all_pods.txt", "LocalPod\n", http.StatusOK},
		{podfmt.ShardFile("LocalPod"), "LocalPod/1.0.0/1.1.0", http.StatusOK},
		{podfmt.SpecPath("LocalPod", "1.0.0"), `"name":"LocalPod"`, http.StatusOK},
		{podfmt.SpecPath("LocalPod", "1.1.0"), `"version":"1.1.0"`, http.StatusOK},
		{podfmt.SpecPath("LocalPod", "2.0.0"), "", http.StatusNotFound},
		{"all_pods_versions_0_0_0.txt", "", http.StatusNotFound},
		{"packages/LocalPod/1.0.0/wrong.tar.gz", "", http.StatusNotFound},
		{"bad/request", "", http.StatusNotFound},
		{"all_pods.txt?index=other", "", http.StatusBadRequest},
	} {
		resp := f.get("pods-local", tc.path)
		if resp.Code != tc.want || !strings.Contains(resp.Body.String(), tc.contains) {
			t.Errorf(
				"local route %q: %d %s; want %d and %q",
				tc.path,
				resp.Code,
				resp.Body,
				tc.want,
				tc.contains,
			)
		}
	}
}

func TestUnitVirtualRejectsInvalidMembersBeforeServingIndex(t *testing.T) {
	for _, members := range [][]string{
		{"pods-local", "pods-local"},
		{"pods-virtual"},
	} {
		t.Run(strings.Join(members, ","), func(t *testing.T) {
			f := newRouteFixture(t)
			c, err := f.db.Repos().Get(t.Context(), "pods-virtual")
			if err != nil {
				t.Fatal(err)
			}
			c.Repositories = members
			if err := f.db.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			resp := f.get("pods-virtual", "all_pods.txt")
			if resp.Code != http.StatusBadRequest {
				t.Errorf("virtual member cycle/duplicate returned %d: %s", resp.Code, resp.Body)
			}
		})
	}
}

func TestUnitVirtualSourceSelectsMemberWithMatchingArchive(t *testing.T) {
	f := newRouteFixture(t)
	f.seedRemote(t)
	second := repo.Config{
		Key: "pods-remote-second", Class: repo.Remote, PackageType: repo.CocoaPods,
		URL: f.up.URL, PodsCdnURL: f.up.URL + "/second",
	}
	enabled := true
	second.ExternalDependenciesEnabled = &enabled
	second.ExternalDependenciesPatterns = []string{f.up.URL + "/**"}
	if err := f.db.Repos().Create(t.Context(), *repo.Defaults(&second)); err != nil {
		t.Fatal(err)
	}
	firstSpec := podfmt.SpecPath("RemotePod", "2.0.0")
	f.up.Set("/"+firstSpec, fakeupstream.Resource{Body: []byte(
		`{"name":"RemotePod","version":"2.0.0","source":{"http":"` + f.up.URL + `/RemotePod.zip"}}`,
	)})
	f.up.Set("/second/"+firstSpec, fakeupstream.Resource{Body: []byte(
		`{"name":"RemotePod","version":"2.0.0","source":{"http":"` + f.up.URL + `/RemotePod.tar.gz"}}`,
	)})
	c, err := f.db.Repos().Get(t.Context(), "pods-virtual")
	if err != nil {
		t.Fatal(err)
	}
	c.Repositories = []string{"pods-remote", "pods-remote-second"}
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	resp := f.get("pods-virtual", "sources/RemotePod/2.0.0/source.tar.gz")
	if resp.Code != http.StatusOK || !bytes.Equal(resp.Body.Bytes(), sourceTar(t)) {
		t.Fatalf("virtual source did not select matching remote: %d %s", resp.Code, resp.Body)
	}
	if got := f.up.Count(http.MethodGet, "/RemotePod.zip"); got != 0 {
		t.Errorf("wrong source member fetched %d times", got)
	}
}

func TestUnitVirtualStopsOnInvalidUpstreamMetadata(t *testing.T) {
	f := newRouteFixture(t)
	f.seedLocal("LocalPod", "1.0.0")
	f.up.Set("/all_pods.txt", fakeupstream.Resource{Body: []byte("../escape\n")})
	resp := f.get("pods-virtual", "all_pods.txt")
	if resp.Code == http.StatusOK || strings.Contains(resp.Body.String(), "LocalPod") {
		t.Fatalf("invalid upstream index was silently skipped: %d %s", resp.Code, resp.Body)
	}
	f.up.Set(
		"/"+podfmt.SpecPath("LocalPod", "1.0.0"),
		fakeupstream.Resource{Status: http.StatusServiceUnavailable},
	)
	c, err := f.db.Repos().Get(t.Context(), "pods-virtual")
	if err != nil {
		t.Fatal(err)
	}
	c.Repositories = []string{"pods-remote", "pods-local"}
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	resp = f.get("pods-virtual", podfmt.SpecPath("LocalPod", "1.0.0"))
	if resp.Code == http.StatusOK {
		t.Fatal("upstream outage was treated as an absent member")
	}
}

func TestUnitRemoteAndVirtualDoNotDiscloseForbiddenSource(t *testing.T) {
	f := newRouteFixture(t)
	f.seedRemote(t)
	if err := f.db.Permissions().Create(t.Context(), types.Permission{
		Name: "podspec-only",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"partial": types.ActionRead}},
			Targets: map[string]types.Target{
				"pods-remote":  {Include: []string{"RemotePod/2.0.0/safe.txt"}},
				"pods-virtual": {Include: []string{"RemotePod/2.0.0/safe.txt"}},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"pods-remote", "pods-virtual"} {
		spec := f.getAs("partial", key, podfmt.SpecPath("RemotePod", "2.0.0"))
		if spec.Code == http.StatusOK || strings.Contains(spec.Body.String(), f.up.URL) {
			t.Errorf("%s disclosed source without artifact READ: %d %s", key, spec.Code, spec.Body)
		}
		archive := f.getAs("partial", key, "sources/RemotePod/2.0.0/source.tar.gz")
		if archive.Code == http.StatusOK {
			t.Errorf("%s served source without artifact READ", key)
		}
	}
	if got := f.up.Count(http.MethodGet, "/RemotePod.tar.gz"); got != 0 {
		t.Errorf("denied source reached upstream %d times", got)
	}
	if got := f.up.Count(http.MethodGet, "/"+podfmt.SpecPath("RemotePod", "2.0.0")); got == 0 {
		t.Fatal("permission scenario never reached the CDN spec")
	}
}

func TestUnitLocalRefusesCorruptIndexedPodspec(t *testing.T) {
	f := newRouteFixture(t)
	f.seedLocal("LocalPod", "1.0.0")
	p, err := f.db.Packages().Ensure(t.Context(), types.Package{
		RepoKey: "pods-local", Type: repo.CocoaPods, Name: "LocalPod",
	})
	if err != nil {
		t.Fatal(err)
	}
	v, err := f.db.Packages().Version(t.Context(), p.ID, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	v.Metadata = []byte("broken")
	if _, err := f.db.Packages().PutVersion(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	resp := f.get("pods-local", "all_pods.txt")
	if resp.Code == http.StatusOK {
		t.Fatal("corrupt local index was advertised")
	}
	archive, err := json.Marshal(podfmt.Archive{
		Name: "LocalPod", Version: "1.0.0", Spec: []byte(`{"name":"LocalPod"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	v.Metadata = archive
	if _, err := f.db.Packages().PutVersion(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	resp = f.get("pods-local", podfmt.SpecPath("LocalPod", "1.0.0"))
	if resp.Code != http.StatusOK {
		t.Fatalf("valid index failed to serve: %d %s", resp.Code, resp.Body)
	}
	// Valid metadata can still contain an unreadable stored podspec when a
	// backend is corrupted. It must fail closed at the CDN projection.
	v.Metadata = []byte(`{"name":"LocalPod","version":"1.0.0","spec":"bm90LWpzb24="}`)
	if _, err := f.db.Packages().PutVersion(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"pods-local", "pods-virtual"} {
		resp = f.get(key, podfmt.SpecPath("LocalPod", "1.0.0"))
		if resp.Code == http.StatusOK {
			t.Errorf("%s served a corrupt indexed podspec", key)
		}
	}
}

func TestUnitVirtualSkipsMissingMemberButRejectsWrongFormat(t *testing.T) {
	f := newRouteFixture(t)
	f.seedLocal("LocalPod", "1.0.0")
	c, err := f.db.Repos().Get(t.Context(), "pods-virtual")
	if err != nil {
		t.Fatal(err)
	}
	c.Repositories = []string{"missing-member", "pods-local"}
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	resp := f.get("pods-virtual", "all_pods.txt")
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "LocalPod") {
		t.Fatalf("missing member masked healthy member: %d %s", resp.Code, resp.Body)
	}
	other := repo.Config{Key: "npm-member", Class: repo.Local, PackageType: repo.NPM}
	if err := f.db.Repos().Create(t.Context(), *repo.Defaults(&other)); err != nil {
		t.Fatal(err)
	}
	c.Repositories = []string{"npm-member", "pods-local"}
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	resp = f.get("pods-virtual", "all_pods.txt")
	if resp.Code != http.StatusBadRequest || strings.Contains(resp.Body.String(), "LocalPod") {
		t.Fatalf("wrong-format virtual member served: %d %s", resp.Code, resp.Body)
	}
}

func TestUnitCDNRejectsAmbiguousRequestPaths(t *testing.T) {
	f := newRouteFixture(t)
	for _, path := range []string{
		"all_pods.txt/", "all_pods.txt;property=value", "Specs/../all_pods.txt",
		"all_pods.txt//extra", "",
	} {
		resp := f.get("pods-local", path)
		if resp.Code == http.StatusOK {
			t.Errorf("ambiguous CDN path %q was served", path)
		}
	}
}

func TestUnitRemoteSourceRequiresAvailableApprovedSpec(t *testing.T) {
	t.Run("missing spec", func(t *testing.T) {
		f := newRouteFixture(t)
		resp := f.get("pods-remote", "sources/AbsentPod/1.0.0/source.tar.gz")
		if resp.Code != http.StatusNotFound {
			t.Errorf("source without CDN spec: %d %s", resp.Code, resp.Body)
		}
	})
	t.Run("unapproved source", func(t *testing.T) {
		f := newRouteFixture(t)
		f.seedRemote(t)
		f.up.Set("/"+podfmt.SpecPath("RemotePod", "2.0.0"), fakeupstream.Resource{Body: []byte(
			`{"name":"RemotePod","version":"2.0.0","source":{"http":"https://unapproved.example.org/archive.tar.gz"}}`,
		)})
		resp := f.get("pods-remote", "sources/RemotePod/2.0.0/source.tar.gz")
		if resp.Code == http.StatusOK || f.up.Count(http.MethodGet, "/RemotePod.tar.gz") != 0 {
			t.Errorf("unapproved source reached archive: %d %s", resp.Code, resp.Body)
		}
	})
	t.Run("invalid CDN origin", func(t *testing.T) {
		f := newRouteFixture(t)
		c, err := f.db.Repos().Get(t.Context(), "pods-remote")
		if err != nil {
			t.Fatal(err)
		}
		c.PodsCdnURL = "ftp://unapproved.example.org/"
		if err := f.db.Repos().Update(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		resp := f.get("pods-remote", "all_pods.txt")
		if resp.Code == http.StatusOK || f.up.Count(http.MethodGet, "/all_pods.txt") != 0 {
			t.Errorf("invalid CDN origin fetched: %d %s", resp.Code, resp.Body)
		}
	})
}

func TestUnitVirtualSourceDoesNotIgnoreUpstreamOutage(t *testing.T) {
	f := newRouteFixture(t)
	f.up.Set("/"+podfmt.SpecPath("RemotePod", "2.0.0"), fakeupstream.Resource{
		Status: http.StatusServiceUnavailable,
	})
	resp := f.get("pods-virtual", "sources/RemotePod/2.0.0/source.tar.gz")
	if resp.Code == http.StatusOK {
		t.Fatal("virtual source treated upstream outage as a missing pod")
	}
}

func TestUnitMalformedStoredSpecIsNotProjected(t *testing.T) {
	_, err := specFor("https://registry.example.org", "pods-local", podfmt.Archive{
		Name: "LocalPod", Version: "1.0.0", Spec: []byte("not JSON"),
	})
	if err == nil {
		t.Fatal("malformed stored podspec was projected")
	}
}

func TestUnitEmptyRemoteIndexesAndAbsentVirtualSource(t *testing.T) {
	f := newRouteFixture(t)
	f.up.Set("/all_pods.txt", fakeupstream.Resource{Body: []byte{}})
	f.up.Set("/"+podfmt.ShardFile("RemotePod"), fakeupstream.Resource{Body: []byte("\n")})
	for _, path := range []string{"all_pods.txt", podfmt.ShardFile("RemotePod")} {
		resp := f.get("pods-remote", path)
		if resp.Code != http.StatusOK || resp.Body.Len() != 0 {
			t.Errorf("empty valid CDN index %q: %d %s", path, resp.Code, resp.Body)
		}
	}
	resp := f.get("pods-virtual", "sources/AbsentPod/1.0.0/source.tar.gz")
	if resp.Code != http.StatusNotFound {
		t.Errorf("absent virtual source: %d %s", resp.Code, resp.Body)
	}
}
