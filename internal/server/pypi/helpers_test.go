package pypi_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/format/formats"
	pypifmt "github.com/weaveplatform/artifactweave/internal/format/pypi"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/server/pypi"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var update = flag.Bool("update", false, "rewrite the golden pages")

const (
	api  = "/artifactory/api/pypi/pypi-local/"
	base = "https://pypi.example"
	// pipAccept and uvAccept are what pip 26.2.1 and uv 0.13.1 ask for an
	// index page with (research 28 section 1).
	pipAccept = "application/vnd.pypi.simple.v1+json, " +
		"application/vnd.pypi.simple.v1+html; q=0.1, text/html; q=0.01"
	uvAccept = "application/vnd.pypi.simple.v1+json, " +
		"application/vnd.pypi.simple.v1+html;q=0.2, text/html;q=0.01"
)

// fixture is the PyPI protocol mounted as the server mounts it, over a
// store whose pypi-local holds what each test deploys. A request names
// its principal in X-Test-User: none, anonymous, admin, or a user whose
// grants the fixture gives:
//   - reader reads all of pypi-local;
//   - partial reads only the files under demo/1.0/;
//   - writer reads, deploys and deletes all of pypi-local;
//   - deployer reads and deploys all of pypi-local, and may not delete;
//   - nobody holds nothing, and neither does anonymous.
type fixture struct {
	h      http.Handler
	db     *inmem.Store
	items  *item.Service
	tick   time.Time
	faults []error
	opts   pkgroute.Options
	// served are the files the download hands to the item routes.
	served []string
}

func newFixture(
	t *testing.T,
	settings pypifmt.Settings,
	wrap ...func(store.Store) store.Store,
) *fixture {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	for _, c := range []repo.Config{
		{Key: "pypi-local", Class: repo.Local, PackageType: repo.PyPI},
		{Key: "pypi-fed", Class: repo.Federated, PackageType: repo.PyPI},
		{Key: "pypi-remote", Class: repo.Remote, PackageType: repo.PyPI, URL: "https://up.example"},
		{Key: "libs-local", Class: repo.Local},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	grant := func(user, include string, actions types.Actions) {
		t.Helper()
		if err := db.Permissions().Create(ctx, types.Permission{
			Name: user,
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{user: actions}},
				Targets: map[string]types.Target{
					"pypi-local": {Include: []string{include}},
					"pypi-fed":   {Include: []string{include}},
				},
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	read, write, del := types.ActionRead, types.ActionWrite, types.ActionDelete
	grant("reader", "**", read)
	grant("partial", "demo/1.0/**", read)
	grant("writer", "**", read|write|del)
	grant("deployer", "**", read|write)
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{db: db, tick: time.Date(2026, 10, 1, 9, 0, 0, 123456000, time.UTC)}
	f.items = &item.Service{
		Store: db, Blobs: blobs, Now: func() time.Time { return f.tick },
		Formats: formats.Settings{PyPI: settings}.For,
	}
	var backend store.Store = db
	for _, w := range wrap {
		backend = w(backend)
	}
	f.opts = pkgroute.Options{
		Store: backend,
		Items: func() *item.Service { return f.items },
		ServeItem: func(w http.ResponseWriter, _ *http.Request, c repo.Config, it types.Item) {
			f.served = append(f.served, c.Key+"/"+it.FullPath())
			_, _ = io.WriteString(w, "served "+c.Key+"/"+it.FullPath())
		},
		ItemError: uploadItemError,
		Fault:     func(_ *http.Request, err error) { f.faults = append(f.faults, err) },
	}
	r := chi.NewRouter()
	r.Use(baseurl.Middleware(base))
	r.Use(principal(t, db))
	pkgroute.Mount(r, f.opts, pypi.Protocol(pypi.Options{Format: settings}))
	f.h = r
	return f
}

// principal authorises the user X-Test-User names, as the server's
// middleware does.
func principal(t *testing.T, db *inmem.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := r.Header.Get("X-Test-User")
			if name == "" {
				next.ServeHTTP(w, r)
				return
			}
			p := auth.Principal{Name: name, Admin: name == "admin"}
			if name == auth.AnonymousUser {
				p = auth.Anonymous
			}
			p, err := auth.Authorize(r.Context(), authz.Sources{
				Permissions: db.Permissions(), Groups: db.Groups(), AnonymousEnabled: true,
			}, p)
			if err != nil {
				t.Error(err)
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	}
}

// deploy stores body at path in the repository key, a minute after the
// last deploy, with props.
func (f *fixture) deploy(
	t *testing.T,
	key, path string,
	body []byte,
	props types.Properties,
) types.Item {
	t.Helper()
	ctx := context.Background()
	c, err := f.db.Repos().Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	f.tick = f.tick.Add(time.Minute)
	it, err := f.items.Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: c, Path: path}, Body: bytes.NewReader(body), Properties: props,
	})
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func (f *fixture) do(method, path, user string, headers ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	if user != "" {
		r.Header.Set("X-Test-User", user)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	return rec
}

// metadata is a core metadata file for name at version, with extra header
// lines.
func metadata(name, version, extra string) string {
	return "Metadata-Version: 2.1\nName: " + name + "\nVersion: " + version + "\n" + extra +
		"\nThe description.\n"
}

// wheel is a wheel whose METADATA is meta, in a .dist-info folder named
// for name and version.
func wheel(t *testing.T, name, version, meta string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// In a fixed order, so a wheel's bytes, and the pages' hashes of them,
	// are the same on every run.
	for _, e := range [][2]string{
		{"pkg/__init__.py", ""}, {name + "-" + version + ".dist-info/METADATA", meta},
	} {
		w, err := zw.Create(e[0])
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, e[1])
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// sdist is a .tar.gz source distribution whose PKG-INFO is meta.
func sdist(t *testing.T, name, version, meta string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: name + "-" + version + "/PKG-INFO", Mode: 0o644, Size: int64(len(meta)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(tw, meta)
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status %d, want %d: %s", rec.Code, want, rec.Body)
	}
}

// golden compares got with testdata/name, or rewrites it with -update.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(p, got, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("%s (-want +got):\n%s", name, diff)
	}
}
