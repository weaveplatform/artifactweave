package vagrant_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const base = "/artifactory/api/vagrant/boxes/"

type catalog struct {
	Name     string `json:"name"`
	Versions []struct {
		Version   string `json:"version"`
		Providers []struct {
			Name, URL, Checksum, Architecture string
			ChecksumType                      string `json:"checksum_type"`
		} `json:"providers"`
	} `json:"versions"`
}
type fixture struct {
	t      *testing.T
	db     *inmem.Store
	faults *faultStore
	items  *item.Service
	c      repo.Config
	router http.Handler
}

func newFixture(t *testing.T, class repo.Class) *fixture {
	t.Helper()
	db := inmem.New()
	faults := &faultStore{Store: db}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := *repo.Defaults(&repo.Config{Key: "boxes", Class: class, PackageType: repo.Vagrant})
	if err := db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	router := server.New(
		server.Config{
			Store:   faults,
			Blobs:   blobs,
			BaseURL: "https://public.example",
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	)
	return &fixture{
		t:      t,
		db:     db,
		faults: faults,
		c:      c,
		items:  &item.Service{Store: db, Blobs: blobs},
		router: router,
	}
}

func (f *fixture) do(method, path, user string, body []byte) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	p := auth.Principal{Name: user, Admin: user == "admin"}
	if user == "" {
		p = auth.Anonymous
	}
	p, err := auth.Authorize(
		r.Context(),
		authz.Sources{
			Permissions:      f.db.Permissions(),
			Groups:           f.db.Groups(),
			AnonymousEnabled: true,
		},
		p,
	)
	if err != nil {
		f.t.Fatal(err)
	}
	r = r.WithContext(auth.WithPrincipal(r.Context(), p))
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}

func archive(t *testing.T, provider string) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, e := range []struct{ name, body string }{{"metadata.json", `{"provider":"` + provider + `","architecture":"amd64"}`}, {"box.ovf", "provider fixture"}} {
		if err := tw.WriteHeader(
			&tar.Header{Name: e.name, Size: int64(len(e.body)), Mode: 0o644},
		); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func (f *fixture) publish(path, name, version, provider string) []byte {
	f.t.Helper()
	body := archive(f.t, provider)
	w := f.do(
		"PUT",
		"/artifactory/boxes/"+path+";box_name="+url.PathEscape(
			name,
		)+";box_version="+version+";box_provider="+provider,
		"admin",
		body,
	)
	if w.Code != 201 {
		f.t.Fatalf("publish %s: %d %s", path, w.Code, w.Body.String())
	}
	return body
}

func (f *fixture) catalog(path, user string) catalog {
	f.t.Helper()
	w := f.do("GET", base+path, user, nil)
	if w.Code != 200 {
		f.t.Fatalf("catalog: %d %s", w.Code, w.Body.String())
	}
	var c catalog
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		f.t.Fatal(err)
	}
	return c
}

