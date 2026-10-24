package cargo_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
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
	"github.com/weaveplatform/artifactweave/internal/format/formats"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	cargoserver "github.com/weaveplatform/artifactweave/internal/server/cargo"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
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
}

func newFixture(
	t *testing.T,
	class repo.Class,
	configure ...func(*item.Service) *source.Service,
) *fixture {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := *repo.Defaults(&repo.Config{Key: "cargo-test", Class: class, PackageType: repo.Cargo})
	if err := db.Repos().Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	items := &item.Service{Store: db, Blobs: blobs, Formats: formats.For}
	var remoteSources *source.Service
	if len(configure) > 0 {
		remoteSources = configure[0](items)
	}
	r := chi.NewRouter()
	r.Use(baseurl.Middleware("http://registry.test"))
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
		ItemError: func(_ context.Context, _ string, err error) error {
			var refusal *format.Rejection
			if errors.As(err, &refusal) {
				return apierror.New(refusal.Status, refusal.Message)
			}
			return err
		},
		ServeItem: func(w http.ResponseWriter, r *http.Request, _ repo.Config, it types.Item) {
			rc, _, err := blobs.Open(r.Context(), it.Actual.SHA256)
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Close()
			if r.Method != http.MethodHead {
				_, _ = io.Copy(w, rc)
			}
		},
	}, cargoserver.Protocol(remoteSources))
	return &fixture{t, r, db, items}
}

func (f *fixture) do(method, path, user string, body []byte) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set("X-User", user)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}

func archive(t *testing.T, name, version string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	tw := tar.NewWriter(z)
	manifest := []byte(
		"[package]\nname = \"" + name + "\"\nversion = \"" + version + "\"\nedition = \"2021\"\n",
	)
	if err := tw.WriteHeader(
		&tar.Header{
			Name: name + "-" + version + "/Cargo.toml",
			Mode: 0o644,
			Size: int64(len(manifest)),
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(manifest); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func publishBody(t *testing.T, name, version string, crate []byte) []byte {
	t.Helper()
	metadata, err := json.Marshal(
		map[string]any{
			"name":     name,
			"vers":     version,
			"deps":     []any{},
			"features": map[string]any{},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := binary.Write(&b, binary.LittleEndian, uint32(len(metadata))); err != nil {
		t.Fatal(err)
	}
	b.Write(metadata)
	if err := binary.Write(&b, binary.LittleEndian, uint32(len(crate))); err != nil {
		t.Fatal(err)
	}
	b.Write(crate)
	return b.Bytes()
}

func framed(metadata, crate []byte) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(metadata)))
	b.Write(metadata)
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(crate)))
	b.Write(crate)
	return b.Bytes()
}

