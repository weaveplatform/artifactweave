package luarocks_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/weaveplatform/artifactweave/internal/server/luarocks"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/trash"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type fixture struct {
	t      *testing.T
	router http.Handler
	items  *item.Service
	db     *inmem.Store
	c      repo.Config
}

func newFixture(t *testing.T, class repo.Class) *fixture {
	t.Helper()
	ctx := context.Background()
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
	c := *repo.Defaults(&repo.Config{Key: "rocks", Class: class, PackageType: repo.LuaRocks})
	if err := db.Repos().Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := r.Header.Get("X-User")
			p, err := auth.Authorize(
				r.Context(),
				authz.Sources{
					Permissions:      db.Permissions(),
					Groups:           db.Groups(),
					AnonymousEnabled: true,
				},
				auth.Principal{Name: name, Admin: name == "admin"},
			)
			if err != nil {
				t.Fatal(err)
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	})
	pkgroute.Mount(r, pkgroute.Options{
		Store: db, Items: func() *item.Service { return items },
		ItemError: func(_ context.Context, _ string, err error) error { return err },
		ServeItem: func(w http.ResponseWriter, r *http.Request, _ repo.Config, it types.Item) {
			rc, _, err := blobs.Open(r.Context(), it.Actual.SHA256)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rc.Close() }()
			w.Header().Set("Content-Type", it.MimeType)
			if r.Method != http.MethodHead {
				_, _ = io.Copy(w, rc)
			}
		},
	}, luarocks.Protocol())
	return &fixture{t, r, items, db, c}
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
	f.router.ServeHTTP(w, r)
	return w
}

func spec(name, version string) []byte {
	return []byte(
		fmt.Sprintf(
			"package = %q\nversion = %q\nsource = { url = 'file:///tmp/a' }\nbuild = { type = 'builtin', modules = {} }\n",
			name,
			version,
		),
	)
}

func rock(t *testing.T, name, version string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	f, err := z.Create(name + "-" + version + ".rockspec")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(spec(name, version)); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func form(t *testing.T, field, filename string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	p, err := w.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &b, w.FormDataContentType()
}

func signedForm(
	t *testing.T,
	field, filename string,
	content []byte,
	sigField string,
) (*bytes.Buffer, string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	p, err := w.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write(content); err != nil {
		t.Fatal(err)
	}
	p, err = w.CreateFormFile(sigField, filename+".asc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte("signature")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &b, w.FormDataContentType()
}

func TestNativeUploadAndInstallManifest(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) {
			f := newFixture(t, class)
			base := "/artifactory/api/luarocks/rocks/"
			if rec := f.do(
				"GET",
				base+"api/tool_version",
				nil,
				"",
				"admin",
			); rec.Code != 200 ||
				!strings.Contains(rec.Body.String(), `"version":"1.0.0"`) {
				t.Fatalf("tool version %d %s", rec.Code, rec.Body)
			}
			if rec := f.do(
				"GET",
				base+"api/1/secret/check_rockspec?package=hello&version=1.0-1",
				nil,
				"",
				"admin",
			); rec.Code != 200 ||
				strings.Contains(rec.Body.String(), `"version"`) {
				t.Fatalf("new rockspec check %d %s", rec.Code, rec.Body)
			}
			body, ct := signedForm(
				t,
				"rockspec_file",
				"hello-1.0-1.rockspec",
				spec("hello", "1.0-1"),
				"rockspec_sig",
			)
			rec := f.do("POST", base+"api/1/secret/upload", body, ct, "admin")
			if rec.Code != 200 {
				t.Fatalf("upload rockspec: %d %s", rec.Code, rec.Body)
			}
			var result struct {
				Version struct {
					ID int64 `json:"id"`
				} `json:"version"`
			}
			if err := json.Unmarshal(
				rec.Body.Bytes(),
				&result,
			); err != nil ||
				result.Version.ID == 0 {
				t.Fatalf("upload response: %v %s", err, rec.Body)
			}
			if rec := f.do(
				"GET",
				base+"api/1/secret/check_rockspec?package=hello&version=1.0-1",
				nil,
				"",
				"admin",
			); rec.Code != 200 ||
				!strings.Contains(rec.Body.String(), `"version"`) {
				t.Fatalf("existing rockspec check %d %s", rec.Code, rec.Body)
			}
			if _, err := f.db.Items().
				Get(context.Background(), "rocks", false, "hello-1.0-1.rockspec.asc"); err != nil {
				t.Fatalf("signature missing: %v", err)
			}
			rockBytes := rock(t, "hello", "1.0-1")
			body, ct = form(t, "rock_file", "hello-1.0-1.src.rock", rockBytes)
			rec = f.do(
				"POST",
				fmt.Sprintf("%sapi/1/secret/upload_rock/%d", base, result.Version.ID),
				body,
				ct,
				"admin",
			)
			if rec.Code != 200 {
				t.Fatalf("upload rock: %d %s", rec.Code, rec.Body)
			}
			for _, name := range []string{"manifest", "manifest-5.1", "manifest-5.4"} {
				rec = f.do("GET", base+name, nil, "", "admin")
				if rec.Code != 200 || !strings.Contains(rec.Body.String(), `["hello"]`) ||
					!strings.Contains(rec.Body.String(), `arch = "src"`) {
					t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
				}
			}
			if rec := f.do(
				"HEAD",
				base+"manifest-5.4",
				nil,
				"",
				"admin",
			); rec.Code != 200 ||
				rec.Body.Len() != 0 {
				t.Fatalf("manifest HEAD %d %s", rec.Code, rec.Body)
			}
			rec = f.do("GET", base+"hello-1.0-1.src.rock", nil, "", "admin")
			if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), rockBytes) {
				t.Fatalf("install bytes: %d %d", rec.Code, rec.Body.Len())
			}
			p, err := f.db.Packages().Find(context.Background(), "rocks", "hello")
			if err != nil {
				t.Fatal(err)
			}
			v, err := f.db.Packages().Version(context.Background(), p.ID, "1.0-1")
			if err != nil || v.ID != result.Version.ID {
				t.Fatalf("package index: %+v %v", v, err)
			}
		})
	}
}

