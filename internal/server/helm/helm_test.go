package helm_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/format"
	helmfmt "github.com/weaveplatform/artifactweave/internal/format/helm"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/helm"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const endpoint = "/artifactory/api/helm/charts/"

type fixture struct {
	t     *testing.T
	h     http.Handler
	items *item.Service
	c     repo.Config
}

func newFixture(t *testing.T, relative bool) *fixture {
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
	c := *repo.Defaults(&repo.Config{Key: "charts", Class: repo.Local, PackageType: repo.Helm})
	for _, r := range []repo.Config{c, *repo.Defaults(&repo.Config{Key: "generic", Class: repo.Local, PackageType: repo.Generic}), *repo.Defaults(&repo.Config{Key: "remote", Class: repo.Remote, PackageType: repo.Helm, URL: "https://charts.example"})} {
		if err := db.Repos().Create(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	for user, pattern := range map[string]string{"reader": "**", "partial": "visible/**"} {
		if err := db.Permissions().
			Create(ctx, types.Permission{Name: user, Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {Actions: types.Grants{Users: map[string]types.Actions{user: types.ActionRead}}, Targets: map[string]types.Target{c.Key: {Include: []string{pattern}}}}}}); err != nil {
			t.Fatal(err)
		}
	}
	router := chi.NewRouter()
	router.Use(baseurl.Middleware("https://proxy.example"), func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := r.Header.Get("X-User")
			p := auth.Principal{Name: name, Admin: name == "admin"}
			if name == "anonymous" {
				p = auth.Anonymous
			}
			p, err := auth.Authorize(
				r.Context(),
				authz.Sources{
					Permissions:      db.Permissions(),
					Groups:           db.Groups(),
					AnonymousEnabled: true,
				},
				p,
			)
			if err != nil {
				t.Fatal(err)
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	})
	pkgroute.Mount(
		router,
		pkgroute.Options{
			Store: db,
			Items: func() *item.Service { return items },
			ServeItem: func(w http.ResponseWriter, r *http.Request, _ repo.Config, it types.Item) {
				rc, _, err := blobs.Open(r.Context(), it.Actual.SHA256)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = rc.Close() }()
				w.Header().Set("Content-Type", it.MimeType)
				w.Header().Set("X-Downloaded", it.FullPath())
				if r.Method != http.MethodHead {
					_, _ = io.Copy(w, rc)
				}
			},
		},
		helm.Protocol(helm.Options{Format: helmfmt.Settings{RelativeURLs: &relative}}),
	)
	return &fixture{t: t, h: router, items: items, c: c}
}

func (f *fixture) do(method, path, user string) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("X-User", user)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func (f *fixture) deploy(path string, data []byte) {
	f.t.Helper()
	if _, err := f.items.Deploy(
		context.Background(),
		item.Deploy{Target: item.Target{Repo: f.c, Path: path}, Body: bytes.NewReader(data)},
	); err != nil {
		f.t.Fatal(err)
	}
}

func archive(name, version string) []byte {
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	body := fmt.Sprintf("apiVersion: v2\nname: %s\nversion: %s\n", name, version)
	_ = tw.WriteHeader(&tar.Header{Name: name + "/Chart.yaml", Mode: 0o644, Size: int64(len(body))})
	_, _ = io.WriteString(tw, body)
	_ = tw.Close()
	_ = gz.Close()
	return b.Bytes()
}