func TestAccCatalogAndDownloads(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) {
			f := newFixture(t, class)
			body := f.publish("arbitrary/release%20one.box", "acme/widget", "1.0.0", "virtualbox")
			f.publish("different/path.box", "acme/widget", "1.0.0", "docker")
			f.publish("latest.box", "acme/widget", "2.0.0", "virtualbox")
			c := f.catalog("acme/widget", "admin")
			if c.Name != "acme/widget" || len(c.Versions) != 2 ||
				len(c.Versions[0].Providers) != 2 {
				t.Fatalf("catalog %+v", c)
			}
			provider := c.Versions[0].Providers[1]
			sum := sha256.Sum256(body)
			if provider.Name != "virtualbox" ||
				provider.URL != "https://public.example/artifactory/boxes/arbitrary/release%20one.box" ||
				provider.ChecksumType != "sha256" ||
				provider.Checksum != hex.EncodeToString(sum[:]) ||
				provider.Architecture != "amd64" {
				t.Fatalf("provider %+v", provider)
			}
			got := f.do("GET", provider.URL, "admin", nil)
			if got.Code != 200 || !bytes.Equal(body, got.Body.Bytes()) {
				t.Fatalf("download: %d %s", got.Code, got.Body.String())
			}
			head := f.do("HEAD", base+"acme/widget", "admin", nil)
			get := f.do("GET", base+"acme/widget", "admin", nil)
			if head.Code != 200 || head.Body.Len() != 0 ||
				head.Header().Get("Content-Length") != get.Header().Get("Content-Length") ||
				head.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("HEAD: %d %v %q", head.Code, head.Header(), head.Body.String())
			}
			filtered := f.catalog("acme/widget;box_provider=docker", "admin")
			if len(filtered.Versions) != 1 || len(filtered.Versions[0].Providers) != 1 ||
				filtered.Versions[0].Providers[0].Name != "docker" {
				t.Fatalf("filtered %+v", filtered)
			}
			if w := f.do("GET", base+"acme/widget;channel+=stable", "admin", nil); w.Code != 404 {
				t.Fatalf("mandatory filter: %d", w.Code)
			}
		})
	}
}

