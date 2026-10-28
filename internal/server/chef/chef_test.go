package chef_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/chef"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const base = "/artifactory/api/chef/chef-local"

type fixture struct {
	t     *testing.T
	r     http.Handler
	db    *inmem.Store
	c     repo.Config
	items *item.Service
}

func newFixture(t *testing.T, class repo.Class) *fixture {
	return newFixtureWith(t, class, nil)
}

func newFixtureWith(t *testing.T, class repo.Class, tune func(*pkgroute.Protocol)) *fixture {
	t.Helper()
	return newFixtureStore(t, class, tune, nil)
}

func newFixtureStore(
	t *testing.T,
	class repo.Class,
	tune func(*pkgroute.Protocol),
	wrap func(store.Store) store.Store,
) *fixture {
	t.Helper()
	db := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	q, err := work.New(db, work.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var st store.Store = db
	if wrap != nil {
		st = wrap(st)
	}
	items := &item.Service{Store: st, Blobs: blobs, Work: q}
	if err := items.RegisterWork(q); err != nil {
		t.Fatal(err)
	}
	c := *repo.Defaults(&repo.Config{Key: "chef-local", Class: class, PackageType: repo.Chef})
	if class == repo.Remote {
		c.URL = "https://example.com/chef/"
	}
	if err := db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use(baseurl.Middleware(""))
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := r.Header.Get("X-User")
			p, err := auth.Authorize(r.Context(), authz.Sources{
				Permissions: db.Permissions(), Groups: db.Groups(), AnonymousEnabled: true,
			}, auth.Principal{Name: name, Admin: name == "admin", Anonymous: name == ""})
			if err != nil {
				t.Fatal(err)
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	})
	p := chef.Protocol(nil)
	if tune != nil {
		tune(&p)
	}
	pkgroute.Mount(r, pkgroute.Options{
		Store: st, Items: func() *item.Service { return items },
		ItemError: func(_ context.Context, _ string, err error) error {
			var refused *format.RejectionError
			if errors.As(err, &refused) {
				return apierror.New(refused.Status, refused.Message)
			}
			return err
		},
		ServeItem: func(w http.ResponseWriter, r *http.Request, _ repo.Config, it types.Item) {
			rc, _, err := blobs.Open(r.Context(), it.Actual.SHA256)
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Close()
			w.Header().Set("Content-Type", "application/octet-stream")
			if r.Method != http.MethodHead {
				_, _ = io.Copy(w, rc)
			}
		},
	}, p)
	return &fixture{t: t, r: r, db: db, c: c, items: items}
}

type metadataSource struct {
	bodyAt                 func(source.Resource) string
	metadataBase           string
	cachedItem             *types.Item
	skipMetadataValidation bool
	redirect               *url.URL
	checkNilURL            bool
	check                  func(context.Context, string) error
	missingRevalidate      bool
	revalidateError        error
	revalidateCheck        func(context.Context) error
	readError              error
	missingStream          bool
	badCachedItem          bool
	body                   string
	archive                []byte
	archiveSet             bool
	missingArchiveRV       bool
}

func (metadataSource) Item(
	context.Context,
	store.Store,
	repo.Config,
	string,
) (pkgroute.Found, error) {
	return pkgroute.Found{}, source.ErrUnsupported
}

