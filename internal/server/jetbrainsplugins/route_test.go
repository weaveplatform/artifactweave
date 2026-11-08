package jetbrainsplugins_test

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/server/jetbrainsplugins"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/migration/access/accesstest"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const routeBase = "/artifactory/api/jetbrainsplugins/jetbrains-remote/"

type routeFixture struct {
	t     *testing.T
	up    *fakeupstream.Server
	clock *fakeupstream.Clock
	db    *inmem.Store
	items *item.Service
	h     http.Handler
	blobs blob.Store
}

func newRouteFixture(t *testing.T) *routeFixture {
	t.Helper()
	clock := fakeupstream.NewClock(time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC))
	up := fakeupstream.New(t, fakeupstream.Options{Now: clock.Now})
	db := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := *repo.Defaults(&repo.Config{
		Key: "jetbrains-remote", Class: repo.Remote,
		PackageType: repo.JetBrainsPlugins, URL: up.URL,
	})
	ttl := 10
	c.RetrievalCachePeriodSecs = &ttl
	if err := db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if err := db.Permissions().Create(t.Context(), types.Permission{
		Name: "jetbrains-read", Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{
					"reader": types.ActionRead,
				}},
				Targets: map[string]types.Target{"jetbrains-remote": {Include: []string{"**"}}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	items := &item.Service{Store: db, Blobs: blobs, Now: clock.Now, Formats: ecosystem.For}
	resolver, err := remote.NewResolver(db, nil, remote.ResolverOptions{
		RevisionKey: []byte(strings.Repeat("k", 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	pool := new(remote.Pool)
	policy := &remote.Policy{
		Store: db, Blobs: blobs, Items: items, Pool: pool,
		Key: []byte(strings.Repeat("k", 32)), Now: clock.Now,
		Options: resolver.Options,
	}
	t.Cleanup(pool.Close)
	svc := &source.Service{
		HTTP: policy, Cache: policy,
		Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
			return db.RemoteCache().Source(ctx, c.Key)
		},
	}
	router := chi.NewRouter()
	router.Use(accesstest.Over(t, db).Middleware(t, true, func(r *http.Request) kernel.Principal {
		p := kernel.Principal{Name: r.Header.Get("X-User")}
		if p.Name == "" {
			p = kernel.Anonymous
		}
		return p
	}))
	pkgroute.Mount(router, pkgroute.Options{
		Store: db, Items: func() *item.Service { return items },
		ServeItem: func(w http.ResponseWriter, r *http.Request, _ repo.Config, it types.Item) {
			body, _, err := blobs.Open(r.Context(), it.Actual.SHA256)
			if err != nil {
				t.Fatal(err)
			}
			defer body.Close()
			w.Header().Set("Content-Type", it.MimeType)
			if r.Method == http.MethodGet {
				_, _ = io.Copy(w, body)
			}
		},
	}, jetbrainsplugins.Protocol(svc))
	return &routeFixture{t: t, up: up, clock: clock, db: db, items: items, h: router, blobs: blobs}
}

func (f *routeFixture) get(path, user string) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(http.MethodGet, routeBase+path, nil)
	r.Header.Set("X-User", user)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func (f *routeFixture) configure(change func(*repo.Config)) {
	f.t.Helper()
	c, err := f.db.Repos().Get(f.t.Context(), "jetbrains-remote")
	if err != nil {
		f.t.Fatal(err)
	}
	change(&c)
	if err := f.db.Repos().Update(f.t.Context(), c); err != nil {
		f.t.Fatal(err)
	}
}

func fixtureJar(t *testing.T, id string, comment ...string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	if len(comment) != 0 {
		if err := z.SetComment(comment[0]); err != nil {
			t.Fatal(err)
		}
	}
	f, err := z.Create("META-INF/plugin.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write([]byte(`<idea-plugin><id>` + id + `</id><name>Weave Fixture</name>` +
		`<version>1.0.0</version><idea-version since-build="252"/>` +
		`</idea-plugin>`))
	if err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func (f *routeFixture) seed(jar []byte) {
	f.up.Set("/api/search/updates/compatible", fakeupstream.Resource{
		Body: []byte(
			`[{"id":12345,"pluginId":98765,"version":"1.0.0","pluginXmlId":"com.weave.fixture"}]`,
		),
		Header: http.Header{"Content-Type": {"application/graphql+json"}},
	})
	f.up.Set("/files/98765/12345/meta.json", fakeupstream.Resource{
		Body: []byte(
			fmt.Sprintf(
				`{"id":12345,"xmlId":"com.weave.fixture","name":"Weave Fixture","version":"1.0.0","since":"252","size":%d}`,
				len(jar),
			),
		),
		Header: http.Header{"Content-Type": {"application/json"}},
	})
	f.up.Set("/pluginManager", fakeupstream.Resource{
		Redirect: "/files/98765/12345/weave-fixture.jar",
	})
	f.up.Set("/files/98765/12345/weave-fixture.jar", fakeupstream.Resource{
		Body: jar, Header: http.Header{"Content-Type": {"application/java-archive"}},
	})
}

func TestAccRemoteNativeInstallFlowColdWarmOffline(t *testing.T) {
	f := newRouteFixture(t)
	jar := fixtureJar(t, "com.weave.fixture")
	f.seed(jar)
	paths := []string{
		"api/search/updates/compatible?build=IC-252.28238.7&os=Linux+6.12.76-linuxkit&pluginXmlId=com.weave.fixture",
		"files/98765/12345/meta.json",
		"pluginManager?id=com.weave.fixture&build=IC-252.28238.7&uuid=transient&updatedFrom",
	}
	for range 2 {
		for i, path := range paths {
			rec := f.get(path, "reader")
			if rec.Code != http.StatusOK {
				t.Fatalf("step %d status %d: %s", i, rec.Code, rec.Body.String())
			}
			if i == 2 && (!bytes.Equal(rec.Body.Bytes(), jar) ||
				!strings.Contains(rec.Header().Get("Content-Disposition"), "plugin-com-weave-fixture.jar")) {
				t.Fatalf(
					"archive bytes/filename mismatched at step %d: body %d/%d, disposition %q, media %q",
					i,
					len(
						rec.Body.Bytes(),
					),
					len(jar),
					rec.Header().Get("Content-Disposition"),
					rec.Header().Get("Content-Type"),
				)
			}
		}
	}
	for _, path := range []string{
		"/api/search/updates/compatible", "/files/98765/12345/meta.json", "/pluginManager",
		"/files/98765/12345/weave-fixture.jar",
	} {
		if n := f.up.Count(http.MethodGet, path); n != 1 {
			t.Errorf("%s fetched %d times, want 1", path, n)
		}
	}
	f.configure(func(c *repo.Config) { offline := true; c.Offline = &offline })
	for i, path := range paths {
		if rec := f.get(path, "reader"); rec.Code != http.StatusOK {
			t.Fatalf("offline step %d status %d: %s", i, rec.Code, rec.Body.String())
		}
	}
}

func TestAccRemotePlatformArchivesRemainDistinctOffline(t *testing.T) {
	f := newRouteFixture(t)
	f.seed(fixtureJar(t, "com.weave.fixture"))
	variants := []struct {
		osName, arch string
		query        url.Values
		archive      []byte
	}{
		{osName: "Linux 6.12.76-linuxkit", arch: "X86_64"},
		{osName: "Linux 6.12.76-linuxkit", arch: "ARM64"},
		{osName: "macOS 26.5", arch: "ARM64"},
	}
	for i := range variants {
		v := &variants[i]
		v.query = url.Values{
			"id": {"com.weave.fixture"}, "build": {"IU-262.11879.100"},
			"os": {v.osName}, "arch": {v.arch}, "uuid": {"first-profile"},
		}
		v.archive = fixtureJar(t, "com.weave.fixture", v.osName+"/"+v.arch)
		f.up.Set("/files/98765/12345/weave-fixture.jar", fakeupstream.Resource{
			Body: v.archive, Header: http.Header{"Content-Type": {"application/java-archive"}},
		})
		rec := f.get("pluginManager?"+v.query.Encode(), "reader")
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), v.archive) {
			t.Fatalf(
				"cold %s/%s archive: status %d, body %q",
				v.osName,
				v.arch,
				rec.Code,
				rec.Body.Bytes(),
			)
		}
	}
	f.configure(func(c *repo.Config) { offline := true; c.Offline = &offline })
	for _, v := range variants {
		v.query.Set("uuid", "fresh-profile")
		rec := f.get("pluginManager?"+v.query.Encode(), "reader")
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), v.archive) {
			t.Fatalf(
				"offline %s/%s archive: status %d, body %q",
				v.osName,
				v.arch,
				rec.Code,
				rec.Body.Bytes(),
			)
		}
	}
	for _, path := range []string{"/pluginManager", "/files/98765/12345/weave-fixture.jar"} {
		if got := f.up.Count(http.MethodGet, path); got != len(variants) {
			t.Errorf("%s fetched %d times, want one per platform (%d)", path, got, len(variants))
		}
	}
}