func TestAccPublishMetadataAndFrameValidation(t *testing.T) {
	f := newFixture(t, repo.Local)
	base := "/artifactory/api/cargo/cargo-test/"
	crate := archive(t, "hello_cargo", "1.0.0")
	good := []byte(
		`{"name":"hello_cargo","vers":"1.0.0","features":{"default":["std"]},"deps":[{"name":"real_dep","version_req":"^1","explicit_name_in_toml":"alias_dep","default_features":true}]}`,
	)
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"empty", nil},
		{"zero metadata", framed(nil, crate)},
		{"truncated metadata", []byte{8, 0, 0, 0, '{'}},
		{"malformed metadata", framed([]byte("not json"), crate)},
		{"missing crate length", append([]byte{2, 0, 0, 0}, []byte("{}")...)},
		{"zero crate", framed(good, nil)},
		{"truncated crate", framed(good, crate)[:len(framed(good, crate))-1]},
		{"trailing bytes", append(framed(good, crate), 'x')},
		{"bad dependency", framed([]byte(`{"name":"hello_cargo","vers":"1.0.0","deps":[{"name":"bad/path","version_req":"^1"}]}`), crate)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := f.do(http.MethodPut, base+"api/v1/crates/new", "admin", tc.body)
			if got.Code != http.StatusBadRequest {
				t.Fatalf("%d %s", got.Code, got.Body.String())
			}
		})
	}
	got := f.do(http.MethodPut, base+"api/v1/crates/new", "admin", framed(good, crate))
	if got.Code != 200 {
		t.Fatalf("publish: %d %s", got.Code, got.Body.String())
	}
	index := f.do(http.MethodGet, base+"index/he/ll/hello_cargo", "admin", nil)
	var record struct {
		Deps []struct {
			Name    string  `json:"name"`
			Package *string `json:"package"`
			Req     string  `json:"req"`
			Kind    string  `json:"kind"`
		} `json:"deps"`
	}
	if index.Code != 200 || json.Unmarshal(index.Body.Bytes(), &record) != nil ||
		len(record.Deps) != 1 ||
		record.Deps[0].Name != "alias_dep" ||
		record.Deps[0].Package == nil ||
		*record.Deps[0].Package != "real_dep" ||
		record.Deps[0].Req != "^1" ||
		record.Deps[0].Kind != "normal" {
		t.Fatalf("translated index: %d %s", index.Code, index.Body.String())
	}
}

func TestAccProtocolValidationAndReadOnlyClasses(t *testing.T) {
	local := newFixture(t, repo.Local)
	base := "/artifactory/api/cargo/cargo-test/"
	c, err := local.db.Repos().Get(t.Context(), "cargo-test")
	if err != nil {
		t.Fatal(err)
	}
	allowed := true
	c.CargoAnonymousAccess = &allowed
	if err := local.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if got := local.do(
		http.MethodGet,
		base+"index/config.json",
		"admin",
		nil,
	); got.Code != 200 ||
		!strings.Contains(got.Body.String(), `"auth-required":false`) {
		t.Fatalf("anonymous config: %d %s", got.Code, got.Body.String())
	}
	for _, path := range []string{"index/he/ll/hello_cargo", "api/v1/crates/hello_cargo"} {
		if got := local.do(
			http.MethodGet,
			base+path,
			"admin",
			nil,
		); got.Code != 404 ||
			!strings.Contains(got.Body.String(), "Cargo crate not found") {
			t.Fatalf("absent %s: %d %s", path, got.Code, got.Body.String())
		}
	}
	if got := local.do(
		http.MethodGet,
		base+"api/v1/crates/bad/name",
		"admin",
		nil,
	); got.Code != 404 {
		t.Fatalf("invalid route: %d", got.Code)
	}
	if got := local.do(http.MethodGet, base+"api/v1/crates/bad!", "admin", nil); got.Code != 404 {
		t.Fatalf("invalid crate name: %d %s", got.Code, got.Body.String())
	}
	remote := newFixture(
		t,
		repo.Remote,
		func(*item.Service) *source.Service { return &source.Service{} },
	)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPut, "api/v1/crates/new"},
		{http.MethodDelete, "api/v1/crates/hello_cargo/1.0.0/yank"},
		{http.MethodPut, "api/v1/crates/hello_cargo/1.0.0/unyank"},
		{http.MethodGet, "api/v1/crates/hello_cargo"},
	} {
		if got := remote.do(
			tc.method,
			base+tc.path,
			"admin",
			nil,
		); got.Code != http.StatusMethodNotAllowed {
			t.Fatalf("remote %s %s: %d %s", tc.method, tc.path, got.Code, got.Body.String())
		}
	}
}