func (s metadataSource) Read(
	r *http.Request,
	_ repo.Config,
	resource source.Resource,
) (source.Result, error) {
	if s.readError != nil {
		return source.Result{}, s.readError
	}
	if resource.Kind == source.ResourceKindArtifact {
		if !s.archiveSet {
			return source.Result{}, source.ErrUnavailable
		}
		if resource.CheckURL != nil {
			u, err := url.Parse(resource.ExternalURL)
			if err != nil {
				return source.Result{}, err
			}
			if s.redirect != nil {
				u = s.redirect
			}
			if s.checkNilURL {
				u = nil
			}
			if err := resource.CheckURL(r.Context(), u); err != nil {
				return source.Result{}, err
			}
		}
		if resource.Validate != nil {
			if err := resource.Validate(
				r.Context(),
				bytes.NewReader(s.archive),
				int64(len(s.archive)),
			); err != nil {
				return source.Result{}, err
			}
		}
		var revalidate func(context.Context) error
		if !s.missingArchiveRV {
			revalidate = func(context.Context) error { return nil }
		}
		return source.Result{
			Stream: &source.Stream{
				Body: io.NopCloser(bytes.NewReader(s.archive)), Status: http.StatusOK,
				Size: int64(len(s.archive)),
			},
			Revalidate: revalidate,
		}, nil
	}
	body := s.body
	if s.bodyAt != nil {
		body = s.bodyAt(resource)
	}
	if body == "" {
		body = `{"name":"widget","versions":[]}`
	}
	var revalidate func(context.Context) error
	if !s.missingRevalidate {
		revalidate = func(ctx context.Context) error {
			if s.revalidateCheck != nil {
				return s.revalidateCheck(ctx)
			}
			return s.revalidateError
		}
	}
	result := source.Result{
		Stream: &source.Stream{
			Body:   io.NopCloser(strings.NewReader(body)),
			Status: 200,
			Size:   int64(len(body)),
		},
		Metadata: source.MetadataResponse{
			BaseURL: resource.ExternalURL,
		},
		Revalidate: revalidate,
		CheckRead:  s.check,
	}
	if s.metadataBase != "" {
		result.Metadata.BaseURL = s.metadataBase
	}
	if resource.ValidateMetadata != nil && !s.skipMetadataValidation {
		if err := resource.ValidateMetadata(
			r.Context(),
			bytes.NewReader([]byte(body)),
			int64(len(body)),
			result.Metadata,
		); err != nil {
			return source.Result{}, err
		}
	}
	if s.missingStream {
		result.Stream = nil
	}
	if s.badCachedItem {
		result.Stream = nil
		result.Item = &types.Item{Actual: types.Checksums{SHA256: "missing"}}
	}
	if s.cachedItem != nil {
		result.Stream = nil
		result.Item = s.cachedItem
	}
	return result, nil
}

func (f *fixture) do(method, path, user string) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("X-User", user)
	w := httptest.NewRecorder()
	f.r.ServeHTTP(w, r)
	return w
}

func archive(t *testing.T, name, version string) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	files := map[string]string{
		"metadata.json": fmt.Sprintf(
			`{"name":%q,"version":%q,"dependencies":{"helper":">= 1.0.0"},"platforms":{"ubuntu":">= 20.04"}}`,
			name,
			version,
		),
		"recipes/default.rb": "# controlled recipe",
	}
	for p, b := range files {
		if err := tw.WriteHeader(
			&tar.Header{Name: name + "/" + p, Mode: 0o644, Size: int64(len(b))},
		); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(b)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func (f *fixture) upload(name string, body []byte, user string) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest("POST", base+"/api/v1/cookbooks/"+name, bytes.NewReader(body))
	r.Header.Set("X-User", user)
	r.Header.Set("Content-Type", "application/x-binary")
	w := httptest.NewRecorder()
	f.r.ServeHTTP(w, r)
	return w
}

func (f *fixture) deploy(c repo.Config, name, version string, body []byte) types.Item {
	f.t.Helper()
	it, err := f.items.Deploy(
		f.t.Context(),
		item.Deploy{
			Target: item.Target{Repo: c, Path: name + "/" + name + "-" + version + ".tar.gz"},
			Body:   bytes.NewReader(body),
			By:     "admin",
		},
	)
	if err != nil {
		f.t.Fatal(err)
	}
	return it
}

func TestAccHostedCookbookPublishResolveDeleteAndReindex(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) {
			f := newFixture(t, class)
			data := archive(t, "widget", "1.0.0")
			if got := f.upload("widget.tgz", data, "admin"); got.Code != 200 {
				t.Fatalf("publish %d %s", got.Code, got.Body.String())
			}
			if got := f.upload("widget.tgz", data, "admin"); got.Code != 409 {
				t.Fatalf("duplicate %d", got.Code)
			}
			for _, p := range []string{"/api/v1/cookbooks", "/api/v1/search?q=widget", "/api/v1/cookbooks/widget", "/api/v1/cookbooks/widget/versions/latest", "/api/v1/cookbooks/widget/versions/1_0_0", "/api/v1/cookbooks/widget/versions/1_0_0/download"} {
				got := f.do("GET", base+p, "admin")
				if got.Code != 200 {
					t.Fatalf("%s: %d %s", p, got.Code, got.Body.String())
				}
				if strings.HasSuffix(p, "download") && !bytes.Equal(got.Body.Bytes(), data) {
					t.Fatal("archive changed")
				}
				got = f.do("HEAD", base+p, "admin")
				if got.Code != 200 || got.Body.Len() != 0 {
					t.Fatalf("HEAD %s: %d", p, got.Code)
				}
				got = f.do("GET", base+p, "denied")
				if got.Code != 403 {
					t.Fatalf("denied %s: %d", p, got.Code)
				}
			}
			if err := f.items.Reindex(t.Context(), f.c); err != nil {
				t.Fatal(err)
			}
			if got := f.do(
				"DELETE",
				base+"/api/v1/cookbooks/widget/1.0.0",
				"denied",
			); got.Code != 403 {
				t.Fatal(got.Code)
			}
			if got := f.do(
				"DELETE",
				base+"/api/v1/cookbooks/widget/1.0.0",
				"admin",
			); got.Code != 200 {
				t.Fatal(got.Code, got.Body.String())
			}
			if got := f.do("GET", base+"/api/v1/cookbooks/widget", "admin"); got.Code != 404 {
				t.Fatal(got.Code)
			}
		})
	}
}