func TestNativeRouteRefusalsDoNotEchoUploadKey(t *testing.T) {
	f := newFixture(t, repo.Local)
	base := "/artifactory/api/luarocks/rocks/"
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", base + "api/1/secret/check_rockspec?package=x&version=bad", 400},
		{"POST", base + "api/1/secret/upload_rock/nope", 400},
		{"POST", base + "api/1/secret/upload_rock/123", 404},
		{"PUT", base + "api/1/secret/upload", 405},
		{"GET", base + "api/1/secret/unknown", 404},
		{"GET", base + "missing.src.rock", 404},
	} {
		rec := f.do(tc.method, tc.path, nil, "", "admin")
		if rec.Code != tc.status {
			t.Errorf("%s %s = %d, want %d: %s", tc.method, tc.path, rec.Code, tc.status, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("upload key leaked: %s", rec.Body)
		}
	}
	if rec := f.do(
		"GET",
		base+"api/1/secret/check_rockspec?package=x&version=1.0-1",
		nil,
		"",
		"nobody",
	); rec.Code != 403 {
		t.Fatalf("denied check: %d %s", rec.Code, rec.Body)
	}
	bad, ct := form(t, "rockspec_file", "wrong.txt", spec("hello", "1.0-1"))
	if rec := f.do("POST", base+"api/1/secret/upload", bad, ct, "admin"); rec.Code != 400 {
		t.Fatalf("bad filename: %d %s", rec.Code, rec.Body)
	}
	var invalid bytes.Buffer
	w := multipart.NewWriter(&invalid)
	part, err := w.CreateFormFile("rockspec_file", "hello-1.0-1.rockspec")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(spec("hello", "1.0-1")); err != nil {
		t.Fatal(err)
	}
	part, err = w.CreateFormFile("rockspec_sig", "wrong.asc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("bad signature")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if rec := f.do(
		"POST",
		base+"api/1/secret/upload",
		&invalid,
		w.FormDataContentType(),
		"admin",
	); rec.Code != 400 {
		t.Fatalf("bad signature: %d %s", rec.Code, rec.Body)
	}
	if _, err := f.db.Items().
		Get(context.Background(), "rocks", false, "hello-1.0-1.rockspec"); err == nil {
		t.Fatal("rockspec stored despite invalid signature")
	}
}

func TestBadRocksAreRejectedAndReadDenied(t *testing.T) {
	f := newFixture(t, repo.Local)
	corrupt := rockWithCorruptEntry(t)
	for _, tc := range []struct {
		path string
		body []byte
	}{
		{"nested/hello-1.0-1.rockspec", spec("hello", "1.0-1")},
		{"hello-1.0-1.rockspec", spec("other", "1.0-1")},
		{"hello-1.0-1.src.rock", []byte("not a ZIP")},
		{"hello-1.0-1.src.rock", corrupt},
	} {
		_, err := f.items.Deploy(
			context.Background(),
			item.Deploy{
				Target: item.Target{Repo: f.c, Path: tc.path},
				Body:   bytes.NewReader(tc.body),
			},
		)
		if err == nil {
			t.Errorf("accepted %s", tc.path)
		}
	}
	rec := f.do("GET", "/artifactory/api/luarocks/rocks/manifest", nil, "", "nobody")
	if rec.Code != 403 {
		t.Fatalf("denied manifest: %d %s", rec.Code, rec.Body)
	}
}