func TestAccStoredIndexIntegrityAndPermissions(t *testing.T) {
	f := newFixture(t, repo.Local)
	base := "/artifactory/api/cargo/cargo-test/"
	crate := archive(t, "hello_cargo", "1.0.0")
	if got := f.do(
		http.MethodPut,
		base+"api/v1/crates/new",
		"admin",
		publishBody(t, "hello_cargo", "1.0.0", crate),
	); got.Code != 200 {
		t.Fatalf("publish: %d %s", got.Code, got.Body.String())
	}
	indexPath := base + "index/he/ll/hello_cargo"
	if got := f.do(
		http.MethodGet,
		base+"api/v1/crates/hello_cargo",
		"reader",
		nil,
	); got.Code != 403 {
		t.Fatalf("denied versions: %d %s", got.Code, got.Body.String())
	}
	if got := f.do(http.MethodGet, base+"index/config.json", "reader", nil); got.Code != 403 {
		t.Fatalf("denied config: %d %s", got.Code, got.Body.String())
	}
	if got := f.do(http.MethodGet, indexPath, "reader", nil); got.Code != 403 {
		t.Fatalf("denied index: %d %s", got.Code, got.Body.String())
	}
	if got := f.do(
		http.MethodDelete,
		base+"api/v1/crates/hello_cargo/1.0.0/yank",
		"reader",
		nil,
	); got.Code != 403 {
		t.Fatalf("denied yank: %d %s", got.Code, got.Body.String())
	}
	if got := f.do(
		http.MethodDelete,
		base+"api/v1/crates/hello_cargo/2.0.0/yank",
		"admin",
		nil,
	); got.Code != 404 {
		t.Fatalf("missing yank: %d %s", got.Code, got.Body.String())
	}
	if got := f.do(
		http.MethodDelete,
		base+"api/v1/crates/hello_cargo/invalid/yank",
		"admin",
		nil,
	); got.Code != 400 {
		t.Fatalf("invalid yank: %d %s", got.Code, got.Body.String())
	}
	it, err := f.db.Items().
		Get(t.Context(), "cargo-test", false, "crates/hello_cargo/hello_cargo-1.0.0.crate")
	if err != nil {
		t.Fatal(err)
	}
	props, err := f.db.Items().Properties(t.Context(), it.ID)
	if err != nil {
		t.Fatal(err)
	}
	original := props["cargo.index"][0]
	for _, tc := range []struct {
		name   string
		set    types.Properties
		remove []string
	}{
		{"missing", nil, []string{"cargo.index"}},
		{"malformed", types.Properties{"cargo.index": {"bad JSON"}}, nil},
		{"wrong checksum", types.Properties{"cargo.index": {strings.Replace(original, it.Actual.SHA256, strings.Repeat("a", 64), 1)}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := f.db.Items().
				PatchProperties(t.Context(), it.ID, tc.set, tc.remove, it.Updated); err != nil {
				t.Fatal(err)
			}
			if got := f.do(http.MethodGet, indexPath, "admin", nil); got.Code != 502 {
				t.Fatalf("corrupt index: %d %s", got.Code, got.Body.String())
			}
			if got := f.do(
				http.MethodGet,
				base+"api/v1/crates/hello_cargo",
				"admin",
				nil,
			); got.Code != 502 {
				t.Fatalf("corrupt versions: %d %s", got.Code, got.Body.String())
			}
			if got := f.do(
				http.MethodDelete,
				base+"api/v1/crates/hello_cargo/1.0.0/yank",
				"admin",
				nil,
			); got.Code != 502 {
				t.Fatalf("corrupt yank: %d %s", got.Code, got.Body.String())
			}
			if err := f.db.Items().
				PatchProperties(t.Context(), it.ID, types.Properties{"cargo.index": {original}}, nil, it.Updated); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAccPublishSparseResolveYankAndClasses(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) {
			f := newFixture(t, class)
			base := "/artifactory/api/cargo/cargo-test/"
			content := archive(t, "hello_cargo", "1.0.0")
			if got := f.do(
				http.MethodPut,
				base+"api/v1/crates/new",
				"reader",
				publishBody(t, "hello_cargo", "1.0.0", content),
			); got.Code != http.StatusForbidden {
				t.Fatalf("denied publish: %d %s", got.Code, got.Body.String())
			}
			if got := f.do(
				http.MethodPut,
				base+"api/v1/crates/new",
				"admin",
				publishBody(t, "other", "1.0.0", content),
			); got.Code != http.StatusBadRequest {
				t.Fatalf("mismatch: %d %s", got.Code, got.Body.String())
			}
			if got := f.do(
				http.MethodPut,
				base+"api/v1/crates/new",
				"admin",
				publishBody(t, "hello_cargo", "1.0.0", content),
			); got.Code != http.StatusOK {
				t.Fatalf("publish: %d %s", got.Code, got.Body.String())
			}
			if got := f.do(
				http.MethodPut,
				base+"api/v1/crates/new",
				"admin",
				publishBody(t, "hello_cargo", "1.0.0", content),
			); got.Code != http.StatusConflict {
				t.Fatalf("duplicate version: %d %s", got.Code, got.Body.String())
			}
			cfg := f.do(http.MethodGet, base+"index/config.json", "admin", nil)
			if cfg.Code != 200 || !strings.Contains(cfg.Body.String(), "api/v1/crates") {
				t.Fatalf("config: %d %s", cfg.Code, cfg.Body.String())
			}
			if got := f.do(
				http.MethodHead,
				base+"index/config.json",
				"admin",
				nil,
			); got.Code != 200 ||
				got.Body.Len() != 0 {
				t.Fatalf("HEAD config: %d %s", got.Code, got.Body.String())
			}
			index := f.do(http.MethodGet, base+"index/he/ll/hello_cargo", "admin", nil)
			if index.Code != 200 || !strings.Contains(index.Body.String(), `"vers":"1.0.0"`) {
				t.Fatalf("index: %d %s", index.Code, index.Body.String())
			}
			if got := f.do(
				http.MethodHead,
				base+"index/he/ll/hello_cargo",
				"admin",
				nil,
			); got.Code != 200 ||
				got.Body.Len() != 0 {
				t.Fatalf("HEAD index: %d %s", got.Code, got.Body.String())
			}
			if got := f.do(
				http.MethodGet,
				base+"api/v1/crates/hello_cargo",
				"admin",
				nil,
			); got.Code != 200 ||
				!strings.Contains(got.Body.String(), `"vers":"1.0.0"`) {
				t.Fatalf("versions: %d %s", got.Code, got.Body.String())
			}
			if got := f.do(http.MethodGet, base+"index/wrong", "admin", nil); got.Code != 404 {
				t.Fatalf("invalid sparse path: %d %s", got.Code, got.Body.String())
			}
			download := f.do(
				http.MethodGet,
				base+"api/v1/crates/hello_cargo/1.0.0/download",
				"admin",
				nil,
			)
			if download.Code != 200 || !bytes.Equal(download.Body.Bytes(), content) {
				t.Fatalf("download: %d", download.Code)
			}
			if got := f.do(
				http.MethodHead,
				base+"api/v1/crates/hello_cargo/1.0.0/download",
				"admin",
				nil,
			); got.Code != 200 ||
				got.Body.Len() != 0 {
				t.Fatalf("HEAD download: %d %s", got.Code, got.Body.String())
			}
			if got := f.do(
				http.MethodDelete,
				base+"api/v1/crates/hello_cargo/1.0.0/yank",
				"admin",
				nil,
			); got.Code != 200 {
				t.Fatalf("yank: %d %s", got.Code, got.Body.String())
			}
			index = f.do(http.MethodGet, base+"index/he/ll/hello_cargo", "admin", nil)
			if !strings.Contains(index.Body.String(), `"yanked":true`) {
				t.Fatalf("yanked index: %s", index.Body.String())
			}
			if got := f.do(
				http.MethodPut,
				base+"api/v1/crates/hello_cargo/1.0.0/unyank",
				"admin",
				nil,
			); got.Code != 200 {
				t.Fatalf("unyank: %d", got.Code)
			}
		})
	}
}