func TestAccCatalogLifecycle(t *testing.T) {
	f := newFixture(t, repo.Local)
	f.publish("one.box", "widget", "1.0.0", "virtualbox")
	it, err := f.db.Items().Get(t.Context(), "boxes", false, "one.box")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"vagrant.provider", "vagrant.architecture", "vagrant.sha256"} {
		if err := f.items.Annotate(
			t.Context(),
			f.c,
			it,
			types.Properties{key: {"forged"}},
			nil,
		); err == nil {
			t.Fatalf("accepted forged %s", key)
		}
	}
	if err := f.items.Annotate(
		t.Context(),
		f.c,
		it,
		types.Properties{"box_provider": {"docker"}},
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if w := f.do("GET", base+"widget", "admin", nil); w.Code != 404 {
		t.Fatalf("provider mismatch: %d", w.Code)
	}
	if err := f.items.Annotate(
		t.Context(),
		f.c,
		it,
		types.Properties{
			"box_provider": {"virtualbox"},
			"box_name":     {"renamed"},
			"box_version":  {"2.0.0"},
		},
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if c := f.catalog("renamed", "admin"); c.Versions[0].Version != "2.0.0" {
		t.Fatalf("renamed %+v", c)
	}
	if err := f.items.Annotate(t.Context(), f.c, it, nil, []string{"box_name"}); err != nil {
		t.Fatal(err)
	}
	if w := f.do("GET", base+"renamed", "admin", nil); w.Code != 404 {
		t.Fatalf("removed identity: %d", w.Code)
	}
	if err := f.items.Annotate(
		t.Context(),
		f.c,
		it,
		types.Properties{"box_name": {"renamed"}},
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if err := f.items.Reindex(t.Context(), f.c); err != nil {
		t.Fatal(err)
	}
	for _, move := range []bool{false, true} {
		from, to := "one.box", "copy.box"
		if move {
			from, to = "copy.box", "moved.box"
		}
		res, err := f.items.Transfer(
			t.Context(),
			item.Transfer{
				From: item.Target{Repo: f.c, Path: from},
				To:   item.Target{Repo: f.c, Path: to},
				Move: move,
				By:   "admin",
			},
		)
		if err != nil || res.Failed != 0 || res.Files != 1 {
			t.Fatalf("transfer %+v %v", res, err)
		}
	}
	if _, err := f.items.Delete(
		t.Context(),
		item.Target{Repo: f.c, Path: "one.box"},
		"admin",
	); err != nil {
		t.Fatal(err)
	}
	c := f.catalog("renamed", "admin")
	if !strings.HasSuffix(c.Versions[0].Providers[0].URL, "/moved.box") {
		t.Fatalf("moved %+v", c)
	}
	moved, err := f.db.Items().Get(t.Context(), "boxes", false, "moved.box")
	if err != nil {
		t.Fatal(err)
	}
	props, err := f.db.Items().Properties(t.Context(), moved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.items.Delete(
		t.Context(),
		item.Target{Repo: f.c, Path: "moved.box"},
		"admin",
	); err != nil {
		t.Fatal(err)
	}
	if w := f.do("GET", base+"renamed", "admin", nil); w.Code != 404 {
		t.Fatalf("deleted: %d", w.Code)
	}
	if _, err := f.items.Restore(
		t.Context(),
		item.Target{Repo: f.c, Path: "restored.box"},
		types.TrashRecord{Item: moved, Properties: props},
		"admin",
	); err != nil {
		t.Fatal(err)
	}
	f.catalog("renamed", "admin")
}

func TestAccCatalogStatusAndPermissions(t *testing.T) {
	f := newFixture(t, repo.Local)
	f.publish("visible/a.box", "widget", "1.0", "virtualbox")
	f.publish("hidden/a.box", "widget", "2.0", "docker")
	permission := types.Permission{
		Name: "reader",
		Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{"reader": types.ActionRead}},
				Targets: map[string]types.Target{"boxes": {Include: []string{"visible/**"}}},
			},
		},
	}
	if err := f.db.Permissions().Create(t.Context(), permission); err != nil {
		t.Fatal(err)
	}
	c := f.catalog("widget", "reader")
	if len(c.Versions) != 1 || c.Versions[0].Version != "1.0" {
		t.Fatalf("permission leak %+v", c)
	}
	for _, tc := range []struct {
		method, path, user string
		want               int
	}{{"GET", "widget", "", 401}, {"HEAD", "widget", "outsider", 403}, {"GET", "missing", "admin", 404}, {"GET", "a/b/c", "admin", 400}, {"GET", "widget/", "admin", 400}, {"GET", "widget?access_token=x", "admin", 400}, {"GET", "%2e%2e", "admin", 400}, {"GET", "widget;=invalid", "admin", 400}, {"POST", "widget", "admin", 405}} {
		t.Run(tc.method+tc.path+tc.user, func(t *testing.T) {
			w := f.do(tc.method, base+tc.path, tc.user, nil)
			if w.Code != tc.want {
				t.Fatalf("status %d want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
	excluded := "**"
	f.c.ExcludesPattern = &excluded
	if err := f.db.Repos().Update(t.Context(), f.c); err != nil {
		t.Fatal(err)
	}
	if w := f.do("GET", base+"widget", "admin", nil); w.Code != 404 {
		t.Fatalf("excluded: %d %s", w.Code, w.Body.String())
	}
	for _, class := range []repo.Class{repo.Remote, repo.Virtual} {
		t.Run(string(class), func(t *testing.T) {
			f := newFixture(t, class)
			if w := f.do("GET", base+"widget", "admin", nil); w.Code != 501 {
				t.Fatalf("class: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestAccPublishRejectsInvalidBoxes(t *testing.T) {
	f := newFixture(t, repo.Local)
	good := archive(t, "virtualbox")
	for _, tc := range []struct {
		path string
		body []byte
	}{{"bad.box", good}, {"bad.txt;box_name=x;box_version=1;box_provider=virtualbox", good}, {"bad.box;box_name=x;box_version=1;box_provider=docker", good}, {"bad.box;box_name=x;box_version=bad;box_provider=virtualbox", good}, {"bad.box;box_name=x;box_version=1;box_provider=virtualbox", []byte("not an archive")}} {
		w := f.do("PUT", "/artifactory/boxes/"+tc.path, "admin", tc.body)
		if w.Code != 400 {
			t.Fatalf("invalid %s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
	if w := f.do("GET", base+"x", "admin", nil); w.Code != 404 {
		t.Fatalf("failed upload indexed: %d", w.Code)
	}
}

var errFault = errors.New("injected store fault")

type faultStore struct {
	store.Store
	stage string
}

func (f *faultStore) Tx(ctx context.Context, fn func(context.Context) error) error {
	if f.stage == "tx" {
		return errFault
	}
	return f.Store.Tx(ctx, fn)
}

func (f *faultStore) Packages() store.PackageStore { return faultPackages{f.Store.Packages(), f.stage} }
func (f *faultStore) Items() store.ItemStore       { return faultItems{f.Store.Items(), f.stage} }

type faultPackages struct {
	store.PackageStore
	stage string
}

func (f faultPackages) Find(ctx context.Context, key, name string) (types.Package, error) {
	if f.stage == "find" {
		return types.Package{}, errFault
	}
	return f.PackageStore.Find(ctx, key, name)
}

func (f faultPackages) Versions(ctx context.Context, id int64) ([]types.PackageVersion, error) {
	if f.stage == "versions" {
		return nil, errFault
	}
	if f.stage == "manyversions" {
		return make([]types.PackageVersion, 10001), nil
	}
	return f.PackageStore.Versions(ctx, id)
}

func (f faultPackages) Files(ctx context.Context, id int64) ([]int64, error) {
	if f.stage == "files" {
		return nil, errFault
	}
	if f.stage == "manyfiles" {
		return make([]int64, 10001), nil
	}
	return f.PackageStore.Files(ctx, id)
}

type faultItems struct {
	store.ItemStore
	stage string
}

func (f faultItems) GetByID(ctx context.Context, id int64) (types.Item, error) {
	if f.stage == "item" {
		return types.Item{}, errFault
	}
	if f.stage == "missingitem" {
		return types.Item{}, store.ErrNotFound
	}
	return f.ItemStore.GetByID(ctx, id)
}

func (f faultItems) Properties(ctx context.Context, id int64) (types.Properties, error) {
	if f.stage == "properties" {
		return nil, errFault
	}
	if f.stage == "unverified" {
		return nil, nil
	}
	return f.ItemStore.Properties(ctx, id)
}

func TestAccCatalogStoreFailures(t *testing.T) {
	f := newFixture(t, repo.Local)
	f.publish("one.box", "widget", "1.0", "virtualbox")
	for _, stage := range []string{"tx", "find", "versions", "files", "item", "properties", "manyversions", "manyfiles", "missingitem", "unverified"} {
		t.Run(stage, func(t *testing.T) {
			f.faults.stage = stage
			want := 500
			if strings.HasPrefix(stage, "many") {
				want = 503
			}
			if stage == "missingitem" || stage == "unverified" {
				want = 404
			}
			w := f.do("GET", base+"widget", "admin", nil)
			if w.Code != want || strings.Contains(w.Body.String(), errFault.Error()) {
				t.Fatalf("fault %s: %d %s", stage, w.Code, w.Body.String())
			}
		})
	}
}

func TestAccDuplicateProviderUsesNewestReadableItem(t *testing.T) {
	f := newFixture(t, repo.Local)
	f.publish("a.box", "widget", "1.0", "virtualbox")
	f.publish("b.box", "widget", "1.0", "virtualbox")
	c := f.catalog("widget", "admin")
	if len(c.Versions[0].Providers) != 1 ||
		!strings.HasSuffix(c.Versions[0].Providers[0].URL, "/b.box") {
		t.Fatalf("newest %+v", c)
	}
	// Equal timestamps have a stable lexical path order.
	for _, path := range []string{"a.box", "b.box"} {
		it, err := f.db.Items().Get(t.Context(), "boxes", false, path)
		if err != nil {
			t.Fatal(err)
		}
		it.Updated = time.Unix(100, 0)
		if _, err := f.db.Items().Put(t.Context(), it); err != nil {
			t.Fatal(err)
		}
	}
	c = f.catalog("widget", "admin")
	if !strings.HasSuffix(c.Versions[0].Providers[0].URL, "/a.box") {
		t.Fatalf("tie %+v", c)
	}
}
