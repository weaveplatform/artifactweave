package puppet_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/server/puppet"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const base = "/artifactory/api/puppet/puppet-local"

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
	c := *repo.Defaults(&repo.Config{Key: "puppet-local", Class: class, PackageType: repo.Puppet})
	if class == repo.Remote {
		c.URL = "https://example.com/puppet/"
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
	p := puppet.Protocol(puppet.Options{Blobs: blobs})
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
		body = `{"name":"visible_pub","versions":[{"version":"1.0.0","archive_url":"https://example.com/puppet/packages/visible_pub/versions/1.0.0.tar.gz","pubspec":{"name":"visible_pub","version":"1.0.0"}}]}`
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
			BaseURL: "https://example.com/puppet/v3/releases",
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

func fixtureArchive(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../../../test/acceptance/fixtures/puppet/acme-widget-1.0.0.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (f *fixture) deploy(c repo.Config, body []byte) types.Item {
	f.t.Helper()
	it, err := f.items.Deploy(
		f.t.Context(),
		item.Deploy{
			Target: item.Target{Repo: c, Path: "acme-widget/acme-widget-1.0.0.tar.gz"},
			Body:   bytes.NewReader(body),
			By:     "admin",
		},
	)
	if err != nil {
		f.t.Fatal(err)
	}
	return it
}

func upstreamBody(t *testing.T, data []byte) string {
	t.Helper()
	sum := sha256.Sum256(data)
	return fmt.Sprintf(
		`{"results":[{"version":"1.0.0","file_uri":"/puppet/v3/files/acme-widget-1.0.0.tar.gz","file_sha256":%q,"file_size":%d,"metadata":{"name":"acme-widget","version":"1.0.0","author":"acme","summary":"Controlled Puppet acceptance module","license":"Apache-2.0","source":"https://example.invalid/fixture","dependencies":[{"name":"acme/helper","version_requirement":">= 1.0.0 < 2.0.0"}]}}],"pagination":{"offset":0,"total":1,"next":null}}`,
		hex.EncodeToString(sum[:]),
		len(data),
	)
}

func TestAccHostedModuleRoutes(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) {
			f := newFixture(t, class)
			data := fixtureArchive(t)
			f.deploy(f.c, data)
			for _, path := range []string{"/v3/releases?module=acme-widget", "/v3/modules/acme-widget", "/v3/releases/acme-widget-1.0.0", "/v3/modules?query=acme-widget", "/v3/files/acme-widget-1.0.0.tar.gz"} {
				r := f.do("GET", base+path, "admin")
				if r.Code != 200 {
					t.Fatalf("%s: %d %s", path, r.Code, r.Body.String())
				}
				if strings.Contains(path, "/files/") {
					if !bytes.Equal(r.Body.Bytes(), data) {
						t.Fatal("archive bytes")
					}
				} else if !strings.Contains(r.Body.String(), "acme/helper") {
					t.Fatal("missing dependencies")
				}
				if r := f.do("HEAD", base+path, "admin"); r.Code != 200 || r.Body.Len() != 0 {
					t.Fatalf("HEAD: %d %s", r.Code, r.Body.String())
				}
				if r := f.do("GET", base+path, "denied"); r.Code != 403 {
					t.Fatalf("denied: %d %s", r.Code, r.Body.String())
				}
			}
			if err := f.items.Reindex(t.Context(), f.c); err != nil {
				t.Fatal(err)
			}
			if r := f.do("GET", base+"/v3/modules/acme-widget", "admin"); r.Code != 200 {
				t.Fatal(r.Code)
			}
		})
	}
}

