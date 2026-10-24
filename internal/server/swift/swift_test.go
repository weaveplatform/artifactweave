package swift_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/server/swift"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	base     = "/artifactory/api/swift/packages/"
	manifest = "// swift-tools-version:5.9\nimport PackageDescription\nlet package = Package(name: \"Widget\", targets: [.target(name: \"Widget\")])\n"
)

type fixture struct {
	t        *testing.T
	r        http.Handler
	db       *inmem.Store
	items    *item.Service
	mu       *sync.Mutex
	requests map[string]int
}

func newFixture(
	t *testing.T,
	class repo.Class,
	configure ...func(*item.Service, *inmem.Store) swift.Options,
) *fixture {
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
	c := *repo.Defaults(&repo.Config{Key: "packages", Class: class, PackageType: repo.Swift})
	if err := db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	counts := map[string]int{}
	countsMu := new(sync.Mutex)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			countsMu.Lock()
			counts[r.Method+" "+r.URL.Path]++
			countsMu.Unlock()
			name := r.Header.Get("X-User")
			if name == "" {
				name, _, _ = r.BasicAuth()
			}
			if os.Getenv("ARTIFACTWEAVE_SWIFT_CLIENT_LOCAL") == "1" {
				t.Logf("client request %s %s authenticated=%t", r.Method, r.URL.Path, name != "")
			}
			principal := auth.Principal{Name: name, Admin: name == "admin"}
			if name == "" {
				principal = auth.Anonymous
			}
			p, authErr := auth.Authorize(
				r.Context(),
				authz.Sources{
					Permissions:      db.Permissions(),
					Groups:           db.Groups(),
					AnonymousEnabled: true,
				},
				principal,
			)
			if authErr != nil {
				t.Fatal(authErr)
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	})
	var protocol swift.Options
	if len(configure) != 0 {
		protocol = configure[0](items, db)
	}
	pkgroute.Mount(r, pkgroute.Options{
		Store: db, Items: func() *item.Service { return items },
		ItemError: func(_ context.Context, _ string, err error) error {
			var rejected *format.Rejection
			if errors.As(err, &rejected) {
				return apierror.New(rejected.Status, rejected.Message)
			}
			return err
		},
		ServeItem: func(w http.ResponseWriter, r *http.Request, _ repo.Config, it types.Item) {
			rc, _, openErr := blobs.Open(r.Context(), it.Actual.SHA256)
			if openErr != nil {
				t.Fatal(openErr)
			}
			defer rc.Close()
			if r.Method != http.MethodHead {
				_, _ = io.Copy(w, rc)
			}
		},
	}, swift.Protocol(protocol))
	return &fixture{t: t, r: r, db: db, items: items, requests: counts, mu: countsMu}
}

func (f *fixture) count(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[method+" "+path]
}