func TestAccRemoteRejectsWrongArchiveIdentityAndNoReadGrant(t *testing.T) {
	f := newRouteFixture(t)
	f.seed(fixtureJar(t, "com.other"))
	archive := "pluginManager?id=com.weave.fixture&build=IC-252.28238.7"
	if rec := f.get(archive, "reader"); rec.Code != http.StatusBadGateway {
		t.Fatalf("wrong plugin archive status %d: %s", rec.Code, rec.Body.String())
	}
	if rec := f.get(archive, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous archive status %d: %s", rec.Code, rec.Body.String())
	}
	if rec := f.get(archive, "reader"); rec.Code != http.StatusBadGateway {
		t.Fatalf("second invalid archive status %d: %s", rec.Code, rec.Body.String())
	}
	if n := f.up.Count(http.MethodGet, "/pluginManager"); n != 2 {
		t.Fatalf("invalid archive cached after %d upstream requests", n)
	}
}

func TestAccRemoteMetadataUpstreamFailureThenRefresh(t *testing.T) {
	f := newRouteFixture(t)
	path := "api/search/updates/compatible?build=IC-252.28238.7&pluginXmlId=com.weave.fixture"
	f.up.Set("/api/search/updates/compatible", fakeupstream.Resource{
		Status: http.StatusServiceUnavailable,
	})
	if rec := f.get(path, "reader"); rec.Code < 500 {
		t.Fatalf("upstream failure returned %d: %s", rec.Code, rec.Body.String())
	}
	f.up.Set("/api/search/updates/compatible", fakeupstream.Resource{
		Body: []byte(
			`[{"id":12345,"pluginId":98765,"version":"1.0.0","pluginXmlId":"com.weave.fixture"}]`,
		),
		Header: http.Header{"Content-Type": {"application/graphql+json"}},
	})
	if rec := f.get(path, "reader"); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"version":"1.0.0"`) {
		t.Fatalf("recovered metadata = %d %s", rec.Code, rec.Body.String())
	}
	f.up.Set("/api/search/updates/compatible", fakeupstream.Resource{
		Body: []byte(
			`[{"id":12346,"pluginId":98765,"version":"2.0.0","pluginXmlId":"com.weave.fixture"}]`,
		),
		Header: http.Header{"Content-Type": {"application/graphql+json"}},
	})
	f.clock.Advance(11 * time.Second)
	if rec := f.get(path, "reader"); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"version":"2.0.0"`) {
		t.Fatalf("refreshed metadata = %d %s", rec.Code, rec.Body.String())
	}
	if n := f.up.Count(http.MethodGet, "/api/search/updates/compatible"); n != 2 {
		t.Fatalf("metadata upstream GETs %d, want recovery and refresh", n)
	}
}