func TestAccInvalidRoutesAndQueries(t *testing.T) {
	f := newFixture(t, repo.Local)
	for _, path := range []string{"/v3/releases?module=%zz", "/v3/releases?module=" + strings.Repeat("a", 4097), "/v3/releases", "/v3/releases?module=../evil", "/v3/releases?module=acme-widget&limit=0", "/v3/releases?module=acme-widget&offset=-1", "/v3/releases?module=acme-widget&sort_by=bad", "/v3/releases?module=acme-widget&x=bad", "/v3/releases?module=acme-widget&module=acme-widget", "/v3/releases/bad", "/v3/modules/bad", "/v3/modules/acme-widget?bad=1", "/v3/modules?query=bad", "/v3/files/acme-widget-1.0.0.zip", "/v3/files/acme-widget-1.0.0.tar.gz?x=1"} {
		if r := f.do("GET", base+path, "admin"); r.Code != 400 {
			t.Errorf("%s: %d", path, r.Code)
		}
	}
	for _, path := range []string{"/v3/releases/acme-widget-1.0.0", "/v3/modules/acme-widget", "/v3/files/acme-widget-1.0.0.tar.gz"} {
		if r := f.do("GET", base+path, "admin"); r.Code != 404 {
			t.Errorf("missing %s: %d", path, r.Code)
		}
	}
	if r := f.do(
		"GET",
		base+"/v3/releases?module=acme-widget",
		"admin",
	); r.Code != 200 ||
		!strings.Contains(r.Body.String(), `"results":[]`) {
		t.Fatalf("empty releases: %d %s", r.Code, r.Body.String())
	}
	if r := f.do("GET", base+"/v3/modules?query=acme-widget", "admin"); r.Code != 200 {
		t.Fatal(r.Code)
	}
}

func TestAccRemoteAuthorityIntegrityAndFailures(t *testing.T) {
	data := fixtureArchive(t)
	good := metadataSource{
		body:       upstreamBody(t, data),
		archive:    data,
		archiveSet: true,
		check:      func(context.Context, string) error { return nil },
	}
	for _, tc := range []struct {
		name   string
		tune   func(*metadataSource)
		status int
	}{
		{"success", func(*metadataSource) {}, 200},
		{"no check", func(s *metadataSource) { s.check = nil }, 502},
		{"no revalidation", func(s *metadataSource) { s.missingRevalidate = true }, 502},
		{"stale", func(s *metadataSource) { s.revalidateError = source.ErrSuperseded }, 503},
		{"denied", func(s *metadataSource) { s.check = func(context.Context, string) error { return source.ErrDenied } }, 404},
		{"check failure", func(s *metadataSource) {
			s.check = func(context.Context, string) error { return source.ErrUnavailable }
		}, 502},
		{"upstream 503", func(s *metadataSource) { s.readError = &source.HTTPError{Status: 503} }, 503},
		{"bad metadata", func(s *metadataSource) { s.body = `{}` }, 502},
		{"no stream", func(s *metadataSource) { s.missingStream = true }, 502},
		{"no cached blob", func(s *metadataSource) { s.badCachedItem = true }, 500},
		{"no archive authority", func(s *metadataSource) { s.missingArchiveRV = true }, 502},
		{"corrupt archive", func(s *metadataSource) { s.archive = bytes.Repeat([]byte("x"), len(data)) }, 502},
		{"truncated archive", func(s *metadataSource) { s.archive = data[:10] }, 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := good
			tc.tune(&src)
			f := newFixtureWith(
				t,
				repo.Remote,
				func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = src },
			)
			r := f.do("GET", base+"/v3/files/acme-widget-1.0.0.tar.gz", "admin")
			if r.Code != tc.status {
				t.Fatalf("got %d: %s", r.Code, r.Body.String())
			}
			if tc.status == 200 && !bytes.Equal(r.Body.Bytes(), data) {
				t.Fatal("archive differs")
			}
		})
	}
}

