package bower

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
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
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
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

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	items := &item.Service{Store: db, Blobs: blobs}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := r.Header.Get("X-User")
			p, err := auth.Authorize(r.Context(), authz.Sources{
				Permissions: db.Permissions(), Groups: db.Groups(), AnonymousEnabled: true,
			}, auth.Principal{Name: user, Admin: user == "admin"})
			if err != nil {
				t.Fatal(err)
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	})
	options := pkgroute.Options{
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
			if r.Method != http.MethodHead {
				_, _ = io.Copy(w, rc)
			}
		},
	}
	pkgroute.Mount(r, options, Protocol(Options{}))
	return &fixture{t, r, db, items}
}

func (f *fixture) repo(key string, class repo.Class, members ...string) repo.Config {
	f.t.Helper()
	c := *repo.Defaults(&repo.Config{Key: key, Class: class, PackageType: repo.Bower, Repositories: members})
	if err := f.db.Repos().Create(f.t.Context(), c); err != nil {
		f.t.Fatal(err)
	}
	return c
}

func (f *fixture) deploy(c repo.Config, name, version, file, body string) {
	f.t.Helper()
	path := name + "/" + version + "/" + file
	if _, err := f.items.Deploy(f.t.Context(), item.Deploy{
		Target: item.Target{Repo: c, Path: path}, Body: strings.NewReader(body), By: "admin",
	}); err != nil {
		f.t.Fatalf("deploy %s: %v", path, err)
	}
}

func (f *fixture) get(path, user string) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("X-User", user)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}

func TestUnitBowerLocalFederatedAndVirtualResolver(t *testing.T) {
	f := newFixture(t)
	l := f.repo("bower-local", repo.Local)
	fed := f.repo("bower-fed", repo.Federated)
	_ = f.repo("bower-virtual", repo.Virtual, l.Key, fed.Key)
	for _, c := range []repo.Config{l, fed} {
		f.deploy(c, "hello", "1.0.0", "bower.json", `{"name":"hello","version":"1.0.0"}`)
		f.deploy(c, "hello", "1.0.0", "lib/index.js", "from-"+c.Key)
	}
	base := "/artifactory/api/bower/"
	for _, key := range []string{l.Key, fed.Key, "bower-virtual"} {
		p := base + key + "/"
		if rec := f.get(p+"packages/hello", "admin"); rec.Code != 200 ||
			!strings.Contains(rec.Body.String(), `"url":"art://`+key+`/hello"`) {
			t.Fatalf("%s lookup: %d %s", key, rec.Code, rec.Body)
		}
		if rec := f.get(p+"refs/"+key+"/hello", "admin"); rec.Code != 200 ||
			!refRE.MatchString(strings.TrimSpace(rec.Body.String())) {
			t.Fatalf("%s refs: %d %s", key, rec.Code, rec.Body)
		}
		rec := f.get(p+"binaries/"+key+"/hello.git/v1.0.0", "admin")
		if rec.Code != 200 {
			t.Fatalf("%s archive: %d %s", key, rec.Code, rec.Body)
		}
		if err := validateArchive(
			"hello",
			"1.0.0",
		)(
			t.Context(),
			bytes.NewReader(rec.Body.Bytes()),
			int64(rec.Body.Len()),
		); err != nil {
			t.Fatalf("%s archive invalid: %v", key, err)
		}
		if key == "bower-virtual" {
			gz, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			tr := tar.NewReader(gz)
			var content string
			for {
				h, err := tr.Next()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if strings.HasSuffix(h.Name, "index.js") {
					b, _ := io.ReadAll(tr)
					content = string(b)
				}
			}
			if content != "from-bower-local" {
				t.Fatalf("virtual order: %q", content)
			}
		}
	}
	if rec := f.get(base+l.Key+"/packages/hello", "guest"); rec.Code != http.StatusForbidden {
		t.Fatalf("unauthorized lookup: %d %s", rec.Code, rec.Body)
	}
	if rec := f.get(base+l.Key+"/refs/"+l.Key+"/hello", "guest"); rec.Code == http.StatusOK {
		t.Fatal("unauthorized refs disclosed")
	}
}

func TestUnitBowerRemoteRefsAndArchiveValidation(t *testing.T) {
	for _, body := range []string{
		"", "bad refs",
		strings.Repeat("0", 44) + " refs/tags/v../secret\n",
	} {
		if _, err := parseRefs([]byte(body)); err == nil {
			t.Errorf("accepted invalid refs %q", body)
		}
	}
	if _, err := parseRefs([]byte(strings.Repeat("0", 44) + " refs/tags/v1.0.0\n")); err != nil {
		t.Fatal(err)
	}
	refs, err := parseRefs([]byte(strings.Repeat("0", 40) + " refs/heads/main\n" +
		strings.Repeat("1", 44) + " refs/tags/1.0.0\n" +
		strings.Repeat("1", 44) + " refs/tags/1.0.0^{}\n"))
	if err != nil || len(refs) != 1 || refs[0].Version != "1.0.0" || refs[0].Tag != "1.0.0" {
		t.Fatalf("non-version refs and plain tag: %+v, %v", refs, err)
	}
}