func (f *fixture) do(
	method, path string,
	body io.Reader,
	contentType, user string,
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

func archive(t *testing.T, root string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, content := range map[string]string{
		root + "Package.swift":               manifest,
		root + "Sources/Widget/Widget.swift": "public struct Widget { public init() {} }",
	} {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func form(t *testing.T, archive []byte) (*bytes.Buffer, string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	p, err := w.CreateFormFile("source-archive", "Widget-1.0.0.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write(archive); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteField("metadata", `{"description":"native package"}`); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &b, w.FormDataContentType()
}

func TestUnitNativeSwiftPublishAndRead(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) {
			f := newFixture(t, class)
			zipBytes := archive(t, "Widget/") // SwiftPM archive-source layout.
			body, contentType := form(t, zipBytes)
			url := base + "acme/Widget/1.0.0"
			if rec := f.do(
				http.MethodPut,
				url,
				body,
				contentType,
				"admin",
			); rec.Code != http.StatusCreated ||
				!strings.HasSuffix(rec.Header().Get("Location"), url) {
				t.Fatalf(
					"publish: %d %s; location %s",
					rec.Code,
					rec.Body,
					rec.Header().Get("Location"),
				)
			}
			if rec := f.do(
				http.MethodPut,
				url,
				bytes.NewReader(zipBytes),
				"application/zip",
				"admin",
			); rec.Code != http.StatusUnsupportedMediaType {
				t.Fatalf("publish content type: %d %s", rec.Code, rec.Body)
			}
			body, contentType = form(t, zipBytes)
			if rec := f.do(
				http.MethodPut,
				url,
				body,
				contentType,
				"admin",
			); rec.Code != http.StatusConflict {
				t.Fatalf("immutable duplicate: %d %s", rec.Code, rec.Body)
			}
			for _, tc := range []struct{ suffix, contains string }{
				{"", `"checksum":"`},
				{"/Package.swift", "import PackageDescription"},
				{".zip", "PK"},
			} {
				rec := f.do(http.MethodGet, url+tc.suffix, nil, "", "admin")
				if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tc.contains) ||
					rec.Header().Get("Content-Version") != "1" {
					t.Fatalf("read %s: %d %s", tc.suffix, rec.Code, rec.Body)
				}
			}
			archiveRead := f.do(http.MethodGet, url+".zip", nil, "", "admin")
			if !bytes.Equal(archiveRead.Body.Bytes(), zipBytes) {
				t.Fatalf(
					"archive response differs from published source: status %d, got %d bytes want %d",
					archiveRead.Code,
					archiveRead.Body.Len(),
					len(zipBytes),
				)
			}
			rec := f.do(http.MethodGet, base+"acme/Widget", nil, "", "admin")
			var list struct {
				Releases map[string]any `json:"releases"`
			}
			if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil ||
				len(list.Releases) != 1 {
				t.Fatalf("releases: %d %s", rec.Code, rec.Body)
			}
			if rec := f.do(
				http.MethodHead,
				url+".zip",
				nil,
				"",
				"admin",
			); rec.Code != http.StatusOK ||
				rec.Body.Len() != 0 {
				t.Fatalf("archive HEAD: %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestUnitSwiftRejectsMalformedArchiveAndIdentity(t *testing.T) {
	f := newFixture(t, repo.Local)
	for _, tc := range []struct {
		path   string
		status int
	}{
		{base + "-bad/Widget/1.0.0", 404},
		{base + "acme/Widget/not-semver", 404},
		{base + "acme/Widget/1.0.0?another=1", 405},
	} {
		body, contentType := form(t, archive(t, "Widget/"))
		rec := f.do(http.MethodPut, tc.path, body, contentType, "admin")
		if rec.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.path, rec.Code, rec.Body)
		}
	}
	var bad bytes.Buffer
	z := zip.NewWriter(&bad)
	w, _ := z.Create("../Package.swift")
	_, _ = w.Write([]byte(manifest))
	_ = z.Close()
	body, contentType := form(t, bad.Bytes())
	rec := f.do(http.MethodPut, base+"acme/Widget/1.0.0", body, contentType, "admin")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unsafe archive: %d %s", rec.Code, rec.Body)
	}
	if _, err := f.db.Packages().Find(t.Context(), "packages", "acme.Widget"); err == nil {
		t.Fatal("rejected archive was indexed")
	}
}

func TestUnitSwiftIdentifierLookupRequiresURL(t *testing.T) {
	f := newFixture(t, repo.Local)
	for _, tc := range []struct {
		query  string
		status int
	}{
		{"", http.StatusBadRequest},
		{"?url=", http.StatusBadRequest},
		{"?url=one&url=two", http.StatusBadRequest},
		{"?url=https%3A%2F%2Fexample.com%2FWidget", http.StatusNotFound},
	} {
		rec := f.do(http.MethodGet, base+"identifiers"+tc.query, nil, "", "admin")
		if rec.Code != tc.status {
			t.Fatalf("identifier lookup %q: %d %s", tc.query, rec.Code, rec.Body)
		}
	}
}

func TestUnitSwiftDeleteRestoreAndReindex(t *testing.T) {
	f := newFixture(t, repo.Local)
	url := base + "acme/Widget/1.0.0"
	body, contentType := form(t, archive(t, "Widget/"))
	if rec := f.do(
		http.MethodPut,
		url,
		body,
		contentType,
		"admin",
	); rec.Code != http.StatusCreated {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body)
	}
	c, err := f.db.Repos().Get(t.Context(), "packages")
	if err != nil {
		t.Fatal(err)
	}
	path := "acme/Widget/Widget-1.0.0.zip"
	if _, err := f.items.Delete(
		t.Context(),
		item.Target{Repo: c, Path: path},
		"admin",
	); err != nil {
		t.Fatal(err)
	}
	if rec := f.do(
		http.MethodGet,
		base+"acme/Widget",
		nil,
		"",
		"admin",
	); rec.Code != http.StatusNotFound {
		t.Fatalf("deleted version still listed: %d %s", rec.Code, rec.Body)
	}
	entries, _, err := f.db.Trash().List(t.Context(), "packages", "", store.Page{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if entry.Record.Item.FullPath() != path {
			continue
		}
		found = true
		if _, err := f.items.Restore(
			t.Context(),
			item.Target{Repo: c, Path: path},
			entry.Record,
			"admin",
		); err != nil {
			t.Fatal(err)
		}
	}
	if !found {
		t.Fatal("deleted Swift archive missing from trash")
	}
	if err := f.items.Reindex(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if rec := f.do(
		http.MethodGet,
		base+"acme/Widget",
		nil,
		"",
		"admin",
	); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"1.0.0"`) {
		t.Fatalf("restored version absent after reindex: %d %s", rec.Code, rec.Body)
	}
}

// The CI acceptance lane supplies a digest-pinned SwiftPM image. Developers
// can run the installed SwiftPM client on macOS with the local opt-in.
func TestAccSwiftPMAnonymousPublish(t *testing.T) {
	if !swiftClientEnabled(t) {
		return
	}
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) { accSwiftPMPublish(t, class) })
	}
}

func swiftClientEnabled(t *testing.T) bool {
	t.Helper()
	if os.Getenv("ARTIFACTWEAVE_SWIFT_CLIENT_LOCAL") != "1" &&
		os.Getenv("ARTIFACTWEAVE_SWIFT_IMAGE") == "" {
		t.Skip("SwiftPM client acceptance is enabled by the pinned CI image or local opt-in")
	}
	if os.Getenv("ARTIFACTWEAVE_SWIFT_IMAGE") == "" {
		if _, err := exec.LookPath("swift"); err != nil {
			t.Skip("swift is not installed")
		}
	}
	return true
}

func runSwift(t *testing.T, root string, args ...string) {
	t.Helper()
	var cmd *exec.Cmd
	if image := os.Getenv("ARTIFACTWEAVE_SWIFT_IMAGE"); image != "" {
		argv := []string{
			"run",
			"--rm",
			"--network",
			"host",
			"--user",
			strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()),
			"-v",
			root + ":/work",
			"-w",
			"/work",
			"-e",
			"HOME=/work",
			image,
			"swift",
		}
		cmd = exec.CommandContext(t.Context(), "docker", append(argv, args...)...)
	} else {
		cmd = exec.CommandContext(t.Context(), "swift", args...)
		cmd.Env = append(os.Environ(), "HOME="+root)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("SwiftPM %v: %v\n%s", args, err, out)
	}
}

func swiftPath(root, path string) string {
	if os.Getenv("ARTIFACTWEAVE_SWIFT_IMAGE") == "" {
		return path
	}
	return "/work/" + strings.TrimPrefix(path, root+"/")
}

func accSwiftPMPublish(t *testing.T, class repo.Class) {
	f := newFixture(t, class)
	if err := f.db.Permissions().
		Create(t.Context(), types.Permission{Name: "swiftpm-acceptance", Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {Actions: types.Grants{Users: map[string]types.Actions{
				"anonymous": types.ActionRead | types.ActionWrite,
			}}, Targets: map[string]types.Target{"packages": {Include: []string{"**"}}}},
		}}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(f.r)
	defer srv.Close()
	root := t.TempDir()
	pkg := filepath.Join(root, "Widget")
	if err := os.MkdirAll(filepath.Join(pkg, "Sources", "Widget"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(pkg, "Package.swift"),
		[]byte(manifest),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(pkg, "Sources", "Widget", "Widget.swift"),
		[]byte("public struct Widget { public init() {} }\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	url := srv.URL + "/artifactory/api/swift/packages"
	version := fmt.Sprintf("1.0.%d", time.Now().UnixMilli()%1_000_000_000_000)
	runSwift(t, root, "package-registry", "--package-path", swiftPath(root, pkg),
		"publish", "acme.Widget", version, "--url", url, "--allow-insecure-http")
	if rec := f.do(http.MethodGet, base+"acme/Widget/"+version, nil, "", "admin"); rec.Code != 200 {
		t.Fatalf("SwiftPM publication missing: %d %s", rec.Code, rec.Body)
	}
	consumer := filepath.Join(root, "Consumer")
	if err := os.MkdirAll(filepath.Join(consumer, "Sources", "Consumer"), 0o700); err != nil {
		t.Fatal(err)
	}
	consumerManifest := fmt.Sprintf(`// swift-tools-version:6.0
import PackageDescription
let package = Package(name: "Consumer", dependencies: [.package(id: "acme.Widget", from: "%s")], targets: [.executableTarget(name: "Consumer", dependencies: [.product(name: "Widget", package: "acme.Widget")])])
`, version)
	if err := os.WriteFile(
		filepath.Join(consumer, "Package.swift"),
		[]byte(consumerManifest),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(consumer, "Sources", "Consumer", "main.swift"),
		[]byte("import Widget\nlet _ = Widget()\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	runSwift(t, root, "package-registry", "--package-path", swiftPath(root, consumer),
		"set", "--allow-insecure-http", url)
	runSwift(t, root, "package", "--package-path", swiftPath(root, consumer),
		"--cache-path", swiftPath(root, filepath.Join(root, "client-cache")), "resolve")
	if f.count(http.MethodGet, base+"acme/Widget/"+version+".zip") == 0 {
		t.Fatal("SwiftPM did not download the source archive")
	}
}