func TestAccVirtualOrderedVersionsAndMemberPermissions(t *testing.T) {
	f := newFixture(t, repo.Local)
	data := fixtureArchive(t)
	f.deploy(f.c, data)
	other := *repo.Defaults(&repo.Config{Key: "second", Class: repo.Local, PackageType: repo.Puppet})
	if err := f.db.Repos().Create(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	f.deploy(other, data)
	virtual := *repo.Defaults(&repo.Config{Key: "virtual", Class: repo.Virtual, PackageType: repo.Puppet, Repositories: []string{f.c.Key, other.Key}})
	if err := f.db.Repos().Create(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v3/releases?module=acme-widget", "/v3/files/acme-widget-1.0.0.tar.gz"} {
		r := f.do("GET", "/artifactory/api/puppet/virtual"+path, "admin")
		if r.Code != 200 {
			t.Fatalf("virtual: %d %s", r.Code, r.Body.String())
		}
	}
	permission := types.Permission{
		Name: "virtual-only",
		Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{"viewer": types.ActionRead}},
				Targets: map[string]types.Target{"virtual": {}},
			},
		},
	}
	if err := f.db.Permissions().Create(t.Context(), permission); err != nil {
		t.Fatal(err)
	}
	r := f.do("GET", "/artifactory/api/puppet/virtual/v3/releases?module=acme-widget", "viewer")
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"results":[]`) {
		t.Fatalf("member disclosure %d %s", r.Code, r.Body.String())
	}
}

func variantArchive(t *testing.T, version, summary string) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	m, _ := json.Marshal(
		map[string]any{
			"name":         "acme-widget",
			"version":      version,
			"summary":      summary,
			"dependencies": []any{},
		},
	)
	if err := tw.WriteHeader(
		&tar.Header{
			Name: "acme-widget-" + version + "/metadata.json",
			Mode: 0o644,
			Size: int64(len(m)),
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(m); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestAccVirtualPriorityTracksMemberChanges(t *testing.T) {
	f := newFixture(t, repo.Local)
	first := variantArchive(t, "1.0.0", "first")
	second := variantArchive(t, "1.0.0", "second")
	f.deploy(f.c, first)
	other := *repo.Defaults(&repo.Config{Key: "second", Class: repo.Local, PackageType: repo.Puppet})
	if err := f.db.Repos().Create(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	f.deploy(other, second)
	v := *repo.Defaults(&repo.Config{Key: "virtual", Class: repo.Virtual, PackageType: repo.Puppet, Repositories: []string{"missing", f.c.Key, other.Key}})
	if err := f.db.Repos().Create(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	path := "/artifactory/api/puppet/virtual/v3/files/acme-widget-1.0.0.tar.gz"
	if r := f.do("GET", path, "admin"); r.Code != 200 || !bytes.Equal(r.Body.Bytes(), first) {
		t.Fatalf("first winner: %d", r.Code)
	}
	v.Repositories = []string{other.Key, f.c.Key}
	if err := f.db.Repos().Update(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	if r := f.do("GET", path, "admin"); r.Code != 200 || !bytes.Equal(r.Body.Bytes(), second) {
		t.Fatalf("reordered winner: %d", r.Code)
	}
	other.PackageType = repo.Generic
	if err := f.db.Repos().Update(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	if r := f.do("GET", path, "admin"); r.Code != 400 {
		t.Fatalf("changed member type: %d", r.Code)
	}
}

func TestAccMetadataPaginationAndLateAuthority(t *testing.T) {
	f := newFixture(t, repo.Local)
	for _, v := range []string{"1.0.0", "2.0.0"} {
		_, err := f.items.Deploy(
			t.Context(),
			item.Deploy{
				Target: item.Target{Repo: f.c, Path: "acme-widget/acme-widget-" + v + ".tar.gz"},
				Body:   bytes.NewReader(variantArchive(t, v, v)),
				By:     "admin",
			},
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := f.do("GET", base+"/v3/releases?module=acme-widget&limit=1", "admin")
	if r.Code != 200 || !strings.Contains(r.Body.String(), "offset=1") ||
		!strings.Contains(r.Body.String(), `"version":"2.0.0"`) {
		t.Fatalf("page: %d %s", r.Code, r.Body.String())
	}
	r = f.do("GET", base+"/v3/releases?module=acme-widget&offset=1&limit=1", "admin")
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"next":null`) {
		t.Fatal(r.Body.String())
	}
	for _, route := range []string{"/v3/releases?module=acme-widget", "/v3/releases/acme-widget-1.0.0", "/v3/modules/acme-widget", "/v3/modules?query=acme-widget", "/v3/files/acme-widget-1.0.0.tar.gz"} {
		t.Run(route, func(t *testing.T) {
			calls := 0
			src := metadataSource{
				body:  upstreamBody(t, fixtureArchive(t)),
				check: func(context.Context, string) error { return nil },
				revalidateCheck: func(context.Context) error {
					calls++
					if calls >= 2 {
						return source.ErrSuperseded
					}
					return nil
				},
			}
			f := newFixtureWith(
				t,
				repo.Remote,
				func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = src },
			)
			if r := f.do("GET", base+route, "admin"); r.Code != 503 {
				t.Fatalf("stale metadata: %d %s", r.Code, r.Body.String())
			}
		})
	}
	for _, route := range []string{"/v3/releases/acme-widget-1.0.0", "/v3/modules/acme-widget", "/v3/modules?query=acme-widget"} {
		src := metadataSource{readError: source.ErrUnavailable}
		f := newFixtureWith(
			t,
			repo.Remote,
			func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = src },
		)
		if r := f.do("GET", base+route, "admin"); r.Code != 502 {
			t.Fatal(r.Code)
		}
	}
}

