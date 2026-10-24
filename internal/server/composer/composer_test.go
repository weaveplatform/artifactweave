package composer

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type fixture struct {
	t      *testing.T
	router http.Handler
	db     *inmem.Store
	items  *item.Service
	blobs  blob.Store
}

func newFixture(t *testing.T) *fixture { return newFixtureWithSource(t, nil) }

func newFixtureWithSource(t *testing.T, remote *source.Service) *fixture {
	t.Helper()
	db := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	items := &item.Service{Store: db, Blobs: blobs}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := r.Header.Get("X-User")
			if name == "" {
				name = "admin"
			}
			principal, err := auth.Authorize(r.Context(), authz.Sources{Permissions: db.Permissions(), Groups: db.Groups(), AnonymousEnabled: true}, auth.Principal{Name: name, Admin: name == "admin"})
			if err != nil {
				t.Fatal(err)
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
		})
	})
	opts := pkgroute.Options{Store: db, Items: func() *item.Service { return items },
		ItemError: func(_ context.Context, _ string, err error) error {
			var reject *format.Rejection
			if errors.As(err, &reject) {
				return apierror.New(reject.Status, reject.Message)
			}
			return err
		},
		ServeItem: func(w http.ResponseWriter, r *http.Request, _ repo.Config, it types.Item) {
			body, _, err := blobs.Open(r.Context(), it.Actual.SHA256)
			if err != nil {
				t.Fatal(err)
			}
			defer body.Close()
			if r.Method != http.MethodHead {
				_, _ = io.Copy(w, body)
			}
		},
	}
	pkgroute.Mount(r, opts, Protocol(Options{Blobs: blobs, RemoteSources: remote}))
	return &fixture{t, r, db, items, blobs}
}

func (f *fixture) create(key string, class repo.Class, members ...string) repo.Config {
	f.t.Helper()
	c := *repo.Defaults(&repo.Config{Key: key, Class: class, PackageType: repo.Composer, Repositories: members})
	if err := f.db.Repos().Create(context.Background(), c); err != nil {
		f.t.Fatal(err)
	}
	return c
}

func (f *fixture) request(method, path string) *httptest.ResponseRecorder {
	return f.requestAs(method, path, "admin")
}

func (f *fixture) requestAs(method, path, user string) *httptest.ResponseRecorder {
	f.t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("X-User", user)
	f.router.ServeHTTP(w, r)
	return w
}

func makeZIP(t *testing.T, version, marker string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for path, contents := range map[string]string{
		"composer.json":  `{"name":"acme/widget","version":"` + version + `","type":"library"}`,
		"src/Widget.php": "<?php // " + marker,
	} {
		w, err := z.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, contents)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func (f *fixture) deploy(c repo.Config, version, marker string) []byte {
	f.t.Helper()
	archive := makeZIP(f.t, version, marker)
	_, err := f.items.Deploy(context.Background(), item.Deploy{Target: item.Target{Repo: c, Path: "acme/widget/" + version + ".zip"}, Body: bytes.NewReader(archive), By: "admin"})
	if err != nil {
		f.t.Fatal(err)
	}
	return archive
}

func TestAccHostedComposerMetadataAndDist(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) {
			f := newFixture(t)
			c := f.create("php", class)
			archive := f.deploy(c, "1.2.3", "first")
			f.deploy(c, "1.2.4", "second")
			base := "/artifactory/api/composer/php"
			root := f.request("GET", base+"/packages.json")
			if root.Code != 200 || !strings.Contains(root.Body.String(), `"metadata-url"`) || strings.Contains(root.Body.String(), `"1.2.3"`) {
				t.Fatalf("root %d %s", root.Code, root.Body)
			}
			v1req := httptest.NewRequest(http.MethodGet, base+"/packages.json", nil)
			v1req.Header.Set("User-Agent", "Composer/1.10.28")
			v1root := httptest.NewRecorder()
			f.router.ServeHTTP(v1root, v1req)
			if v1root.Code != 200 || strings.Contains(v1root.Body.String(), `"metadata-url"`) {
				t.Fatalf("v1 root %d %s", v1root.Code, v1root.Body)
			}
			var catalog map[string]any
			if err := json.Unmarshal(v1root.Body.Bytes(), &catalog); err != nil {
				t.Fatal(err)
			}
			packages := catalog["packages"].(map[string]any)
			if _, ok := packages["acme/widget"].(map[string]any)["1.2.3"]; !ok {
				t.Fatal("v1 catalog lacks version key")
			}
			p2 := f.request("GET", base+"/p2/acme/widget.json")
			if p2.Code != 200 || !strings.Contains(p2.Body.String(), `"shasum"`) {
				t.Fatalf("p2 %d %s", p2.Code, p2.Body)
			}
			if got := f.request("GET", base+"/p2/acme/widget~dev.json"); got.Code != 404 {
				t.Fatalf("dev %d", got.Code)
			}
			if got := f.request("GET", base+"/dist/acme/widget/1.2.3.zip"); got.Code != 200 || !bytes.Equal(got.Body.Bytes(), archive) {
				t.Fatalf("dist %d", got.Code)
			}
			if got := f.requestAs("GET", base+"/packages.json", "ungranted"); got.Code != 403 {
				t.Fatalf("root permission %d", got.Code)
			}
			if got := f.requestAs("GET", base+"/dist/acme/widget/1.2.3.zip", "ungranted"); got.Code != 403 {
				t.Fatalf("dist permission %d", got.Code)
			}
			if got := f.request("POST", base+"/reindex"); got.Code != 200 {
				t.Fatalf("reindex %d %s", got.Code, got.Body)
			}
			if _, err := f.db.Items().Delete(context.Background(), c.Key, false, "acme/widget/1.2.3.zip"); err != nil {
				t.Fatal(err)
			}
			if got := f.request("GET", base+"/p2/acme/widget.json"); got.Code != 200 || strings.Contains(got.Body.String(), `"1.2.3"`) || !strings.Contains(got.Body.String(), `"1.2.4"`) {
				t.Fatalf("deleted version still visible: %d %s", got.Code, got.Body)
			}
			f.deploy(c, "1.2.3", "restored")
			if got := f.request("GET", base+"/p2/acme/widget.json"); got.Code != 200 {
				t.Fatalf("restored p2 %d", got.Code)
			}
		})
	}
}