func TestRepoAddAndPull(t *testing.T) {
	for _, relative := range []bool{true, false} {
		t.Run(fmt.Sprint(relative), func(t *testing.T) {
			f := newFixture(t, relative)
			data := archive("web", "1.0.0")
			f.deploy("visible/web-1.0.0.tgz", data)
			f.deploy("visible/web-1.0.0.tgz.prov", []byte("signed"))
			f.deploy("hidden/db-2.0.0.tgz", archive("db", "2.0.0"))
			rec := f.do(http.MethodGet, endpoint+"index.yaml", "partial")
			if rec.Code != http.StatusOK ||
				rec.Header().Get("Content-Type") != "application/x-yaml" {
				t.Fatalf("index %d %s", rec.Code, rec.Body)
			}
			var index helmfmt.Index
			if err := yaml.Unmarshal(rec.Body.Bytes(), &index); err != nil {
				t.Fatal(err)
			}
			if len(index.Entries) != 2 {
				t.Fatal(
					"repository metadata must include charts outside the caller's download grant",
				)
			}
			location := index.Entries["web"][0].URLs[0]
			want := "visible/web-1.0.0.tgz"
			if !relative {
				want = "https://proxy.example/artifactory/charts/" + want
			}
			if location != want {
				t.Fatalf("URL %q want %q", location, want)
			}
			for _, suffix := range []string{"", ".prov"} {
				rec = f.do(http.MethodGet, endpoint+"visible/web-1.0.0.tgz"+suffix, "partial")
				if rec.Code != 200 || rec.Header().Get("X-Downloaded") == "" {
					t.Fatalf("download %d %s", rec.Code, rec.Body)
				}
			}
			if rec = f.do(
				http.MethodGet,
				endpoint+"hidden/db-2.0.0.tgz",
				"partial",
			); rec.Code != http.StatusForbidden {
				t.Fatalf("hidden download %d", rec.Code)
			}
			rec = f.do(http.MethodHead, endpoint+"index.yaml", "reader")
			if rec.Code != 200 || rec.Body.Len() != 0 {
				t.Fatalf("HEAD %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestColdIndexAndRefusals(t *testing.T) {
	f := newFixture(t, true)
	rec := f.do(http.MethodGet, endpoint+"index.yaml", "reader")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "entries: {}") {
		t.Fatalf("cold %d %s", rec.Code, rec.Body)
	}
	for _, c := range []struct {
		method, path, user string
		status             int
	}{
		{"GET", endpoint + "index.yaml", "nobody", 403},
		{"GET", endpoint + "index.yaml", "anonymous", 401},
		{"GET", "/artifactory/api/helm/missing/index.yaml", "admin", 404},
		{"GET", "/artifactory/api/helm/generic/index.yaml", "admin", 404},
		{"GET", "/artifactory/api/helm/remote/index.yaml", "admin", 501},
		{"GET", endpoint + "missing.tgz", "admin", 404},
		{"GET", endpoint + "index.yaml;=bad", "admin", 400},
		{"POST", endpoint + "api/charts", "admin", 405},
		{"PUT", endpoint + "index.yaml", "admin", 405},
		{"DELETE", endpoint + "a.tgz", "admin", 405},
	} {
		rec = f.do(c.method, c.path, c.user)
		if rec.Code != c.status {
			t.Errorf("%s %s: %d want %d: %s", c.method, c.path, rec.Code, c.status, rec.Body)
		}
	}
}

func TestIndexFailures(t *testing.T) {
	for _, mode := range []string{"missing-blob", "bad-yaml", "folder", "work-fault"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, false)
			ctx := context.Background()
			if mode == "work-fault" {
				f.deploy("web.tgz", archive("web", "1.0.0"))
				packages, _, err := f.items.Store.Packages().
					List(ctx, f.c.Key, store.Page{Limit: 10})
				if err != nil {
					t.Fatal(err)
				}
				versions, err := f.items.Store.Packages().Versions(ctx, packages[0].ID)
				if err != nil {
					t.Fatal(err)
				}
				versions[0].Metadata = []byte("broken JSON")
				if _, err := f.items.Store.Packages().PutVersion(ctx, versions[0]); err != nil {
					t.Fatal(err)
				}
			} else if mode == "folder" {
				if _, err := f.items.Store.Items().
					Put(ctx, types.Item{RepoKey: f.c.Key, Name: "index.yaml", Folder: true}); err != nil {
					t.Fatal(err)
				}
			} else {
				it, err := f.items.Env().
					Derive(ctx, format.Derived{Repo: f.c, Path: "index.yaml", Body: strings.NewReader("[broken YAML"), By: "_system_"})
				if err != nil {
					t.Fatal(err)
				}
				if mode == "missing-blob" {
					if err := f.items.Blobs.Delete(ctx, it.Actual.SHA256); err != nil {
						t.Fatal(err)
					}
				}
			}
			rec := f.do(http.MethodGet, endpoint+"index.yaml", "reader")
			want := http.StatusInternalServerError
			if mode == "folder" {
				want = http.StatusNotFound
			}
			if rec.Code != want {
				t.Fatalf("%d want%d: %s", rec.Code, want, rec.Body)
			}
		})
	}
}