func rockWithCorruptEntry(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, entry := range []struct{ name, body string }{
		{"hello-1.0-1.rockspec", string(spec("hello", "1.0-1"))},
		{"payload.lua", "uncorrupted Lua content"},
	} {
		w, err := z.CreateHeader(&zip.FileHeader{Name: entry.name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	data := b.Bytes()
	pos := bytes.Index(data, []byte("uncorrupted Lua content"))
	if pos < 0 {
		t.Fatal("test payload not found")
	}
	data[pos] ^= 1 // central directory still claims the original CRC
	return data
}

func TestManifestFollowsDeleteRestoreCopyAndReindex(t *testing.T) {
	f := newFixture(t, repo.Local)
	ctx := context.Background()
	filename := "hello-1.0-1.rockspec"
	if err := f.db.Settings().
		Put(ctx, types.SettingTrashcan, types.TrashcanSettings{Enabled: true, RetentionDays: 14}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.items.Deploy(
		ctx,
		item.Deploy{
			Target: item.Target{Repo: f.c, Path: filename},
			Body:   bytes.NewReader(spec("hello", "1.0-1")),
		},
	); err != nil {
		t.Fatal(err)
	}
	base := "/artifactory/api/luarocks/rocks/manifest"
	if rec := f.do(
		"GET",
		base,
		nil,
		"",
		"admin",
	); rec.Code != 200 ||
		!strings.Contains(rec.Body.String(), `["hello"]`) {
		t.Fatalf("initial manifest %d %s", rec.Code, rec.Body)
	}
	if _, err := f.items.Delete(ctx, item.Target{Repo: f.c, Path: filename}, "admin"); err != nil {
		t.Fatal(err)
	}
	if rec := f.do(
		"GET",
		base,
		nil,
		"",
		"admin",
	); rec.Code != 200 ||
		strings.Contains(rec.Body.String(), `["hello"]`) {
		t.Fatalf("deleted manifest %d %s", rec.Code, rec.Body)
	}
	tr := &trash.Service{Store: f.db, Items: f.items}
	if _, err := tr.Restore(
		ctx,
		f.c.Key,
		filename,
		trash.Destination{Repo: f.c},
		"admin",
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if rec := f.do(
		"GET",
		base,
		nil,
		"",
		"admin",
	); rec.Code != 200 ||
		!strings.Contains(rec.Body.String(), `["hello"]`) {
		t.Fatalf("restored manifest %d %s", rec.Code, rec.Body)
	}
	copyRepo := *repo.Defaults(&repo.Config{Key: "copy", Class: repo.Local, PackageType: repo.LuaRocks})
	if err := f.db.Repos().Create(ctx, copyRepo); err != nil {
		t.Fatal(err)
	}
	result, err := f.items.Transfer(
		ctx,
		item.Transfer{
			From: item.Target{Repo: f.c, Path: filename},
			To:   item.Target{Repo: copyRepo, Path: filename},
			By:   "admin",
		},
	)
	if err != nil || result.Failed != 0 {
		t.Fatalf("copy: %+v %v", result, err)
	}
	if rec := f.do(
		"GET",
		"/artifactory/api/luarocks/copy/manifest",
		nil,
		"",
		"admin",
	); rec.Code != 200 ||
		!strings.Contains(rec.Body.String(), `["hello"]`) {
		t.Fatalf("copied manifest %d %s", rec.Code, rec.Body)
	}
	if err := f.items.Reindex(ctx, copyRepo); err != nil {
		t.Fatal(err)
	}
	if rec := f.do(
		"GET",
		"/artifactory/api/luarocks/copy/manifest",
		nil,
		"",
		"admin",
	); rec.Code != 200 ||
		!strings.Contains(rec.Body.String(), `["hello"]`) {
		t.Fatalf("reindexed manifest %d %s", rec.Code, rec.Body)
	}
}

func TestLuaVersionManifestsHonorSimpleRockspecConstraints(t *testing.T) {
	f := newFixture(t, repo.Local)
	ctx := context.Background()
	for _, tc := range []struct{ name, requirement string }{
		{"ge", "lua >= 5.3"},
		{"lt", "lua < 5.5"},
		{"gt", "lua > 5.3"},
		{"le", "lua <= 5.2"},
		{"eq", "lua == 5.4"},
		{"ne", "lua ~= 5.1"},
	} {
		body := append(
			spec(tc.name, "1.0-1"),
			[]byte(fmt.Sprintf("dependencies = {%q}\n", tc.requirement))...)
		if _, err := f.items.Deploy(
			ctx,
			item.Deploy{
				Target: item.Target{Repo: f.c, Path: tc.name + "-1.0-1.rockspec"},
				Body:   bytes.NewReader(body),
			},
		); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		version         string
		present, absent []string
	}{
		{"5.1", []string{"lt", "le"}, []string{"ge", "gt", "eq", "ne"}},
		{"5.2", []string{"lt", "le", "ne"}, []string{"ge", "gt", "eq"}},
		{"5.4", []string{"ge", "lt", "gt", "eq", "ne"}, []string{"le"}},
		{"5.5", []string{"ge", "gt", "ne"}, []string{"lt", "le", "eq"}},
	} {
		rec := f.do("GET", "/artifactory/api/luarocks/rocks/manifest-"+tc.version, nil, "", "admin")
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", tc.version, rec.Code, rec.Body)
		}
		for _, name := range tc.present {
			if !strings.Contains(rec.Body.String(), `["`+name+`"]`) {
				t.Errorf("%s missing %s: %s", tc.version, name, rec.Body)
			}
		}
		for _, name := range tc.absent {
			if strings.Contains(rec.Body.String(), `["`+name+`"]`) {
				t.Errorf("%s includes %s: %s", tc.version, name, rec.Body)
			}
		}
	}
}