func TestAccComposerVirtualMemberOrderAndBadPaths(t *testing.T) {
	f := newFixture(t)
	first := f.create("first", repo.Local)
	second := f.create("second", repo.Federated)
	f.deploy(first, "1.0.0", "first")
	f.deploy(second, "1.0.0", "second")
	f.create("all", repo.Virtual, "first", "second")
	base := "/artifactory/api/composer/all"
	if got := f.request("GET", base+"/p2/acme/widget.json"); got.Code != 200 {
		t.Fatalf("virtual p2 %d %s", got.Code, got.Body)
	}
	if got := f.request("GET", base+"/dist/acme/widget/1.0.0.zip"); got.Code != 200 || !bytes.Contains(got.Body.Bytes(), []byte("first")) {
		t.Fatalf("virtual dist %d", got.Code)
	}
	for _, path := range []string{"/p2/acme/../widget.json", "/p2/acme/%2Fwidget.json", "/dist/acme/widget/%2F.zip", "/p2/acme/widget.json?x=1"} {
		if got := f.request("GET", base+path); got.Code == 200 {
			t.Errorf("accepted %s", path)
		}
	}
}

func TestAccComposerRefusalsAndVirtualAuthority(t *testing.T) {
	f := newFixture(t)
	first := f.create("first", repo.Local)
	second := f.create("second", repo.Local)
	f.deploy(first, "1.0.0", "hidden first")
	f.deploy(second, "1.0.0", "visible second")
	v := f.create("all", repo.Virtual, "first", "second")
	base := "/artifactory/api/composer/all"
	if got := f.request("GET", base+"/packages.json"); got.Code != 200 {
		t.Fatalf("virtual root %d", got.Code)
	}
	if got := f.request("POST", base+"/reindex"); got.Code != 400 {
		t.Fatalf("virtual reindex %d", got.Code)
	}
	for _, path := range []string{base + "/packages.json?bad=1", base + "/p2/acme/missing.json", base + "/dist/acme/widget/nope.zip", base + "/dist/acme/widget/1.0.0.zip?bad=1", base + "/dist/acme/widget/invalid"} {
		if got := f.request("GET", path); got.Code == 200 {
			t.Errorf("unexpected success %s", path)
		}
	}
	if got := f.requestAs("GET", base+"/p2/acme/widget.json", "ungranted"); got.Code != 403 {
		t.Fatalf("denied p2 %d", got.Code)
	}
	if err := f.db.Permissions().Create(context.Background(), types.Permission{Name: "composer-second-only", Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {Actions: types.Grants{Users: map[string]types.Actions{"reader": types.ActionRead}}, Targets: map[string]types.Target{"all": {}, "second": {}}}}}); err != nil {
		t.Fatal(err)
	}
	if got := f.requestAs("GET", base+"/p2/acme/widget.json", "reader"); got.Code != 200 {
		t.Fatalf("filtered p2 %d %s", got.Code, got.Body)
	}
	if got := f.requestAs("GET", base+"/dist/acme/widget/1.0.0.zip", "reader"); got.Code != 200 || !bytes.Contains(got.Body.Bytes(), []byte("visible second")) {
		t.Fatalf("filtered dist %d", got.Code)
	}
	if got := f.requestAs("POST", "/artifactory/api/composer/first/reindex", "reader"); got.Code != 403 {
		t.Fatalf("manage denial %d", got.Code)
	}
	v.Repositories = []string{"first", "first"}
	if err := f.db.Repos().Update(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	if got := f.request("GET", base+"/p2/acme/widget.json"); got.Code != 400 {
		t.Fatalf("cycle %d", got.Code)
	}
}