type faultStore struct {
	store.Store
	find     bool
	versions bool
	items    bool
	repos    bool
	huge     bool
}

func (f *faultStore) Packages() store.PackageStore {
	return faultPackages{PackageStore: f.Store.Packages(), f: f}
}
func (f *faultStore) Items() store.ItemStore { return faultItems{ItemStore: f.Store.Items(), f: f} }
func (f *faultStore) Repos() store.RepoStore { return faultRepos{RepoStore: f.Store.Repos(), f: f} }

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

func TestAccStoreFailuresDoNotAdvertiseModules(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tune   func(*faultStore)
		status int
	}{
		{"find", func(f *faultStore) { f.find = true }, 500}, {"versions", func(f *faultStore) { f.versions = true }, 500}, {"items", func(f *faultStore) { f.items = true }, 502}, {"oversized index", func(f *faultStore) { f.huge = true }, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var faults *faultStore
			f := newFixtureStore(
				t,
				repo.Local,
				nil,
				func(s store.Store) store.Store { faults = &faultStore{Store: s}; return faults },
			)
			f.deploy(f.c, fixtureArchive(t))
			tc.tune(faults)
			r := f.do("GET", base+"/v3/releases?module=acme-widget", "admin")
			if r.Code != tc.status {
				t.Fatalf("%d %s", r.Code, r.Body.String())
			}
		})
	}
	for _, bad := range []string{`{}`, `{"name":"acme-widget","version":"1.0.0","path":"acme-widget/acme-widget-1.0.0.tar.gz","sha256":"bad"}`} {
		f := newFixture(t, repo.Local)
		f.deploy(f.c, fixtureArchive(t))
		pkg, err := f.db.Packages().Find(t.Context(), f.c.Key, "acme-widget")
		if err != nil {
			t.Fatal(err)
		}
		v, err := f.db.Packages().Version(t.Context(), pkg.ID, "1.0.0")
		if err != nil {
			t.Fatal(err)
		}
		v.Metadata = []byte(bad)
		if _, err = f.db.Packages().PutVersion(t.Context(), v); err != nil {
			t.Fatal(err)
		}
		if r := f.do("GET", base+"/v3/releases?module=acme-widget", "admin"); r.Code != 502 {
			t.Fatal(r.Code)
		}
	}
}

