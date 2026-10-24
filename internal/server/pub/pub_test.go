package pub_test

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
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/server/pub"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const base = "/artifactory/api/pub/pub-local"

type fixture struct {
	t  *testing.T
	r  http.Handler
	db *inmem.Store
	c  repo.Config
}

func newFixture(t *testing.T, class repo.Class) *fixture {
	return newFixtureWith(t, class, nil)
}

func newFixtureWith(t *testing.T, class repo.Class, tune func(*pkgroute.Protocol)) *fixture {
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
	items := &item.Service{Store: db, Blobs: blobs, Work: q}
	if err := items.RegisterWork(q); err != nil {
		t.Fatal(err)
	}
	c := *repo.Defaults(&repo.Config{Key: "pub-local", Class: class, PackageType: repo.Pub})
	if class == repo.Remote {
		c.URL = "https://example.com/pub/"
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
	p := pub.Protocol(pub.Options{Blobs: blobs})
	if tune != nil {
		tune(&p)
	}
	pkgroute.Mount(r, pkgroute.Options{
		Store: db, Items: func() *item.Service { return items },
		ItemError: func(_ context.Context, _ string, err error) error {
			var refused *format.Rejection
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
	return &fixture{t: t, r: r, db: db, c: c}
}

type metadataSource struct {
	check             func(context.Context, string) error
	missingRevalidate bool
	revalidateError   error
	revalidateCheck   func(context.Context) error
	readError         error
	missingStream     bool
	badCachedItem     bool
	body              string
	archive           []byte
	archiveSet        bool
	missingArchiveRV  bool
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
	if resource.Kind == source.Artifact {
		if !s.archiveSet {
			return source.Result{}, source.ErrUnavailable
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
	if body == "" {
		body = `{"name":"visible_pub","versions":[{"version":"1.0.0","archive_url":"https://example.com/pub/packages/visible_pub/versions/1.0.0.tar.gz","pubspec":{"name":"visible_pub","version":"1.0.0"}}]}`
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
			BaseURL: "https://example.com/pub/api/packages/visible_pub",
		},
		Revalidate: revalidate,
		CheckRead:  s.check,
	}
	if s.missingStream {
		result.Stream = nil
	}
	if s.badCachedItem {
		result.Stream = nil
		result.Item = &types.Item{Actual: types.Checksums{SHA256: "missing"}}
	}
	return result, nil
}

func TestUnitRemoteMetadataRequiresDescendantAuthority(t *testing.T) {
	for _, tc := range []struct {
		name      string
		check     func(context.Context, string) error
		missingRV bool
		want      int
	}{
		{"missing read check", nil, false, 502},
		{"missing revalidation", func(context.Context, string) error { return nil }, true, 502},
		{"denied", func(context.Context, string) error { return source.ErrDenied }, false, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixtureWith(t, repo.Remote, func(p *pkgroute.Protocol) {
				p.Sources[repo.Remote] = metadataSource{
					check: tc.check, missingRevalidate: tc.missingRV,
				}
			})
			rec := f.do("GET", base+"/api/packages/visible_pub", "admin", "", nil)
			if rec.Code != tc.want || strings.Contains(rec.Body.String(), "1.0.0") {
				t.Fatalf("authority result: %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestUnitRemoteMetadataExcludesDeniedVersions(t *testing.T) {
	body := `{"name":"visible_pub","versions":[` +
		`{"version":"1.0.0","archive_url":"https://example.com/pub/one.tar.gz","pubspec":{"name":"visible_pub","version":"1.0.0"}},` +
		`{"version":"2.0.0","archive_url":"https://example.com/pub/two.tar.gz","pubspec":{"name":"visible_pub","version":"2.0.0"}}]}`
	f := newFixtureWith(t, repo.Remote, func(p *pkgroute.Protocol) {
		p.Sources[repo.Remote] = metadataSource{
			body: body,
			check: func(_ context.Context, path string) error {
				if path == "visible_pub/visible_pub-2.0.0.tar.gz" {
					return source.ErrDenied
				}
				return nil
			},
		}
	})
	rec := f.do("GET", base+"/api/packages/visible_pub", "admin", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"1.0.0"`) ||
		strings.Contains(rec.Body.String(), `"2.0.0"`) {
		t.Fatalf("permission-safe metadata: %d %s", rec.Code, rec.Body)
	}
}

func TestUnitRemoteMetadataRejectsBrokenSourceAndStaleAuthority(t *testing.T) {
	allowed := func(context.Context, string) error { return nil }
	for _, tc := range []struct {
		name   string
		source metadataSource
		want   int
	}{
		{"source failure", metadataSource{check: allowed, readError: source.ErrUnavailable}, 502},
		{"missing result body", metadataSource{check: allowed, missingStream: true}, 502},
		{"missing cached metadata blob", metadataSource{check: allowed, badCachedItem: true}, 500},
		{"malformed metadata", metadataSource{check: allowed, body: `{"name":"visible_pub","versions":[`}, 502},
		{"stale metadata authority", metadataSource{check: allowed, revalidateError: source.ErrSuperseded}, 503},
		{"exact version check fails", metadataSource{check: func(context.Context, string) error { return source.ErrUnavailable }}, 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixtureWith(t, repo.Remote, func(p *pkgroute.Protocol) {
				p.Sources[repo.Remote] = tc.source
			})
			rec := f.do(http.MethodGet, base+"/api/packages/visible_pub", "admin", "", nil)
			if rec.Code != tc.want || strings.Contains(rec.Body.String(), "1.0.0") {
				t.Fatalf("broken remote source disclosed metadata: %d %s", rec.Code, rec.Body)
			}
			if tc.name == "source failure" {
				for _, path := range []string{
					base + "/api/packages/visible_pub/versions/1.0.0",
					base + "/packages/visible_pub/versions/1.0.0.tar.gz",
				} {
					rec := f.do(http.MethodGet, path, "admin", "", nil)
					if rec.Code != http.StatusBadGateway ||
						strings.Contains(rec.Body.String(), "1.0.0") {
						t.Fatalf("broken source route %s: %d %s", path, rec.Code, rec.Body)
					}
				}
			}
		})
	}
}

func TestUnitRemoteArchiveRechecksExactPathAtDelivery(t *testing.T) {
	data := archive(t, "visible_pub", "1.0.0")
	path := "visible_pub/visible_pub-1.0.0.tar.gz"
	archiveURL := base + "/packages/visible_pub/versions/1.0.0.tar.gz"
	for _, tc := range []struct {
		name             string
		denyAt           int
		missingArchive   bool
		missingArchiveRV bool
		want             int
	}{
		{"allowed", 0, false, false, http.StatusOK},
		{"revoked before fetch", 2, false, false, http.StatusForbidden},
		{"revoked at delivery", 3, false, false, http.StatusForbidden},
		{"archive source unavailable", 0, true, false, http.StatusBadGateway},
		{"missing archive revalidation", 0, false, true, http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checks := 0
			f := newFixtureWith(t, repo.Remote, func(p *pkgroute.Protocol) {
				p.Sources[repo.Remote] = metadataSource{
					archive: data, archiveSet: !tc.missingArchive,
					missingArchiveRV: tc.missingArchiveRV,
					check: func(_ context.Context, got string) error {
						if got != path {
							return source.ErrDenied
						}
						checks++
						if checks == tc.denyAt {
							return source.ErrDenied
						}
						return nil
					},
				}
			})
			rec := f.do(http.MethodGet, archiveURL, "admin", "", nil)
			if rec.Code != tc.want ||
				tc.want == http.StatusOK && !bytes.Equal(rec.Body.Bytes(), data) ||
				tc.want != http.StatusOK && bytes.Equal(rec.Body.Bytes(), data) {
				t.Fatalf("remote archive: %d %s", rec.Code, rec.Body)
			}
			if checks < 2 {
				t.Fatalf(
					"expected exact artifact authorization before fetch, got %d checks",
					checks,
				)
			}
		})
	}
}

func TestUnitRemoteArchiveRejectsContentAndChecksumMismatch(t *testing.T) {
	good := archive(t, "visible_pub", "1.0.0")
	hash := sha256.Sum256(good)
	body := `{"name":"visible_pub","versions":[{"version":"1.0.0","archive_url":"https://example.com/pub/archive.tar.gz","archive_sha256":"` +
		hex.EncodeToString(
			hash[:],
		) + `","pubspec":{"name":"visible_pub","version":"1.0.0"}}]}`
	for _, tc := range []struct {
		name    string
		archive []byte
	}{
		{"wrong checksum", archive(t, "visible_pub", "2.0.0")},
		{"invalid archive", []byte("not a tarball")},
		{"empty archive", []byte{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixtureWith(t, repo.Remote, func(p *pkgroute.Protocol) {
				p.Sources[repo.Remote] = metadataSource{
					body: body, archive: tc.archive, archiveSet: true,
					check: func(context.Context, string) error { return nil },
				}
			})
			rec := f.do(
				http.MethodGet,
				base+"/packages/visible_pub/versions/1.0.0.tar.gz",
				"admin",
				"",
				nil,
			)
			if rec.Code != http.StatusBadGateway || bytes.Equal(rec.Body.Bytes(), tc.archive) {
				t.Fatalf("untrusted archive: %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestUnitRemoteArchiveRevalidatesMetadataBeforeAndDuringDelivery(t *testing.T) {
	for _, failAt := range []int{2, 3} {
		t.Run(fmt.Sprintf("revalidation-%d", failAt), func(t *testing.T) {
			calls := 0
			f := newFixtureWith(t, repo.Remote, func(p *pkgroute.Protocol) {
				p.Sources[repo.Remote] = metadataSource{
					archive: archive(t, "visible_pub", "1.0.0"), archiveSet: true,
					check: func(context.Context, string) error { return nil },
					revalidateCheck: func(context.Context) error {
						calls++
						if calls == failAt {
							return source.ErrSuperseded
						}
						return nil
					},
				}
			})
			rec := f.do(
				http.MethodGet,
				base+"/packages/visible_pub/versions/1.0.0.tar.gz",
				"admin",
				"",
				nil,
			)
			if rec.Code != http.StatusServiceUnavailable || calls < failAt ||
				strings.Contains(rec.Body.String(), "visible_pub") {
				t.Fatalf("stale archive delivery: %d %s, calls=%d", rec.Code, rec.Body, calls)
			}
		})
	}
}

func (f *fixture) do(
	method, path, user, contentType string,
	body io.Reader,
) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(method, path, body)
	r.Header.Set("X-User", user)
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	f.r.ServeHTTP(w, r)
	return w
}

func archive(t *testing.T, name, version string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tarball := tar.NewWriter(gz)
	pubspec := []byte("name: " + name + "\nversion: " + version + "\ndescription: test package\n")
	if err := tarball.WriteHeader(
		&tar.Header{Name: "pubspec.yaml", Mode: 0o600, Size: int64(len(pubspec))},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tarball.Write(pubspec); err != nil {
		t.Fatal(err)
	}
	if err := tarball.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func upload(t *testing.T, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	p, err := w.CreateFormFile("file", "package.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &b, w.FormDataContentType()
}

func TestUnitPublishAndResolveLocalAndFederated(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) {
			f := newFixture(t, class)
			if rec := f.do(
				"GET",
				base+"/api/packages/versions/new",
				"admin",
				"",
				nil,
			); rec.Code != 200 ||
				!strings.Contains(rec.Body.String(), `"fields"`) {
				t.Fatalf("upload discovery: %d %s", rec.Code, rec.Body)
			}
			body, ct := upload(t, archive(t, "hello_pub", "1.2.3"))
			rec := f.do("POST", base+"/api/packages/versions/new", "admin", ct, body)
			if rec.Code != http.StatusNoContent || rec.Header().Get("Location") == "" {
				t.Fatalf("publish: %d %s", rec.Code, rec.Body)
			}
			rec = f.do("GET", rec.Header().Get("Location"), "admin", "", nil)
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"success"`) {
				t.Fatalf("finalize: %d %s", rec.Code, rec.Body)
			}
			rec = f.do("GET", base+"/api/packages/hello_pub", "admin", "", nil)
			if rec.Code != 200 {
				t.Fatalf("versions: %d %s", rec.Code, rec.Body)
			}
			var doc struct {
				Name     string `json:"name"`
				Versions []struct {
					ArchiveURL string `json:"archive_url"`
					Version    string `json:"version"`
				} `json:"versions"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
				t.Fatal(err)
			}
			if doc.Name != "hello_pub" || len(doc.Versions) != 1 ||
				doc.Versions[0].Version != "1.2.3" ||
				!strings.HasPrefix(doc.Versions[0].ArchiveURL, "http://example.com/") {
				t.Fatalf("metadata: %+v", doc)
			}
			rec = f.do("GET", doc.Versions[0].ArchiveURL, "admin", "", nil)
			if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), archive(t, "hello_pub", "1.2.3")) {
				t.Fatalf("archive: %d %s", rec.Code, rec.Body)
			}
			body, ct = upload(t, archive(t, "hello_pub", "1.2.3"))
			if rec = f.do(
				"POST",
				base+"/api/packages/versions/new",
				"admin",
				ct,
				body,
			); rec.Code != 409 {
				t.Fatalf("republish: %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestUnitRejectedUploadDoesNotPublish(t *testing.T) {
	f := newFixture(t, repo.Local)
	for _, data := range [][]byte{[]byte("not tar"), archive(t, "bad-name", "1.0.0")} {
		body, ct := upload(t, data)
		if rec := f.do(
			"POST",
			base+"/api/packages/versions/new",
			"admin",
			ct,
			body,
		); rec.Code != 400 {
			t.Fatalf("invalid archive: %d %s", rec.Code, rec.Body)
		}
	}
	if rec := f.do("GET", base+"/api/packages/hello_pub", "admin", "", nil); rec.Code != 404 {
		t.Fatalf("ghost metadata: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do("GET", base+"/api/packages/../hello_pub", "admin", "", nil); rec.Code == 200 {
		t.Fatalf("unsafe path accepted")
	}
}

func TestUnitAnonymousPublishRequiresBearerAuthentication(t *testing.T) {
	f := newFixture(t, repo.Local)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		var body io.Reader
		contentType := ""
		if method == http.MethodPost {
			body, contentType = upload(t, archive(t, "private_pub", "1.0.0"))
		}
		rec := f.do(method, base+"/api/packages/versions/new", "", contentType, body)
		if rec.Code != http.StatusUnauthorized ||
			rec.Header().Get("WWW-Authenticate") != `Bearer realm="pub"` ||
			!strings.Contains(rec.Body.String(), `"error"`) ||
			strings.Contains(rec.Body.String(), "private_pub") {
			t.Fatalf("anonymous %s: %d %s, challenge=%q", method, rec.Code,
				rec.Body, rec.Header().Get("WWW-Authenticate"))
		}
	}
	if rec := f.do(
		http.MethodGet,
		base+"/api/packages/private_pub",
		"",
		"",
		nil,
	); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous metadata: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(
		http.MethodGet,
		base+"/api/packages/private_pub",
		"admin",
		"",
		nil,
	); rec.Code != http.StatusNotFound {
		t.Fatalf("unauthorized publish persisted package: %d %s", rec.Code, rec.Body)
	}
}

func TestUnitVersionAndArchiveRoutesEnforceIdentityAndReadRights(t *testing.T) {
	f := newFixture(t, repo.Local)
	data := archive(t, "widget_pub", "1.2.3")
	body, contentType := upload(t, data)
	rec := f.do(http.MethodPost, base+"/api/packages/versions/new", "admin", contentType, body)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body)
	}
	for _, tc := range []struct {
		name   string
		method string
		path   string
		user   string
		want   int
	}{
		{"specific version", http.MethodGet, base + "/api/packages/widget_pub/versions/1.2.3", "admin", 200},
		{"invalid package version", http.MethodGet, base + "/api/packages/Bad-Name/versions/1.2.3", "admin", 400},
		{"unknown version", http.MethodGet, base + "/api/packages/widget_pub/versions/2.0.0", "admin", 404},
		{"bad version", http.MethodGet, base + "/api/packages/widget_pub/versions/bad", "admin", 400},
		{"bad package", http.MethodGet, base + "/api/packages/Bad-Name", "admin", 400},
		{"unknown package", http.MethodGet, base + "/api/packages/absent_pub", "admin", 404},
		{"query rejected", http.MethodGet, base + "/api/packages/widget_pub?all=true", "admin", 400},
		{"anonymous version", http.MethodGet, base + "/api/packages/widget_pub/versions/1.2.3", "", 401},
		{"viewer version", http.MethodGet, base + "/api/packages/widget_pub/versions/1.2.3", "viewer", 403},
		{"archive HEAD", http.MethodHead, base + "/packages/widget_pub/versions/1.2.3.tar.gz", "admin", 200},
		{"invalid archive package", http.MethodGet, base + "/packages/Bad-Name/versions/1.2.3.tar.gz", "admin", 400},
		{"bad archive version", http.MethodGet, base + "/packages/widget_pub/versions/bad.tar.gz", "admin", 400},
		{"unknown archive version", http.MethodGet, base + "/packages/widget_pub/versions/2.0.0.tar.gz", "admin", 404},
		{"anonymous archive", http.MethodGet, base + "/packages/widget_pub/versions/1.2.3.tar.gz", "", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.do(tc.method, tc.path, tc.user, "", nil)
			if rec.Code != tc.want {
				t.Fatalf("%s: %d %s, want %d", tc.path, rec.Code, rec.Body, tc.want)
			}
			if tc.method == http.MethodHead && rec.Body.Len() != 0 {
				t.Fatalf("HEAD returned a body: %s", rec.Body)
			}
		})
	}
}

func TestUnitPublishFinalizeRejectsMalformedOrChangedUploads(t *testing.T) {
	f := newFixture(t, repo.Local)
	for _, path := range []string{
		base + "/api/packages/versions/new/complete",
		base + "/api/packages/versions/new/complete?name=widget_pub&version=bad&sha256=" + strings.Repeat("a", 64),
	} {
		if rec := f.do(http.MethodGet, path, "admin", "", nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("malformed finalize: %d %s", rec.Code, rec.Body)
		}
	}
	unknown := base + "/api/packages/versions/new/complete?name=widget_pub&version=1.2.3&sha256=" + strings.Repeat(
		"a",
		64,
	)
	if rec := f.do(http.MethodGet, unknown, "admin", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown version finalize: %d %s", rec.Code, rec.Body)
	}
	data := archive(t, "widget_pub", "1.2.3")
	body, contentType := upload(t, data)
	rec := f.do(http.MethodPost, base+"/api/packages/versions/new", "admin", contentType, body)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodGet, unknown, "admin", "", nil); rec.Code != http.StatusConflict {
		t.Fatalf("changed checksum finalize: %d %s", rec.Code, rec.Body)
	}
	for _, user := range []string{"", "viewer"} {
		want := http.StatusUnauthorized
		if user != "" {
			want = http.StatusForbidden
		}
		if rec := f.do(
			http.MethodGet,
			rec.Header().Get("Location"),
			user,
			"",
			nil,
		); rec.Code != want {
			t.Fatalf("unauthorized finalize as %q: %d %s", user, rec.Code, rec.Body)
		}
	}
	if rec := f.do(
		http.MethodGet,
		rec.Header().Get("Location"),
		"admin",
		"",
		nil,
	); rec.Code != http.StatusOK {
		t.Fatalf("valid finalize: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(
		http.MethodPost,
		base+"/api/packages/versions/new",
		"viewer",
		"multipart/form-data",
		strings.NewReader(""),
	); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer upload: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(
		http.MethodPost,
		base+"/api/packages/versions/new",
		"admin",
		"application/json",
		strings.NewReader("{}"),
	); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("invalid multipart upload: %d %s", rec.Code, rec.Body)
	}
}

func TestUnitPublishRejectsReadOnlyClassesAndOversizedBody(t *testing.T) {
	for _, class := range []repo.Class{repo.Remote, repo.Virtual} {
		t.Run(string(class), func(t *testing.T) {
			f := newFixtureWith(t, class, func(p *pkgroute.Protocol) {
				if class == repo.Remote {
					p.Sources[repo.Remote] = metadataSource{}
				}
			})
			for _, tc := range []struct {
				method string
				path   string
			}{
				{http.MethodGet, base + "/api/packages/versions/new"},
				{http.MethodPost, base + "/api/packages/versions/new"},
				{http.MethodGet, base + "/api/packages/versions/new/complete"},
			} {
				rec := f.do(tc.method, tc.path, "admin", "", nil)
				if rec.Code != http.StatusMethodNotAllowed {
					t.Fatalf("%s publish on %s: %d %s", tc.method, class, rec.Code, rec.Body)
				}
			}
		})
	}
	f := newFixture(t, repo.Local)
	r := httptest.NewRequest(
		http.MethodPost,
		base+"/api/packages/versions/new",
		strings.NewReader("x"),
	)
	r.Header.Set("X-User", "admin")
	r.ContentLength = 70 << 20
	rec := httptest.NewRecorder()
	f.r.ServeHTTP(rec, r)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized multipart: %d %s", rec.Code, rec.Body)
	}
}