func TestAccHostedComposerDevelopmentAndStoredIntegrity(t *testing.T) {
	f := newFixture(t)
	c := f.create("php", repo.Local)
	f.deploy(c, "dev-main", "development")
	base := "/artifactory/api/composer/php"
	if got := f.request("GET", base+"/p2/acme/widget.json"); got.Code != 404 {
		t.Fatalf("stable view exposed development release: %d", got.Code)
	}
	if got := f.request("HEAD", base+"/p2/acme/widget~dev.json"); got.Code != 200 || got.Body.Len() != 0 {
		t.Fatalf("development HEAD: %d %s", got.Code, got.Body)
	}
	if got := f.request("GET", base+"/p2/acme/widget~dev.json"); got.Code != 200 || !strings.Contains(got.Body.String(), "dev-main") {
		t.Fatalf("development metadata: %d %s", got.Code, got.Body)
	}
	if got := f.request("HEAD", base+"/dist/acme/widget/dev-main.zip"); got.Code != 200 || got.Body.Len() != 0 {
		t.Fatalf("development dist HEAD: %d %s", got.Code, got.Body)
	}
	if got := f.request("GET", base+"/dist/acme/widget/dev-other.zip"); got.Code != 404 {
		t.Fatalf("unknown version: %d", got.Code)
	}
	if _, err := f.items.Deploy(context.Background(), item.Deploy{Target: item.Target{Repo: c, Path: "acme/widget/2.0.0.zip"}, Body: bytes.NewReader([]byte("not a ZIP")), By: "admin"}); err == nil {
		t.Fatal("corrupt archive was accepted")
	}
	upload, err := f.blobs.BeginUpload(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := upload.Append(context.Background(), strings.NewReader("not a ZIP")); err != nil {
		t.Fatal(err)
	}
	info, err := upload.Commit(context.Background(), blob.Digests{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Items().Put(context.Background(), types.Item{RepoKey: c.Key, Name: "acme/widget/2.0.0.zip", Actual: types.Checksums{SHA256: info.Digests.SHA256}}); err != nil {
		t.Fatal(err)
	}
	if got := f.request("POST", base+"/reindex"); got.Code != 502 {
		t.Fatalf("corrupt stored archive passed reindex: %d %s", got.Code, got.Body)
	}
	if got := f.request("GET", base+"/packages.json"); got.Code != 502 {
		t.Fatalf("corrupt stored archive entered catalog: %d %s", got.Code, got.Body)
	}
	if _, err := f.db.Items().Delete(context.Background(), c.Key, false, "acme/widget/2.0.0.zip"); err != nil {
		t.Fatal(err)
	}
	if got := f.request("POST", base+"/reindex"); got.Code != 200 {
		t.Fatalf("reindex after rejected archive: %d %s", got.Code, got.Body)
	}
}

func TestAccComposerVirtualMissingMemberAndCacheAlias(t *testing.T) {
	f := newFixture(t)
	f.create("php", repo.Local)
	f.create("all", repo.Virtual, "gone", "php")
	if got := f.request("GET", "/artifactory/api/composer/all/p2/acme/widget.json"); got.Code != 404 {
		t.Fatalf("missing virtual package: %d %s", got.Code, got.Body)
	}
	if got := f.request("GET", "/artifactory/api/composer/php-cache/packages.json"); got.Code != 404 {
		t.Fatalf("hosted cache alias: %d %s", got.Code, got.Body)
	}
	if got := f.request("POST", "/artifactory/api/composer/php/reindex"); got.Code != 200 {
		t.Fatalf("empty reindex: %d %s", got.Code, got.Body)
	}
}

func TestUnitComposerDistributionUsesRequestScheme(t *testing.T) {
	for _, scheme := range []string{"http", "https"} {
		r := httptest.NewRequest("GET", scheme+"://registry.example/artifactory/api/composer/php/packages.json", nil)
		url := distDocument(r, "acme/widget", "1.0.0", "abc")["url"].(string)
		if !strings.HasPrefix(url, scheme+"://registry.example/") {
			t.Fatalf("dist URL %q for scheme %s", url, scheme)
		}
	}
}

func TestUnitComposerRouteParsingRejectsAmbiguousNames(t *testing.T) {
	if keys := resolveKey(""); len(keys) != 0 {
		t.Fatalf("empty repository key: %+v", keys)
	}
	for _, path := range []string{"p2/acme/widget.tar", "p2/acme/%ZZ.json", "p2/acme//widget.json", "p2/acme/widget%252fextra.json", "p2/Acme/widget.json"} {
		if name, _, ok := parseP2(path); ok {
			t.Errorf("accepted ambiguous p2 name %q: %q", path, name)
		}
	}
	for _, path := range []string{"dist/acme/widget/%ZZ.zip", "dist/acme/widget%2Fextra/1.0.zip", "dist/acme/widget/1.0%252fextra.zip", "dist/acme/widget/1.0.tar"} {
		if name, version, ok := parseDist(path); ok {
			t.Errorf("accepted ambiguous distribution %q: %s@%s", path, name, version)
		}
	}
	if _, err := (virtualSource{}).Item(context.Background(), nil, repo.Config{}, "acme/widget"); !errors.Is(err, source.ErrUnsupported) {
		t.Fatalf("virtual source item: %v", err)
	}
}