func TestAccMultipartPublishAndWholeCookbookDelete(t *testing.T) {
	f := newFixture(t, repo.Local)
	var out bytes.Buffer
	m := multipart.NewWriter(&out)
	p, err := m.CreateFormFile("tarball", "widget.tgz")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = p.Write(archive(t, "widget", "1.0.0"))
	_ = m.Close()
	r := httptest.NewRequest("POST", base+"/api/v1/cookbooks", &out)
	r.Header.Set("Content-Type", m.FormDataContentType())
	r.Header.Set("X-User", "admin")
	w := httptest.NewRecorder()
	f.r.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if got := f.do("DELETE", base+"/api/v1/cookbooks/widget", "admin"); got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
}

func TestAccInvalidCookbookRequests(t *testing.T) {
	f := newFixture(t, repo.Local)
	for _, p := range []string{"/api/v1/cookbooks?start=-1", "/api/v1/cookbooks?items=0", "/api/v1/cookbooks?items=1001", "/api/v1/cookbooks?items=a", "/api/v1/cookbooks?start=a", "/api/v1/cookbooks?x=1", "/api/v1/cookbooks?items=1&items=2", "/api/v1/search?q=%xx", "/api/v1/cookbooks/w.bad", "/api/v1/cookbooks/widget?x=1", "/api/v1/cookbooks/widget/versions/bad"} {
		t.Run(p, func(t *testing.T) {
			if got := f.do("GET", base+p, "admin"); got.Code != 400 {
				t.Fatal(got.Code, got.Body.String())
			}
		})
	}
	for _, p := range []string{"/api/v1/cookbooks/widget", "/api/v1/cookbooks/widget/versions/1_0_0", "/api/v1/cookbooks/widget/versions/1_0_0/download"} {
		if got := f.do("GET", base+p, "admin"); got.Code != 404 {
			t.Fatal(got.Code)
		}
	}
	for _, tc := range []struct {
		name string
		data []byte
		user string
		code int
	}{{"widget", []byte("bad"), "admin", 400}, {"other", archive(t, "widget", "1.0.0"), "admin", 400}, {"widget", archive(t, "widget", "1.0.0"), "denied", 403}, {"widget", archive(t, "widget", "1.0.0"), "", 401}, {"bad.name", nil, "admin", 400}, {"widget?x=1", nil, "admin", 400}, {"widget?category=x&category=y", nil, "admin", 400}} {
		if got := f.upload(tc.name, tc.data, tc.user); got.Code != tc.code {
			t.Fatalf("%s %d %s", tc.name, got.Code, got.Body.String())
		}
	}
	for _, class := range []repo.Class{repo.Remote, repo.Virtual} {
		f := newFixtureWith(
			t,
			class,
			func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = metadataSource{} },
		)
		if got := f.upload("widget", nil, "admin"); got.Code != 405 {
			t.Fatal(got.Code)
		}
		if got := f.do("DELETE", base+"/api/v1/cookbooks/widget/1.0.0", "admin"); got.Code != 405 {
			t.Fatal(got.Code)
		}
	}
}