func TestAccLateReadRevocationAndArchiveManifestMismatch(t *testing.T) {
	data := fixtureArchive(t)
	for _, route := range []string{"/v3/releases?module=acme-widget", "/v3/files/acme-widget-1.0.0.tar.gz"} {
		calls := 0
		src := metadataSource{
			body:       upstreamBody(t, data),
			archive:    data,
			archiveSet: true,
			check: func(context.Context, string) error {
				calls++
				if calls >= 2 {
					return source.ErrDenied
				}
				return nil
			},
		}
		f := newFixtureWith(
			t,
			repo.Remote,
			func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = src },
		)
		if r := f.do("GET", base+route, "admin"); r.Code != 403 {
			t.Fatalf("late read denial %d", r.Code)
		}
	}
	for _, data := range [][]byte{[]byte("invalid gzip"), variantArchive(t, "1.0.0", "different manifest")} {
		src := metadataSource{
			body:       upstreamBody(t, data),
			archive:    data,
			archiveSet: true,
			check:      func(context.Context, string) error { return nil },
		}
		f := newFixtureWith(
			t,
			repo.Remote,
			func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = src },
		)
		if r := f.do("GET", base+"/v3/files/acme-widget-1.0.0.tar.gz", "admin"); r.Code != 502 {
			t.Fatalf("archive metadata mismatch %d", r.Code)
		}
	}
	f := newFixture(t, repo.Local)
	f.deploy(f.c, data)
	f.c.ExcludesPattern = new("acme-widget/**")
	if err := f.db.Repos().Update(t.Context(), f.c); err != nil {
		t.Fatal(err)
	}
	if r := f.do(
		"GET",
		base+"/v3/releases?module=acme-widget",
		"admin",
	); r.Code != 404 &&
		!strings.Contains(r.Body.String(), `"results":[]`) {
		t.Fatalf("excluded item leaked: %d %s", r.Code, r.Body.String())
	}
}

func TestAccCachedMetadataAuthorityAndURLFence(t *testing.T) {
	data := fixtureArchive(t)
	for _, tc := range []struct {
		name, base string
		cached     bool
		want       int
	}{
		{"cached valid", "https://example.com/puppet/v3/releases", true, 200},
		{"cached foreign origin", "https://evil.example/puppet/v3/releases", true, 502},
		{"cached outside root", "https://example.com/another/v3/releases", true, 502},
		{"fresh outside root", "https://example.com/another/v3/releases", false, 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := metadataSource{
				body:                   upstreamBody(t, data),
				metadataBase:           tc.base,
				skipMetadataValidation: tc.cached,
				check:                  func(context.Context, string) error { return nil },
			}
			f := newFixtureWith(
				t,
				repo.Remote,
				func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = &src },
			)
			if tc.cached {
				info := blobtest.Put(t.Context(), t, f.items.Blobs, []byte(src.body))
				src.cachedItem = &types.Item{Actual: types.Checksums{SHA256: info.Digests.SHA256}}
			}
			r := f.do("GET", base+"/v3/releases?module=acme-widget", "admin")
			if r.Code != tc.want {
				t.Fatalf("%d %s", r.Code, r.Body.String())
			}
		})
	}
	for _, tc := range []struct {
		url    string
		nilURL bool
		want   int
	}{
		{"https://evil.example/puppet/blob", false, 403},
		{"https://example.com/outside/blob", false, 403},
		{"", true, 400},
	} {
		t.Run(tc.url, func(t *testing.T) {
			target, _ := url.Parse(tc.url)
			src := metadataSource{
				body:        upstreamBody(t, data),
				archive:     data,
				archiveSet:  true,
				redirect:    target,
				checkNilURL: tc.nilURL,
				check:       func(context.Context, string) error { return nil },
			}
			f := newFixtureWith(
				t,
				repo.Remote,
				func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = src },
			)
			r := f.do("GET", base+"/v3/files/acme-widget-1.0.0.tar.gz", "admin")
			if r.Code != tc.want {
				t.Fatalf("%d %s", r.Code, r.Body.String())
			}
		})
	}
}

