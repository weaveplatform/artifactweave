package pubhttp_test

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
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	pubfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/pub"
	pubhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/pub/http"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

const base = "/artifactory/api/pub/pub-local"

type fixture struct {
	t     *testing.T
	r     http.Handler
	db    *inmem.Store
	blobs *fs.Store
	c     repo.Config
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
	items := &item.Service{Store: db, Blobs: blobs, Work: q, Formats: ecosystem.For}
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
	r.Use(accesstest.Over(t, db).Middleware(t, true, func(r *http.Request) kernel.Principal {
		name := r.Header.Get("X-User")
		return kernel.Principal{Name: name, Admin: name == "admin", Anonymous: name == ""}
	}))
	p := pubhttp.Protocol(pubhttp.Options{Blobs: blobs})
	if tune != nil {
		tune(&p)
	}
	pkgroute.Mount(r, pkgroute.Options{
		Store: db, Items: func() *item.Service { return items },
		ItemError: func(_ context.Context, _ string, err error) error {
			var refused *format.RejectionError
			if errors.As(err, &refused) {
				return apierror.New(refused.Status, refused.Message)
			}
			return err
		},
		ServeItem: func(w http.ResponseWriter, r *http.Request, _ repo.Config, it model.Item) {
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
	return &fixture{t: t, r: r, db: db, blobs: blobs, c: c}
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
	pkgroute.Store,
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
		result.Item = &model.Item{Actual: kernel.Checksums{SHA256: "missing"}}
	}
	return result, nil
}

func TestUnitRemoteMetadataRequiresDescendantAuthority(t *testing.T) {
	for _, tc := range []struct {
		name      string
		check     func(context.Context, string) error
		missingRV bool
		expected  int
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
			if rec.Code != tc.expected || strings.Contains(rec.Body.String(), "1.0.0") {
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
		name     string
		source   metadataSource
		expected int
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
			if rec.Code != tc.expected || strings.Contains(rec.Body.String(), "1.0.0") {
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
		expected         int
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
			if rec.Code != tc.expected ||
				tc.expected == http.StatusOK && !bytes.Equal(rec.Body.Bytes(), data) ||
				tc.expected != http.StatusOK && bytes.Equal(rec.Body.Bytes(), data) {
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
	return describedArchive(t, name, version, "test package")
}

func describedArchive(t *testing.T, name, version, description string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tarball := tar.NewWriter(gz)
	pubspec := []byte(
		"name: " + name + "\nversion: " + version + "\ndescription: " + description + "\n",
	)
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

// zeroUpload is upload with a file of size zero bytes, streamed rather than
// built in memory because the archives at the size limit are 64 MiB.
func zeroUpload(t *testing.T, size int64) (io.Reader, string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	if _, err := w.CreateFormFile("file", "package.tar.gz"); err != nil {
		t.Fatal(err)
	}
	head := bytes.Clone(b.Bytes())
	b.Reset()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return io.MultiReader(bytes.NewReader(head), io.LimitReader(zeros{}, size), &b),
		w.FormDataContentType()
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
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
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"not an archive", []byte("not tar")},
		{"invalid name", archive(t, "bad-name", "1.0.0")},
	} {
		body, ct := upload(t, tc.data)
		rec := f.do("POST", base+"/api/packages/versions/new", "admin", ct, body)
		if status, message := pubError(t, rec); status != http.StatusBadRequest ||
			message != "invalid Dart pub archive" {
			t.Fatalf("%s upload: %d %s", tc.name, rec.Code, rec.Body)
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
		name     string
		method   string
		path     string
		user     string
		expected int
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
		{"archive head", http.MethodHead, base + "/packages/widget_pub/versions/1.2.3.tar.gz", "admin", 200},
		{"invalid archive package", http.MethodGet, base + "/packages/Bad-Name/versions/1.2.3.tar.gz", "admin", 400},
		{"bad archive version", http.MethodGet, base + "/packages/widget_pub/versions/bad.tar.gz", "admin", 400},
		{"unknown archive version", http.MethodGet, base + "/packages/widget_pub/versions/2.0.0.tar.gz", "admin", 404},
		{"anonymous archive", http.MethodGet, base + "/packages/widget_pub/versions/1.2.3.tar.gz", "", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.do(tc.method, tc.path, tc.user, "", nil)
			if rec.Code != tc.expected {
				t.Fatalf("%s: %d %s, want %d", tc.path, rec.Code, rec.Body, tc.expected)
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

// TestUnitPublishRefusesOnlyAnArchiveOverTheLimitAsTooLarge: inside the
// multipart allowance, an upload of exactly MaxArchiveBytes is still read as
// an archive, so content that is not one answers 400, and one byte more
// answers 413.
func TestUnitPublishRefusesOnlyAnArchiveOverTheLimitAsTooLarge(t *testing.T) {
	f := newFixture(t, repo.Local)
	for _, tc := range []struct {
		size    int64
		status  int
		message string
	}{
		{pubfmt.MaxArchiveBytes, http.StatusBadRequest, "invalid Dart pub archive"},
		{pubfmt.MaxArchiveBytes + 1, http.StatusRequestEntityTooLarge, "pub archive exceeds limit"},
	} {
		body, contentType := zeroUpload(t, tc.size)
		rec := f.do(http.MethodPost, base+"/api/packages/versions/new", "admin", contentType, body)
		if status, message := pubError(t, rec); status != tc.status || message != tc.message {
			t.Fatalf("upload of %d bytes: %d %q, want %d %q",
				tc.size, status, message, tc.status, tc.message)
		}
	}
}

// keyedSource answers each remote repository from the metadataSource its key
// names, so one fixture holds remote members that behave differently.
type keyedSource map[string]metadataSource

func (keyedSource) Item(
	context.Context,
	pkgroute.Store,
	repo.Config,
	string,
) (pkgroute.Found, error) {
	return pkgroute.Found{}, source.ErrUnsupported
}

func (s keyedSource) Read(
	r *http.Request,
	c repo.Config,
	resource source.Resource,
) (source.Result, error) {
	return s[c.Key].Read(r, c, resource)
}

// checkedSource is metadataSource with the steps of the real source around
// it: with validate it hands the metadata document to the resource's
// validator first, as the source does before it serves or caches one;
// unreadable hands a validator a body that fails to read; rewrite changes
// the metadata result, as a cache hit or a broken stream would; and asked
// records every resource requested.
type checkedSource struct {
	metadataSource
	validate   bool
	unreadable bool
	rewrite    func(*source.Result)
	asked      *[]source.Resource
}

func (s checkedSource) Read(
	r *http.Request,
	c repo.Config,
	resource source.Resource,
) (source.Result, error) {
	if s.asked != nil {
		*s.asked = append(*s.asked, resource)
	}
	if resource.Kind == source.ResourceKindArtifact && s.unreadable {
		return source.Result{}, resource.Validate(
			r.Context(), failingReadSeeker{}, int64(len(s.archive)),
		)
	}
	if resource.Kind == source.ResourceKindMetadata && s.validate {
		body := io.ReadSeeker(strings.NewReader(s.body))
		if s.unreadable {
			body = failingReadSeeker{}
		}
		if err := resource.ValidateMetadata(r.Context(), body, int64(len(s.body)),
			source.MetadataResponse{
				BaseURL: "https://example.com/pub/api/packages/visible_pub",
			}); err != nil {
			return source.Result{}, err
		}
	}
	result, err := s.metadataSource.Read(r, c, resource)
	if err == nil && resource.Kind == source.ResourceKindMetadata && s.rewrite != nil {
		s.rewrite(&result)
	}
	return result, err
}

type failingReadSeeker struct{}

func (failingReadSeeker) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func (failingReadSeeker) Seek(int64, int) (int64, error) { return 0, nil }

func (f *fixture) createRepo(c repo.Config) {
	f.t.Helper()
	if err := f.db.Repos().Create(f.t.Context(), *repo.Defaults(&c)); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) setMembers(keys ...string) {
	f.t.Helper()
	f.c.Repositories = keys
	if err := f.db.Repos().Update(f.t.Context(), f.c); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) publish(key string, data []byte) {
	f.t.Helper()
	body, contentType := upload(f.t, data)
	rec := f.do(http.MethodPost, "/artifactory/api/pub/"+key+"/api/packages/versions/new",
		"admin", contentType, body)
	if rec.Code != http.StatusNoContent {
		f.t.Fatalf("publish to %s: %d %s", key, rec.Code, rec.Body)
	}
}

// grant gives user actions on the paths each repository key maps to.
func (f *fixture) grant(user string, actions accessapi.Actions, paths map[string][]string) {
	f.t.Helper()
	targets := map[string]accesstest.Target{}
	for key, include := range paths {
		targets[key] = accesstest.Target{Include: include}
	}
	if err := f.db.Permissions().Create(f.t.Context(), accesstest.Permission{
		Name: user,
		Resources: map[accessapi.ResourceType]accesstest.Resource{accessapi.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]accessapi.Actions{user: actions}},
			Targets: targets,
		}},
	}); err != nil {
		f.t.Fatal(err)
	}
}

type versionsDocument struct {
	Latest struct {
		Version string `json:"version"`
	} `json:"latest"`
	Versions []struct {
		Version    string         `json:"version"`
		ArchiveURL string         `json:"archive_url"`
		Pubspec    map[string]any `json:"pubspec"`
	} `json:"versions"`
}

// versionsOf lists a package's versions in repository key as user and
// returns the document with the versions in the order listed.
func (f *fixture) versionsOf(key, name, user string) (versionsDocument, []string) {
	f.t.Helper()
	rec := f.do(http.MethodGet, "/artifactory/api/pub/"+key+"/api/packages/"+name, user, "", nil)
	if rec.Code != http.StatusOK {
		f.t.Fatalf("versions of %s in %s as %q: %d %s", name, key, user, rec.Code, rec.Body)
	}
	var doc versionsDocument
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		f.t.Fatal(err)
	}
	listed := make([]string, 0, len(doc.Versions))
	for _, v := range doc.Versions {
		listed = append(listed, v.Version)
	}
	return doc, listed
}

// pubError is the status and message of a pub error document.
func pubError(t *testing.T, rec *httptest.ResponseRecorder) (int, string) {
	t.Helper()
	var doc struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("pub error document %q: %v", rec.Body, err)
	}
	return rec.Code, doc.Error.Message
}

// TestUnitVirtualMergesReadableMemberVersions: a virtual repository lists its
// members' versions under its own key, newest first, a version an earlier
// member holds shadowing a later member's copy; it passes over a missing
// member and one the caller may not read, and serves each archive from the
// member whose version it listed.
func TestUnitVirtualMergesReadableMemberVersions(t *testing.T) {
	f := newFixture(t, repo.Virtual)
	for _, key := range []string{"pub-a", "pub-b"} {
		f.createRepo(repo.Config{Key: key, Class: repo.Local, PackageType: repo.Pub})
	}
	first := describedArchive(t, "hello_pub", "1.0.0", "from a")
	later := archive(t, "hello_pub", "1.5.0")
	f.publish("pub-a", first)
	f.publish("pub-a", archive(t, "hello_pub", "2.0.0-beta.1"))
	f.publish("pub-b", describedArchive(t, "hello_pub", "1.0.0", "from b"))
	f.publish("pub-b", later)
	f.setMembers("missing", "pub-a", "pub-b")
	doc, listed := f.versionsOf(f.c.Key, "hello_pub", "admin")
	if !slices.Equal(listed, []string{"2.0.0-beta.1", "1.5.0", "1.0.0"}) ||
		doc.Latest.Version != "1.5.0" || doc.Versions[2].Pubspec["description"] != "from a" {
		t.Fatalf("merged versions %v, latest %s: %+v", listed, doc.Latest.Version, doc)
	}
	for _, v := range doc.Versions {
		if !strings.HasPrefix(v.ArchiveURL,
			"http://example.com/artifactory/api/pub/pub-local/packages/hello_pub/versions/") {
			t.Errorf("archive of %s outside the virtual repository: %s", v.Version, v.ArchiveURL)
		}
	}
	rec := f.do(http.MethodGet, base+"/api/packages/hello_pub/versions/1.0.0", "admin", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"from a"`) {
		t.Fatalf("shadowed version: %d %s", rec.Code, rec.Body)
	}
	for version, want := range map[string][]byte{"1.0.0": first, "1.5.0": later} {
		rec := f.do(http.MethodGet, base+"/packages/hello_pub/versions/"+version+".tar.gz",
			"admin", "", nil)
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), want) {
			t.Fatalf("archive %s: %d, %d bytes", version, rec.Code, rec.Body.Len())
		}
	}
	f.grant("partial", accessapi.ActionRead,
		map[string][]string{f.c.Key: {"**"}, "pub-a": {"**"}})
	doc, listed = f.versionsOf(f.c.Key, "hello_pub", "partial")
	if !slices.Equal(listed, []string{"2.0.0-beta.1", "1.0.0"}) || doc.Latest.Version != "1.0.0" {
		t.Fatalf("versions without the second member: %v, latest %s", listed, doc.Latest.Version)
	}
	rec = f.do(http.MethodGet, base+"/packages/hello_pub/versions/1.5.0.tar.gz", "partial", "", nil)
	if status, message := pubError(t, rec); status != http.StatusNotFound ||
		message != "version not found" {
		t.Fatalf("archive of an unreadable member: %d %q", status, message)
	}
	rec = f.do(http.MethodGet, base+"/api/packages/absent_pub", "admin", "", nil)
	if status, message := pubError(t, rec); status != http.StatusNotFound ||
		message != "package not found" {
		t.Fatalf("package no member holds: %d %q", status, message)
	}
}

// TestUnitVirtualRemoteMembers: through a virtual repository a remote member
// that does not hold the package is passed over, another remote member's
// version is listed and its archive fetched through that member, and a
// member of another package type or one whose source fails fails the
// listing.
func TestUnitVirtualRemoteMembers(t *testing.T) {
	data := archive(t, "visible_pub", "1.0.0")
	f := newFixtureWith(t, repo.Virtual, func(p *pkgroute.Protocol) {
		p.Sources[repo.Remote] = keyedSource{
			"pub-absent": {readError: kernel.ErrNotFound},
			"pub-remote": {
				check:   func(context.Context, string) error { return nil },
				archive: data, archiveSet: true,
			},
			"pub-down": {readError: source.ErrUnavailable},
		}
	})
	for _, key := range []string{"pub-absent", "pub-remote", "pub-down"} {
		f.createRepo(repo.Config{
			Key: key, Class: repo.Remote, PackageType: repo.Pub, URL: "https://example.com/pub/",
		})
	}
	f.createRepo(repo.Config{Key: "generic-local", Class: repo.Local, PackageType: repo.Generic})
	for key, members := range map[string][]string{
		"pub-mixed":   {"generic-local"},
		"pub-failing": {"pub-absent", "pub-down"},
	} {
		f.createRepo(repo.Config{
			Key: key, Class: repo.Virtual, PackageType: repo.Pub, Repositories: members,
		})
	}
	f.setMembers("pub-absent", "pub-remote")
	doc, listed := f.versionsOf(f.c.Key, "visible_pub", "admin")
	if !slices.Equal(listed, []string{"1.0.0"}) || doc.Versions[0].ArchiveURL !=
		"http://example.com/artifactory/api/pub/pub-local/packages/visible_pub/versions/1.0.0.tar.gz" {
		t.Fatalf("remote member's versions: %+v", doc)
	}
	rec := f.do(
		http.MethodGet,
		base+"/packages/visible_pub/versions/1.0.0.tar.gz",
		"admin",
		"",
		nil,
	)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), data) {
		t.Fatalf("remote member's archive: %d %s", rec.Code, rec.Body)
	}
	for key, want := range map[string]struct {
		status  int
		message string
	}{
		"pub-mixed":   {http.StatusBadRequest, "invalid virtual repository member"},
		"pub-failing": {http.StatusBadGateway, "Bad Gateway"},
	} {
		rec := f.do(http.MethodGet, "/artifactory/api/pub/"+key+"/api/packages/visible_pub",
			"admin", "", nil)
		if status, message := pubError(t, rec); status != want.status || message != want.message {
			t.Errorf("%s: %d %q, want %d %q", key, status, message, want.status, want.message)
		}
	}
}

// TestUnitHostedVersionsFollowTheStoredIndex: a hosted repository lists only
// the versions whose archives the caller may read, orders versions that
// differ only in build metadata by their text, refuses an archive name
// without the .tar.gz suffix, and answers 502 once its index no longer
// matches the stored archives.
func TestUnitHostedVersionsFollowTheStoredIndex(t *testing.T) {
	f := newFixture(t, repo.Local)
	for _, version := range []string{"1.0.0+b", "2.0.0", "1.0.0+a"} {
		f.publish(f.c.Key, archive(t, "widget_pub", version))
	}
	doc, listed := f.versionsOf(f.c.Key, "widget_pub", "admin")
	if !slices.Equal(listed, []string{"2.0.0", "1.0.0+a", "1.0.0+b"}) ||
		doc.Latest.Version != "2.0.0" {
		t.Fatalf("hosted versions %v, latest %s", listed, doc.Latest.Version)
	}
	f.grant("partial", accessapi.ActionRead,
		map[string][]string{f.c.Key: {"widget_pub/widget_pub-1.0.0+a.tar.gz"}})
	if _, listed := f.versionsOf(f.c.Key, "widget_pub", "partial"); !slices.Equal(
		listed, []string{"1.0.0+a"},
	) {
		t.Fatalf("versions listed to a reader of one archive: %v", listed)
	}
	rec := f.do(http.MethodGet, base+"/packages/widget_pub/versions/2.0.0.zip", "admin", "", nil)
	if status, message := pubError(t, rec); status != http.StatusBadRequest ||
		message != "invalid Dart pub archive version" {
		t.Fatalf("archive name without .tar.gz: %d %q", status, message)
	}
	ctx := t.Context()
	p, err := f.db.Packages().Find(ctx, f.c.Key, "widget_pub")
	if err != nil {
		t.Fatal(err)
	}
	versions, err := f.db.Packages().Versions(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(
		versions,
		func(v model.PackageVersion) bool { return v.Version == "2.0.0" },
	)
	original := versions[i]
	moved := original
	moved.Metadata = []byte(`{"name":"widget_pub","version":"2.0.0","path":"elsewhere"}`)
	if _, err := f.db.Packages().PutVersion(ctx, moved); err != nil {
		t.Fatal(err)
	}
	rec = f.do(http.MethodGet, base+"/api/packages/widget_pub", "admin", "", nil)
	if status, message := pubError(t, rec); status != http.StatusBadGateway ||
		message != "invalid stored pub package index" {
		t.Fatalf("index naming another path: %d %q", status, message)
	}
	if _, err := f.db.Packages().PutVersion(ctx, original); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Items().Delete(ctx, f.c.Key, false,
		"widget_pub/widget_pub-1.0.0+b.tar.gz"); err != nil {
		t.Fatal(err)
	}
	rec = f.do(http.MethodGet, base+"/api/packages/widget_pub", "admin", "", nil)
	if status, message := pubError(t, rec); status != http.StatusBadGateway ||
		message != "invalid stored pub archive" {
		t.Fatalf("index naming a removed archive: %d %q", status, message)
	}
}

// TestUnitPublishRequiresWriteOnThePackagePath: a caller who may deploy to
// the repository but not to the uploaded package's path is refused the
// upload and its finalize step, and nothing is stored.
func TestUnitPublishRequiresWriteOnThePackagePath(t *testing.T) {
	f := newFixture(t, repo.Local)
	f.grant("deployer", accessapi.ActionRead|accessapi.ActionWrite,
		map[string][]string{f.c.Key: {"other_pub/**"}})
	body, contentType := upload(t, archive(t, "widget_pub", "1.0.0"))
	rec := f.do(http.MethodPost, base+"/api/packages/versions/new", "deployer", contentType, body)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("upload outside the deployer's paths: %d %s", rec.Code, rec.Body)
	}
	rec = f.do(http.MethodGet, base+"/api/packages/versions/new/complete?name=widget_pub"+
		"&version=1.0.0&sha256="+strings.Repeat("a", 64), "deployer", "", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("finalize outside the deployer's paths: %d %s", rec.Code, rec.Body)
	}
	if _, err := f.db.Items().Get(t.Context(), f.c.Key, false,
		"widget_pub/widget_pub-1.0.0.tar.gz"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("refused upload stored: %v", err)
	}
}

// TestUnitRemoteMetadataValidatorAndCachedDocument: the metadata resource
// carries a validator that accepts the upstream document of the package
// asked for and refuses another package's document or an unreadable one;
// a metadata stream that breaks off is refused the same way; and a document
// the source answers from its cache is read from the blob store.
func TestUnitRemoteMetadataValidatorAndCachedDocument(t *testing.T) {
	allowed := func(context.Context, string) error { return nil }
	const document = `{"name":"visible_pub","versions":[{"version":"1.0.0",` +
		`"archive_url":"https://example.com/pub/packages/visible_pub/versions/1.0.0.tar.gz",` +
		`"pubspec":{"name":"visible_pub","version":"1.0.0"}}]}`
	valid := metadataSource{check: allowed, body: document}
	for _, tc := range []struct {
		name   string
		source checkedSource
	}{
		{"another package's document", checkedSource{
			metadataSource: metadataSource{
				check: allowed,
				body:  strings.Replace(document, `"visible_pub",`, `"other_pub",`, 1),
			},
			validate: true,
		}},
		{"unreadable document", checkedSource{
			metadataSource: valid, validate: true, unreadable: true,
		}},
		{"stream that breaks off", checkedSource{
			metadataSource: valid,
			rewrite: func(result *source.Result) {
				result.Stream.Body = io.NopCloser(failingReadSeeker{})
			},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixtureWith(t, repo.Remote, func(p *pkgroute.Protocol) {
				p.Sources[repo.Remote] = tc.source
			})
			rec := f.do(http.MethodGet, base+"/api/packages/visible_pub", "admin", "", nil)
			if status, message := pubError(t, rec); status != http.StatusBadGateway ||
				message != "invalid upstream Dart pub metadata" {
				t.Fatalf("refused document: %d %q", status, message)
			}
		})
	}
	var cached string
	f := newFixtureWith(t, repo.Remote, func(p *pkgroute.Protocol) {
		p.Sources[repo.Remote] = checkedSource{
			metadataSource: valid,
			validate:       true,
			rewrite: func(result *source.Result) {
				_ = result.Stream.Body.Close()
				result.Stream = nil
				result.Item = &model.Item{Actual: kernel.Checksums{SHA256: cached}}
			},
		}
	})
	upload, err := f.blobs.BeginUpload(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := upload.Append(t.Context(), strings.NewReader(document)); err != nil {
		t.Fatal(err)
	}
	stored, err := upload.Commit(t.Context(), blob.Digests{})
	if err != nil {
		t.Fatal(err)
	}
	cached = stored.Digests.SHA256
	if _, listed := f.versionsOf(f.c.Key, "visible_pub", "admin"); !slices.Equal(
		listed, []string{"1.0.0"},
	) {
		t.Fatalf("versions from the cached document: %v", listed)
	}
}

// TestUnitRemoteVersionsFollowTheCallerAndTheArchiveLink: a remote lists only
// the versions whose archives the caller may read; it fetches an archive
// linked on the remote's own host by its upstream path and one linked
// elsewhere by its full URL; and it refuses with 502 an archive of another
// version when the metadata gives no checksum, or one that cannot be read.
func TestUnitRemoteVersionsFollowTheCallerAndTheArchiveLink(t *testing.T) {
	allowed := func(context.Context, string) error { return nil }
	const two = `{"name":"visible_pub","versions":[` +
		`{"version":"1.0.0","archive_url":"https://example.com/pub/packages/visible_pub/versions/1.0.0.tar.gz",` +
		`"pubspec":{"name":"visible_pub","version":"1.0.0"}},` +
		`{"version":"2.0.0","archive_url":"https://cdn.example.net/visible_pub-2.0.0.tar.gz",` +
		`"pubspec":{"name":"visible_pub","version":"2.0.0"}}]}`
	f := newFixtureWith(t, repo.Remote, func(p *pkgroute.Protocol) {
		p.Sources[repo.Remote] = metadataSource{check: allowed, body: two}
	})
	f.grant("partial", accessapi.ActionRead,
		map[string][]string{f.c.Key: {"visible_pub/visible_pub-2.0.0.tar.gz"}})
	if _, listed := f.versionsOf(f.c.Key, "visible_pub", "partial"); !slices.Equal(
		listed, []string{"2.0.0"},
	) {
		t.Fatalf("remote versions listed to a reader of one archive: %v", listed)
	}
	for version, want := range map[string]struct{ upstream, external string }{
		"1.0.0": {upstream: "packages/visible_pub/versions/1.0.0.tar.gz"},
		"2.0.0": {external: "https://cdn.example.net/visible_pub-2.0.0.tar.gz"},
	} {
		var asked []source.Resource
		data := archive(t, "visible_pub", version)
		f := newFixtureWith(t, repo.Remote, func(p *pkgroute.Protocol) {
			p.Sources[repo.Remote] = checkedSource{
				metadataSource: metadataSource{
					check: allowed, body: two, archive: data, archiveSet: true,
				},
				asked: &asked,
			}
		})
		rec := f.do(http.MethodGet, base+"/packages/visible_pub/versions/"+version+".tar.gz",
			"admin", "", nil)
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), data) {
			t.Fatalf("archive %s: %d %s", version, rec.Code, rec.Body)
		}
		last := asked[len(asked)-1]
		if last.Kind != source.ResourceKindArtifact || last.UpstreamPath != want.upstream ||
			last.ExternalURL != want.external {
			t.Fatalf("archive %s requested as %+v", version, last)
		}
	}
	for name, src := range map[string]checkedSource{
		"another version": {metadataSource: metadataSource{
			check: allowed, archive: archive(t, "visible_pub", "2.0.0"), archiveSet: true,
		}},
		"unreadable archive": {
			metadataSource: metadataSource{
				check: allowed, archive: archive(t, "visible_pub", "1.0.0"), archiveSet: true,
			},
			unreadable: true,
		},
	} {
		f := newFixtureWith(t, repo.Remote, func(p *pkgroute.Protocol) {
			p.Sources[repo.Remote] = src
		})
		rec := f.do(http.MethodGet, base+"/packages/visible_pub/versions/1.0.0.tar.gz",
			"admin", "", nil)
		if status, message := pubError(t, rec); status != http.StatusBadGateway ||
			message != "Bad Gateway" {
			t.Errorf("%s: %d %q", name, status, message)
		}
	}
}