func upstream(t *testing.T, data []byte) metadataSource {
	t.Helper()
	sum := sha256.Sum256(data)
	return metadataSource{
		check:      func(context.Context, string) error { return nil },
		archive:    data,
		archiveSet: true,
		bodyAt: func(resource source.Resource) string {
			switch resource.Path {
			case "api/v1/cookbooks":
				return `{"total":1,"start":0,"items":[{"cookbook_name":"widget"}]}`
			case "api/v1/cookbooks/widget":
				return `{"name":"widget","versions":["https://example.com/chef/api/v1/cookbooks/widget/versions/1_0_0"],"latest_version":"https://example.com/chef/api/v1/cookbooks/widget/versions/1_0_0"}`
			default:
				return fmt.Sprintf(
					`{"version":"1.0.0","file":"https://example.com/chef/blobs/widget.tgz","cookbook":"https://example.com/chef/api/v1/cookbooks/widget","tarball_file_size":%d,"sha256":%q,"dependencies":{"helper":">= 1.0.0"},"platforms":{"ubuntu":">= 20.04"}}`,
					len(data),
					hex.EncodeToString(sum[:]),
				)
			}
		},
	}
}

func TestAccRemoteCookbookValidationAndAuthority(t *testing.T) {
	data := archive(t, "widget", "1.0.0")
	for _, tc := range []struct {
		name   string
		change func(*metadataSource)
		code   int
	}{
		{"valid", func(*metadataSource) {}, 200},
		{"unavailable", func(s *metadataSource) { s.readError = source.ErrUnavailable }, 502},
		{"malformed metadata", func(s *metadataSource) { s.bodyAt = func(source.Resource) string { return "{" } }, 502},
		{"absent authority", func(s *metadataSource) { s.missingRevalidate = true }, 502},
		{"absent check", func(s *metadataSource) { s.check = nil }, 502},
		{"revoked", func(s *metadataSource) { s.revalidateError = source.ErrDenied }, 403},
		{"missing body", func(s *metadataSource) { s.missingStream = true }, 502},
		{"missing cached blob", func(s *metadataSource) { s.badCachedItem = true }, 500},
		{"metadata off-origin", func(s *metadataSource) { s.metadataBase = "https://evil.invalid/feed" }, 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := upstream(t, data)
			tc.change(&s)
			f := newFixtureWith(
				t,
				repo.Remote,
				func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = s },
			)
			got := f.do("GET", base+"/api/v1/cookbooks/widget", "admin")
			if got.Code != tc.code {
				t.Fatal(got.Code, got.Body.String())
			}
		})
	}
	f := newFixtureWith(
		t,
		repo.Remote,
		func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = upstream(t, data) },
	)
	for _, p := range []string{"/api/v1/cookbooks", "/api/v1/cookbooks/widget/versions/latest", "/api/v1/cookbooks/widget/versions/1_0_0/download"} {
		got := f.do("GET", base+p, "admin")
		if got.Code != 200 {
			t.Fatal(p, got.Code, got.Body.String())
		}
	}
	for _, tc := range []struct {
		name   string
		change func(*metadataSource)
		code   int
	}{{"bad archive", func(s *metadataSource) { s.archive = []byte("bad") }, 502}, {"wrong manifest", func(s *metadataSource) { s.archive = archive(t, "other", "1.0.0") }, 502}, {"archive authority missing", func(s *metadataSource) { s.missingArchiveRV = true }, 502}, {"cross origin redirect", func(s *metadataSource) { s.redirect, _ = url.Parse("https://objects.example.com/data") }, 200}, {"credential URL", func(s *metadataSource) { s.redirect, _ = url.Parse("https://secret@objects.example.com/data") }, 403}} {
		t.Run(tc.name, func(t *testing.T) {
			s := upstream(t, data)
			tc.change(&s)
			f := newFixtureWith(
				t,
				repo.Remote,
				func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = s },
			)
			got := f.do("GET", base+"/api/v1/cookbooks/widget/versions/1_0_0/download", "admin")
			if got.Code != tc.code {
				t.Fatal(got.Code, got.Body.String())
			}
		})
	}
}