func TestAccRemoteNativeRouteRejectsWrites(t *testing.T) {
	f := newRouteFixture(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		r := httptest.NewRequest(
			method,
			routeBase+"pluginManager?id=com.weave.fixture&build=IC-252.28238.7",
			nil,
		)
		r.Header.Set("X-User", "reader")
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, r)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d %s", method, w.Code, w.Body.String())
		}
	}
}

func TestAccRemoteLegacyIDArchiveHasSafeFilename(t *testing.T) {
	f := newRouteFixture(t)
	f.seed(fixtureJar(t, "RSS/Atom feed reader"))
	rec := f.get("pluginManager?id=RSS%2FAtom+feed+reader&build=IC-252.28238.7", "reader")
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy ID download = %d %s", rec.Code, rec.Body.String())
	}
	name := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(name, `attachment; filename="plugin-opaque-`) ||
		!strings.HasSuffix(name, `.jar"`) || strings.ContainsAny(name, "/\\") {
		t.Fatalf("unsafe legacy ID filename %q", name)
	}
}

func TestAccRemoteCatalogPreservesOfficialLegacyIdentifiers(t *testing.T) {
	f := newRouteFixture(t)
	f.up.Set("/api/search/plugins", fakeupstream.Resource{
		Body:   []byte(`[{"id":42,"name":"Gold Section","xmlId":"Gold Section","updateId":101}]`),
		Header: http.Header{"Content-Type": {"application/graphql+json"}},
	})
	f.up.Set("/files/pluginsXMLIds.json", fakeupstream.Resource{
		Body:   []byte(`["Gold Section","RSS/Atom feed reader"]`),
		Header: http.Header{"Content-Type": {"application/json"}},
	})
	for _, path := range []string{
		"api/search/plugins?build=IC-252.28238.7&search=Gold",
		"files/pluginsXMLIds.json",
	} {
		for range 2 {
			rec := f.get(path, "reader")
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Gold Section") {
				t.Fatalf("catalog %s = %d %s", path, rec.Code, rec.Body.String())
			}
		}
	}
	if n := f.up.Count(http.MethodGet, "/api/search/plugins"); n != 1 {
		t.Errorf("catalog search fetched %d times", n)
	}
	if n := f.up.Count(http.MethodGet, "/files/pluginsXMLIds.json"); n != 1 {
		t.Errorf("plugin ID list fetched %d times", n)
	}
}

