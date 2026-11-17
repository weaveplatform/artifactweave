package bowerhttp

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

type fixture struct {
	t       *testing.T
	router  http.Handler
	db      *inmem.Store
	items   *item.Service
	sources *source.Service
	// faults are the errors the router kit logged as unexpected.
	faults []error
	// logs holds what the router kit logged, its warnings for a virtual
	// repository's failing member among them.
	logs bytes.Buffer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	items := &item.Service{Store: db, Blobs: blobs, Formats: ecosystem.For}
	f := &fixture{t: t, db: db, items: items}
	f.mount(nil)
	return f
}

func (f *fixture) mount(svc *source.Service) {
	f.sources = svc
	r := chi.NewRouter()
	r.Use(accesstest.Over(f.t, f.db).Middleware(f.t, true, func(r *http.Request) kernel.Principal {
		user := r.Header.Get("X-User")
		return kernel.Principal{Name: user, Admin: user == "admin"}
	}))
	options := pkgroute.Options{
		Store: f.db, Items: func() *item.Service { return f.items },
		Logger: slog.New(slog.NewTextHandler(&f.logs, nil)),
		Fault:  func(_ *http.Request, err error) { f.faults = append(f.faults, err) },
		ItemError: func(_ context.Context, _ string, err error) error {
			var refused *format.RejectionError
			if errors.As(err, &refused) {
				return apierror.New(refused.Status, refused.Message)
			}
			return err
		},
		ServeItem: func(w http.ResponseWriter, r *http.Request, _ repo.Config, it model.Item) {
			rc, _, err := f.items.Blobs.Open(r.Context(), it.Actual.SHA256)
			if err != nil {
				f.t.Fatal(err)
			}
			defer rc.Close()
			if r.Method != http.MethodHead {
				_, _ = io.Copy(w, rc)
			}
		},
	}
	pkgroute.Mount(r, options, Protocol(Options{RemoteSources: svc}))
	f.router = r
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

// A virtual repository takes its members from the shared walk (decision
// 0008, R1 to R3): a local member reached through a nested virtual
// repository resolves, a cycle and a repeated member are skipped, a remote
// member whose upstream fails is passed over, and a caller who may read
// only the local member resolves through the virtual repository, with the
// unreadable remote member passed over.
func TestUnitBowerVirtualResolvesThroughTheSharedWalk(t *testing.T) {
	f := scriptedBowerRemote(t, false, func(context.Context, source.Request, source.Permit) (source.Result, error) {
		return source.Result{}, source.ErrUnavailable
	})
	l := f.repo("bower-local", repo.Local)
	_ = f.repo("bower-nested", repo.Virtual, l.Key, "bower-v")
	_ = f.repo("bower-v", repo.Virtual, "bower-remote", "bower-nested", "bower-v", "bower-remote")
	f.deploy(l, "hello", "1.0.0", "bower.json", `{"name":"hello","version":"1.0.0"}`)
	f.deploy(l, "hello", "1.0.0", "lib/index.js", "from-local")
	if err := f.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "local-only",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{"member": api.ActionRead}},
			Targets: map[string]accesstest.Target{l.Key: {}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	const base = "/artifactory/api/bower/bower-v/"
	for _, user := range []string{"admin", "member"} {
		for path, want := range map[string]string{
			"packages":               `[{"name":"hello","url":"art://bower-v/hello"}]`,
			"packages/search/hel":    `[{"name":"hello","url":"art://bower-v/hello"}]`,
			"packages/hello":         `{"name":"hello","url":"art://bower-v/hello"}`,
			"refs/bower-v/hello":     "refs/tags/v1.0.0\n",
			"refs/bower-v/not-there": "Bower package not found",
		} {
			rec := f.get(base+path, user)
			if !strings.Contains(rec.Body.String(), want) {
				t.Fatalf("%s %s: %d %s, want %s", user, path, rec.Code, rec.Body, want)
			}
		}
		rec := f.get(base+"binaries/bower-v/hello.git/v1.0.0", user)
		if rec.Code != http.StatusOK || archivedIndex(t, rec) != "from-local" {
			t.Fatalf("%s archive: %d %s", user, rec.Code, rec.Body)
		}
		rec = f.get(base+"binaries/bower-v/hello.git/v2.0.0", user)
		if rec.Code != http.StatusNotFound || bowerMessage(t, rec) != "Bower version not found" {
			t.Fatalf("%s missing version: %d %s", user, rec.Code, rec.Body)
		}
	}
}

// A member the caller may not read a version's manifest in is passed over
// for the next member that holds the version (decision 0008, R3 and R10).
func TestUnitBowerVirtualPassesOverAMemberThatHidesTheVersion(t *testing.T) {
	f := newFixture(t)
	a, b := f.repo("bower-a", repo.Local), f.repo("bower-b", repo.Local)
	for _, c := range []repo.Config{a, b} {
		f.deploy(c, "hello", "1.0.0", "bower.json", `{"name":"hello","version":"1.0.0"}`)
		f.deploy(c, "hello", "1.0.0", "lib/index.js", "from-"+c.Key)
	}
	_ = f.repo("bower-v", repo.Virtual, a.Key, b.Key)
	if err := f.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "partial",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{"partial": api.ActionRead}},
			Targets: map[string]accesstest.Target{
				a.Key: {Include: []string{"hello/2.0.0/**"}},
				b.Key: {},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	rec := f.get("/artifactory/api/bower/bower-v/binaries/bower-v/hello.git/v1.0.0", "partial")
	if rec.Code != http.StatusOK || archivedIndex(t, rec) != "from-bower-b" {
		t.Fatalf("archive past a member that hides the version: %d %s", rec.Code, rec.Body)
	}
}