func TestAccVirtualMemberFailuresAndFiltering(t *testing.T) {
	for _, tc := range []struct {
		name                string
		failure             error
		repoFault, excluded bool
		want                int
	}{
		{name: "missing member", failure: store.ErrNotFound, want: 200},
		{name: "unavailable member", failure: source.ErrUnavailable, want: 502},
		{name: "member store failure", repoFault: true, want: 500},
		{name: "virtual excludes member item", excluded: true, want: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := metadataSource{
				readError: tc.failure,
				body:      upstreamBody(t, fixtureArchive(t)),
				check:     func(context.Context, string) error { return nil },
			}
			f := newFixtureStore(
				t,
				repo.Virtual,
				func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = src },
				func(s store.Store) store.Store { return &faultStore{Store: s, repos: tc.repoFault} },
			)
			member := *repo.Defaults(&repo.Config{Key: "member", Class: repo.Remote, PackageType: repo.Puppet, URL: "https://example.com/puppet/"})
			if err := f.db.Repos().Create(t.Context(), member); err != nil {
				t.Fatal(err)
			}
			f.c.Repositories = []string{"missing", member.Key}
			if tc.excluded {
				f.c.ExcludesPattern = new("acme-widget/*.tar.gz")
			}
			if err := f.db.Repos().Update(t.Context(), f.c); err != nil {
				t.Fatal(err)
			}
			r := f.do("GET", base+"/v3/releases?module=acme-widget", "admin")
			if r.Code != tc.want ||
				tc.want == 200 && !strings.Contains(r.Body.String(), `"results":[]`) {
				t.Fatalf("%d %s", r.Code, r.Body.String())
			}
		})
	}
}

func TestAccRemotePaginationDoesNotFollowNextAndRejectsDuplicate(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		t.Run(fmt.Sprint(duplicate), func(t *testing.T) {
			calls := 0
			src := metadataSource{
				check: func(context.Context, string) error { return nil },
				bodyAt: func(res source.Resource) string {
					calls++
					u, err := url.Parse(res.ExternalURL)
					if err != nil {
						t.Fatal(err)
					}
					if u.Host != "example.com" || u.Path != "/puppet/v3/releases" {
						t.Fatalf("followed untrusted pagination: %s", u)
					}
					offset := 0
					count := 1000
					var next any = "https://evil.example/steal"
					if calls == 2 {
						offset = 1000
						count = 1
						next = nil
					}
					if u.Query().Get("offset") != fmt.Sprint(offset) {
						t.Fatal(u)
					}
					entries := make([]map[string]any, 0, count)
					for i := 0; i < count; i++ {
						v := fmt.Sprintf("1.0.%d", offset+i)
						if duplicate && calls == 2 {
							v = "1.0.0"
						}
						entries = append(
							entries,
							map[string]any{
								"version":     v,
								"file_uri":    "/puppet/v3/files/file.tar.gz",
								"file_sha256": strings.Repeat("a", 64),
								"file_size":   100,
								"metadata":    map[string]any{"name": "acme-widget", "version": v},
							},
						)
					}
					b, err := json.Marshal(
						map[string]any{
							"results": entries,
							"pagination": map[string]any{
								"offset": offset,
								"total":  1001,
								"next":   next,
							},
						},
					)
					if err != nil {
						t.Fatal(err)
					}
					return string(b)
				},
			}
			f := newFixtureWith(
				t,
				repo.Remote,
				func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = src },
			)
			r := f.do("GET", base+"/v3/releases?module=acme-widget", "admin")
			want := 200
			if duplicate {
				want = 502
			}
			if r.Code != want || calls != 2 {
				t.Fatalf("status=%d calls=%d body=%s", r.Code, calls, r.Body.String())
			}
		})
	}
}