func TestAccRemoteRejectsEncodedSlashInRoutePath(t *testing.T) {
	f := newRouteFixture(t)
	rec := f.get("files%2F98765%2F12345%2Fmeta.json", "reader")
	if rec.Code < 400 {
		t.Fatalf("encoded slash path returned %d: %s", rec.Code, rec.Body.String())
	}
	if n := f.up.Count(http.MethodGet, "/files/98765/12345/meta.json"); n != 0 {
		t.Fatalf("encoded slash path reached upstream %d times", n)
	}
}

func TestAccRemoteCacheDeletionRepopulatesNativeMetadata(t *testing.T) {
	f := newRouteFixture(t)
	f.seed(fixtureJar(t, "com.weave.fixture"))
	path := "api/search/updates/compatible?build=IC-252.28238.7&pluginXmlId=com.weave.fixture"
	if rec := f.get(path, "reader"); rec.Code != http.StatusOK {
		t.Fatalf("initial metadata = %d %s", rec.Code, rec.Body.String())
	}
	entries, _, err := f.db.RemoteCache().List(t.Context(), "jetbrains-remote",
		"plugins/com.weave.fixture", store.Page{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var cachedPath string
	for _, entry := range entries {
		if strings.Contains(entry.Token.Key.Path, "/compatible-") {
			cachedPath = entry.Token.Key.Path
		}
	}
	if cachedPath == "" {
		t.Fatalf("no compatible-update cache entry in %+v", entries)
	}
	c, err := f.db.Repos().Get(t.Context(), "jetbrains-remote")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.items.Delete(
		t.Context(),
		item.Target{Repo: c, Cached: true, Path: cachedPath},
		"admin",
	); err != nil {
		t.Fatal(err)
	}
	if rec := f.get(path, "reader"); rec.Code != http.StatusOK {
		t.Fatalf("repopulated metadata = %d %s", rec.Code, rec.Body.String())
	}
	if n := f.up.Count(http.MethodGet, "/api/search/updates/compatible"); n != 2 {
		t.Fatalf("cache deletion fetched %d times, want 2", n)
	}
}

func TestAccPinnedIDEInstallsThroughNativeMarketplaceHost(t *testing.T) {
	ide := os.Getenv("ARTIFACTWEAVE_JETBRAINS_LOCAL_IDEA_DIR")
	image := os.Getenv("ARTIFACTWEAVE_JETBRAINS_IMAGE")
	if ide == "" && image == "" {
		t.Skip(
			"set ARTIFACTWEAVE_JETBRAINS_IMAGE or ARTIFACTWEAVE_JETBRAINS_LOCAL_IDEA_DIR for native IDE proof",
		)
	}
	if image == "" {
		image = "artifactweave-accept-p2:latest"
	}
	f := newRouteFixture(t)
	f.seed(fixtureJar(t, "com.weave.fixture"))
	if err := f.db.Permissions().Create(t.Context(), types.Permission{
		Name: "jetbrains-anonymous", Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{
					"anonymous": types.ActionRead,
				}},
				Targets: map[string]types.Target{"jetbrains-remote": {Include: []string{"**"}}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := httptest.NewRecorder()
			f.h.ServeHTTP(rec, r)
			t.Logf("IDE %s %s -> %d (%d bytes)", r.Method, r.URL.String(), rec.Code, rec.Body.Len())
			for key, values := range rec.Header() {
				for _, value := range values {
					w.Header().Add(key, value)
				}
			}
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
		}),
	)
	_ = server.Listener.Close()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	server.Listener = listener
	server.Start()
	defer server.Close()
	host := "127.0.0.1"
	if runtime.GOOS != "linux" {
		host = "host.docker.internal"
	}
	address := fmt.Sprintf("http://%s:%d", host, listener.Addr().(*net.TCPAddr).Port)
	for _, offline := range []bool{false, true} {
		f.configure(func(c *repo.Config) { c.Offline = &offline })
		work := t.TempDir()
		properties := "idea.plugins.host=" + address + routeBase[:len(routeBase)-1] + "\n" +
			"idea.config.path=/work/config\nidea.system.path=/work/system\n" +
			"idea.plugins.path=/work/plugins\nidea.log.path=/work/log\n"
		if err := os.WriteFile(
			filepath.Join(work, "idea.properties"),
			[]byte(properties),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		args := []string{
			"run", "--rm", "--network", "host",
			"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
			"-v", work + ":/work", "-w", "/work",
			"-e", "HOME=/work", "-e", "IDEA_PROPERTIES=/work/idea.properties",
		}
		if ide != "" {
			args = append(args, "-v", ide+":/opt/idea:ro")
		}
		args = append(args, image, "/opt/idea/bin/idea.sh", "installPlugins", "com.weave.fixture")
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		cancel()
		if err != nil ||
			!bytes.Contains(out, []byte("installed plugin: PluginNode{id=com.weave.fixture")) {
			t.Fatalf("pinned IDE install: %v: %s", err, out)
		}
	}
	for _, path := range []string{
		"/api/search/updates/compatible", "/files/98765/12345/meta.json", "/pluginManager",
		"/files/98765/12345/weave-fixture.jar",
	} {
		if n := f.up.Count(http.MethodGet, path); n != 1 {
			t.Errorf("native IDE request %s fetched %d times, want 1", path, n)
		}
	}
}