func TestAccVirtualPriorityAndPermissionFiltering(t *testing.T) {
	f := newFixture(t, repo.Virtual)
	for _, key := range []string{"first", "second"} {
		c := *repo.Defaults(&repo.Config{Key: key, Class: repo.Local, PackageType: repo.Chef})
		if err := f.db.Repos().Create(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		f.deploy(c, "widget", "1.0.0", archive(t, "widget", "1.0.0"))
	}
	f.c.Repositories = []string{"first", "second"}
	if err := f.db.Repos().Update(t.Context(), f.c); err != nil {
		t.Fatal(err)
	}
	got := f.do("GET", base+"/api/v1/cookbooks/widget/versions/latest", "admin")
	if got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	if strings.Contains(got.Body.String(), "/chef/first") {
		t.Fatal("member URL leaked")
	}
	got = f.do("GET", base+"/api/v1/cookbooks/widget/versions/latest/download", "admin")
	if got.Code != 200 {
		t.Fatal(got.Code)
	}
	got = f.do("GET", base+"/api/v1/cookbooks", "admin")
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"total":1`) {
		t.Fatal(got.Code, got.Body.String())
	}
}

type faultStore struct {
	store.Store
	find, versions, items, repos, list, huge bool
}

func (f *faultStore) Packages() store.PackageStore { return faultPackages{f.Store.Packages(), f} }
func (f *faultStore) Items() store.ItemStore       { return faultItems{f.Store.Items(), f} }
func (f *faultStore) Repos() store.RepoStore       { return faultRepos{f.Store.Repos(), f} }

type faultPackages struct {
	store.PackageStore
	f *faultStore
}

func (f faultPackages) Find(ctx context.Context, key, name string) (types.Package, error) {
	if f.f.find {
		return types.Package{}, context.Canceled
	}
	return f.PackageStore.Find(ctx, key, name)
}

func (f faultPackages) Versions(ctx context.Context, id int64) ([]types.PackageVersion, error) {
	if f.f.versions {
		return nil, context.Canceled
	}
	if f.f.huge {
		return make([]types.PackageVersion, 4097), nil
	}
	return f.PackageStore.Versions(ctx, id)
}

func (f faultPackages) List(
	ctx context.Context,
	key string,
	p store.Page,
) ([]types.Package, string, error) {
	if f.f.list {
		return nil, "", context.Canceled
	}
	return f.PackageStore.List(ctx, key, p)
}

type faultItems struct {
	store.ItemStore
	f *faultStore
}

func (f faultItems) Get(
	ctx context.Context,
	key string,
	cached bool,
	path string,
) (types.Item, error) {
	if f.f.items {
		return types.Item{}, context.Canceled
	}
	return f.ItemStore.Get(ctx, key, cached, path)
}

type faultRepos struct {
	store.RepoStore
	f *faultStore
}

func (f faultRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	if f.f.repos && key == "member" {
		return repo.Config{}, context.Canceled
	}
	return f.RepoStore.Get(ctx, key)
}

func TestAccBrokenStoredIndexNeverAdvertisesCookbook(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tune   func(*faultStore)
		path   string
		status int
	}{
		{"find", func(f *faultStore) { f.find = true }, "/api/v1/cookbooks/widget", 500},
		{"versions", func(f *faultStore) { f.versions = true }, "/api/v1/cookbooks/widget/versions/latest", 500},
		{"items", func(f *faultStore) { f.items = true }, "/api/v1/cookbooks/widget", 502},
		{"version limit", func(f *faultStore) { f.huge = true }, "/api/v1/cookbooks/widget", 503},
		{"catalog", func(f *faultStore) { f.list = true }, "/api/v1/cookbooks", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var faults *faultStore
			f := newFixtureStore(
				t,
				repo.Local,
				nil,
				func(s store.Store) store.Store { faults = &faultStore{Store: s}; return faults },
			)
			f.deploy(f.c, "widget", "1.0.0", archive(t, "widget", "1.0.0"))
			tc.tune(faults)
			got := f.do("GET", base+tc.path, "admin")
			if got.Code != tc.status {
				t.Fatal(got.Code, got.Body.String())
			}
		})
	}
	for _, raw := range []string{`{}`, `{"name":"widget","version":"1.0.0","path":"widget/widget-1.0.0.tar.gz","sha256":"bad"}`} {
		f := newFixture(t, repo.Local)
		f.deploy(f.c, "widget", "1.0.0", archive(t, "widget", "1.0.0"))
		p, _ := f.db.Packages().Find(t.Context(), f.c.Key, "widget")
		v, _ := f.db.Packages().Version(t.Context(), p.ID, "1.0.0")
		v.Metadata = []byte(raw)
		if _, err := f.db.Packages().PutVersion(t.Context(), v); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"/api/v1/cookbooks/widget", "/api/v1/cookbooks"} {
			got := f.do("GET", base+path, "admin")
			if got.Code != 502 {
				t.Fatal(got.Code, got.Body.String())
			}
		}
	}
}

func grant(t *testing.T, f *fixture, targets map[string]types.Target) {
	t.Helper()
	if err := f.db.Permissions().
		Create(t.Context(), types.Permission{Name: "allowed", Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {Actions: types.Grants{Users: map[string]types.Actions{"reader": types.ActionRead}}, Targets: targets}}}); err != nil {
		t.Fatal(err)
	}
}

func TestAccHostedCatalogAndLatestAreFilteredBeforeSelection(t *testing.T) {
	f := newFixture(t, repo.Local)
	for _, v := range []string{"1.0.0", "2.0.0", "10.0.0"} {
		f.deploy(f.c, "widget", v, archive(t, "widget", v))
	}
	grant(
		t,
		f,
		map[string]types.Target{
			f.c.Key: {Include: []string{"widget/**"}, Exclude: []string{"**/*-10.0.0.tar.gz"}},
		},
	)
	got := f.do("GET", base+"/api/v1/cookbooks/widget/versions/latest", "reader")
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"version":"2.0.0"`) {
		t.Fatal(got.Code, got.Body.String())
	}
	if got = f.do(
		"GET",
		base+"/api/v1/cookbooks/widget/versions/10_0_0",
		"reader",
	); got.Code != 403 {
		t.Fatal(got.Code)
	}
	for _, path := range []string{"/api/v1/cookbooks?start=1&items=1", "/api/v1/search?q=absent"} {
		got = f.do("GET", base+path, "reader")
		if got.Code != 200 || !strings.Contains(got.Body.String(), `"items":[]`) {
			t.Fatal(got.Code, got.Body.String())
		}
	}
	f.c.ExcludesPattern = new("**/*-2.0.0.tar.gz")
	if err := f.db.Repos().Update(t.Context(), f.c); err != nil {
		t.Fatal(err)
	}
	got = f.do("GET", base+"/api/v1/cookbooks/widget/versions/latest", "reader")
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"version":"1.0.0"`) {
		t.Fatal(got.Code, got.Body.String())
	}
	for _, path := range []string{"/api/v1/cookbooks/widget/versions/latest?bad=1", "/api/v1/cookbooks/widget/bad", "/api/v1/cookbooks/widget?bad=1"} {
		got = f.do("DELETE", base+path, "admin")
		if got.Code != 400 {
			t.Fatal(path, got.Code)
		}
	}
}

func TestAccVirtualMembersRetainPriorityAndCurrentPermissions(t *testing.T) {
	var faults *faultStore
	f := newFixtureStore(
		t,
		repo.Virtual,
		nil,
		func(s store.Store) store.Store { faults = &faultStore{Store: s}; return faults },
	)
	for _, key := range []string{"first", "member"} {
		c := *repo.Defaults(&repo.Config{Key: key, Class: repo.Local, PackageType: repo.Chef})
		if err := f.db.Repos().Create(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		f.deploy(c, "widget", "1.0.0", archive(t, "widget", "1.0.0"))
		if key == "member" {
			f.deploy(c, "widget", "2.0.0", archive(t, "widget", "2.0.0"))
		}
	}
	f.c.Repositories = []string{"missing", "first", "member"}
	if err := f.db.Repos().Update(t.Context(), f.c); err != nil {
		t.Fatal(err)
	}
	grant(
		t,
		f,
		map[string]types.Target{
			f.c.Key: {Include: []string{"**"}},
			"first": {Include: []string{"**"}},
		},
	)
	for _, path := range []string{"/api/v1/cookbooks/widget", "/api/v1/cookbooks"} {
		got := f.do("GET", base+path, "reader")
		if got.Code != 200 || strings.Contains(got.Body.String(), "2_0_0") {
			t.Fatal(got.Code, got.Body.String())
		}
	}
	f.c.ExcludesPattern = new("**/*-1.0.0.tar.gz")
	if err := f.db.Repos().Update(t.Context(), f.c); err != nil {
		t.Fatal(err)
	}
	if got := f.do("GET", base+"/api/v1/cookbooks/widget", "reader"); got.Code != 404 {
		t.Fatal(got.Code)
	}
	if got := f.do(
		"GET",
		base+"/api/v1/cookbooks",
		"reader",
	); got.Code != 200 ||
		!strings.Contains(got.Body.String(), `"total":0`) {
		t.Fatal(got.Code, got.Body.String())
	}
	faults.repos = true
	for _, path := range []string{"/api/v1/cookbooks/widget", "/api/v1/cookbooks"} {
		if got := f.do("GET", base+path, "admin"); got.Code != 500 {
			t.Fatal(got.Code, got.Body.String())
		}
	}
	faults.repos = false
	f.c.Repositories = []string{f.c.Key}
	if err := f.db.Repos().Update(t.Context(), f.c); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/cookbooks/widget", "/api/v1/cookbooks"} {
		if got := f.do("GET", base+path, "admin"); got.Code != 400 {
			t.Fatal(got.Code, got.Body.String())
		}
	}
}

func TestAccRemotePoliciesFilterBeforeVersionMetadata(t *testing.T) {
	data := archive(t, "widget", "1.0.0")
	for _, tc := range []struct {
		name   string
		check  func(context.Context, string) error
		status int
	}{
		{"read denied", func(context.Context, string) error { return source.ErrDenied }, 404},
		{"read unavailable", func(context.Context, string) error { return source.ErrUnavailable }, 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := upstream(t, data)
			s.check = tc.check
			f := newFixtureWith(
				t,
				repo.Remote,
				func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = s },
			)
			got := f.do("GET", base+"/api/v1/cookbooks/widget", "admin")
			if got.Code != tc.status {
				t.Fatal(got.Code, got.Body.String())
			}
		})
	}
	s := upstream(t, data)
	calls := 0
	s.revalidateCheck = func(context.Context) error {
		calls++
		if calls > 2 {
			return source.ErrDenied
		}
		return nil
	}
	f := newFixtureWith(t, repo.Remote, func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = s })
	for _, path := range []string{"/api/v1/cookbooks/widget/versions/latest", "/api/v1/cookbooks"} {
		calls = 0
		got := f.do("GET", base+path, "admin")
		if got.Code != 403 {
			t.Fatal(got.Code, got.Body.String())
		}
	}
	s = upstream(t, data)
	reads := s.bodyAt
	s.bodyAt = func(r source.Resource) string {
		if r.Path == "api/v1/cookbooks/widget" {
			return reads(r)
		}
		t.Fatal("excluded archive triggered a version request")
		return ""
	}
	f = newFixtureWith(t, repo.Remote, func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = s })
	f.c.ExcludesPattern = new("**/*.tar.gz")
	if err := f.db.Repos().Update(t.Context(), f.c); err != nil {
		t.Fatal(err)
	}
	if got := f.do("GET", base+"/api/v1/cookbooks/widget", "admin"); got.Code != 404 {
		t.Fatal(got.Code)
	}
}

func TestAccRemoteCatalogRefusesInvalidPagination(t *testing.T) {
	for _, body := range []string{`{"total":1,"start":1,"items":[]}`, `{"total":4097,"start":0,"items":[]}`, `{"total":2,"start":0,"items":[{"cookbook_name":"widget"}]}`, `{"total":1,"start":0,"items":[{"cookbook_name":"bad.name"}]}`, `{"total":2,"start":0,"items":[{"cookbook_name":"widget"},{"cookbook_name":"widget"}]}`} {
		s := upstream(t, archive(t, "widget", "1.0.0"))
		s.bodyAt = func(source.Resource) string { return body }
		f := newFixtureWith(
			t,
			repo.Remote,
			func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = s },
		)
		got := f.do("GET", base+"/api/v1/cookbooks", "admin")
		if got.Code != 502 {
			t.Fatal(got.Code, got.Body.String())
		}
	}
}

func TestAccRemoteCookbookAuthorityRevokedDuringDelivery(t *testing.T) {
	data := archive(t, "widget", "1.0.0")
	// Metadata lookup, response construction and final stream delivery each
	// recheck source authority; revocation at any one must prevent a success.
	for _, after := range []int{2, 3, 4, 5, 6} {
		t.Run(fmt.Sprintf("check-%d", after), func(t *testing.T) {
			calls := 0
			s := upstream(t, data)
			s.check = func(context.Context, string) error {
				calls++
				if calls >= after {
					return source.ErrDenied
				}
				return nil
			}
			f := newFixtureWith(
				t,
				repo.Remote,
				func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = s },
			)
			got := f.do("GET", base+"/api/v1/cookbooks/widget/versions/1_0_0/download", "admin")
			if got.Code != 403 {
				t.Fatalf("revoked at %d calls=%d: %d %s", after, calls, got.Code, got.Body.String())
			}
		})
	}
	for _, after := range []int{4, 5} {
		t.Run(fmt.Sprintf("revision-%d", after), func(t *testing.T) {
			calls := 0
			s := upstream(t, data)
			s.revalidateCheck = func(context.Context) error {
				calls++
				if calls >= after {
					return source.ErrDenied
				}
				return nil
			}
			f := newFixtureWith(
				t,
				repo.Remote,
				func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = s },
			)
			got := f.do("GET", base+"/api/v1/cookbooks/widget/versions/1_0_0/download", "admin")
			if got.Code != 403 {
				t.Fatalf("revision revoked at %d calls=%d: %d", after, calls, got.Code)
			}
		})
	}
}

func TestAccRemoteCachedAndUnvalidatedMetadataStillUsesBoundedParser(t *testing.T) {
	for _, raw := range []string{strings.Repeat(" ", 4<<20+1), `{"name":"other","versions":[]}`} {
		s := upstream(t, archive(t, "widget", "1.0.0"))
		s.skipMetadataValidation = true
		s.bodyAt = func(source.Resource) string { return raw }
		f := newFixtureWith(
			t,
			repo.Remote,
			func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = s },
		)
		got := f.do("GET", base+"/api/v1/cookbooks/widget", "admin")
		if got.Code != 502 {
			t.Fatal(got.Code)
		}
	}
	s := upstream(t, []byte("corrupt gzip bytes"))
	f := newFixtureWith(t, repo.Remote, func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = s })
	if got := f.do(
		"GET",
		base+"/api/v1/cookbooks/widget/versions/1_0_0/download",
		"admin",
	); got.Code != 502 {
		t.Fatal(got.Code)
	}
	f.c.URL = "https://user:password@example.com/chef"
	if err := f.db.Repos().Update(t.Context(), f.c); err != nil {
		t.Fatal(err)
	}
	if got := f.do("GET", base+"/api/v1/cookbooks/widget", "admin"); got.Code != 502 {
		t.Fatal(got.Code)
	}
}

func TestAccVirtualRemoteMemberPreservesSourceAuthority(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code int
	}{{"present", nil, 200}, {"missing", store.ErrNotFound, 404}, {"unavailable", source.ErrUnavailable, 502}} {
		t.Run(tc.name, func(t *testing.T) {
			s := upstream(t, archive(t, "widget", "1.0.0"))
			s.readError = tc.err
			f := newFixtureWith(
				t,
				repo.Virtual,
				func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = s },
			)
			c := *repo.Defaults(&repo.Config{Key: "remote", Class: repo.Remote, PackageType: repo.Chef, URL: "https://example.com/chef"})
			if err := f.db.Repos().Create(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			f.c.Repositories = []string{c.Key}
			if err := f.db.Repos().Update(t.Context(), f.c); err != nil {
				t.Fatal(err)
			}
			got := f.do("GET", base+"/api/v1/cookbooks/widget/versions/1_0_0/download", "admin")
			if got.Code != tc.code {
				t.Fatal(got.Code, got.Body.String())
			}
			if tc.err != nil {
				return
			}
			got = f.do("GET", base+"/api/v1/cookbooks", "admin")
			if got.Code != 200 {
				t.Fatal(got.Code, got.Body.String())
			}
		})
	}
}

func TestAccMalformedMultipartPublishCannotCreateVersion(t *testing.T) {
	f := newFixture(t, repo.Local)
	for _, contentType := range []string{"application/json", "multipart/form-data; boundary=missing"} {
		r := httptest.NewRequest("POST", base+"/api/v1/cookbooks", strings.NewReader("invalid"))
		r.Header.Set("X-User", "admin")
		r.Header.Set("Content-Type", contentType)
		w := httptest.NewRecorder()
		f.r.ServeHTTP(w, r)
		if w.Code != 400 && w.Code != 415 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if got := f.do(
		"GET",
		base+"/api/v1/cookbooks",
		"admin",
	); got.Code != 200 ||
		!strings.Contains(got.Body.String(), `"total":0`) {
		t.Fatal(got.Code, got.Body.String())
	}
}
