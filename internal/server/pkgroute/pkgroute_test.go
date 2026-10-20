package pkgroute_test

import (
	"context"
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
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// testFormat is the test-only protocol: one route per part of the kit.
// It serves generic repositories under /artifactory/api/testfmt and answers
// in the npm envelope, {"error": "..."}, so a test can tell the kit's
// answers from the server's.
func testFormat(sources map[repo.Class]pkgroute.Source) pkgroute.Protocol {
	return pkgroute.Protocol{
		Name:     "testfmt",
		Types:    []repo.PackageType{repo.Generic},
		Surface:  apierror.NPM,
		Reserved: []string{"auth", "{repoKey}/reindex"},
		Absent:   []string{"hidden/"},
		Sources:  sources,
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			files := func(r *http.Request) (pkgroute.Path, error) {
				return pkgroute.ParsePath(
					strings.TrimPrefix(pkgroute.RequestFrom(r).Rest, "files/"),
				)
			}
			r.Get("/files/*", func(w http.ResponseWriter, r *http.Request) {
				p, err := files(r)
				if err != nil {
					k.Error(w, r, err)
					return
				}
				k.ServeItem(w, r, p.String())
			})
			r.Put("/files/*", func(w http.ResponseWriter, r *http.Request) {
				p, err := files(r)
				if err != nil {
					k.Error(w, r, err)
					return
				}
				k.Deploy(w, r, p.String(), p.Props)
			})
			r.Get("/check/*", func(w http.ResponseWriter, r *http.Request) {
				if k.Require(w, r, chi.URLParam(r, "*"), types.ActionRead) {
					w.WriteHeader(http.StatusNoContent)
				}
			})
			// The package's name as the router saw it, and as ParsePath
			// decodes it.
			r.Get("/pkg/{name}", func(w http.ResponseWriter, r *http.Request) {
				raw := chi.URLParam(r, "name")
				p, err := pkgroute.ParsePath(raw)
				if err != nil {
					k.Error(w, r, err)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"raw": raw, "segments": p.Segments, "repo": pkgroute.RequestFrom(r).Repo.Key,
				})
			})
			r.Get("/fault", func(w http.ResponseWriter, r *http.Request) {
				k.Error(w, r, errors.New("boom"))
			})
		},
	}
}

// kitFixture is a router with the test format mounted beside fake JFrog
// operations, over a store with repositories of every class and a few
// grants. A request names its principal in X-Test-User: none, anonymous,
// or a user.
type kitFixture struct {
	h      http.Handler
	db     *inmem.Store
	items  *item.Service
	served []string
	faults []error
}

const (
	local     = "gen-local"
	federated = "gen-fed"
)

func newKitFixture(t *testing.T, sources map[repo.Class]pkgroute.Source) *kitFixture {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	for _, c := range []repo.Config{
		{Key: local, Class: repo.Local},
		{Key: federated, Class: repo.Federated},
		{Key: "gen-remote", Class: repo.Remote, URL: "https://up.example"},
		{Key: "gen-virtual", Class: repo.Virtual, Repositories: []string{local}},
		{Key: "img-local", Class: repo.Local, PackageType: repo.Docker},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	grant := func(name, user, under string, a types.Actions) {
		t.Helper()
		if err := db.Permissions().Create(ctx, types.Permission{
			Name: name,
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{user: a}},
				Targets: map[string]types.Target{
					local:     {Include: []string{under + "**"}},
					federated: {Include: []string{under + "**"}},
				},
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	grant("reader", "reader", "", types.ActionRead)
	grant("writer", "writer", "work/", types.ActionRead|types.ActionWrite|types.ActionDelete)
	// updater deploys but may not replace what is there.
	grant("updater", "updater", "", types.ActionRead|types.ActionWrite)
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &kitFixture{db: db, items: &item.Service{Store: db, Blobs: blobs}}
	for _, p := range []string{"a/b.txt", "work/old.txt", "dir/inner.txt"} {
		f.deploy(t, local, p, "content of "+p)
	}
	o := pkgroute.Options{
		Store: db,
		Items: func() *item.Service { return f.items },
		ServeItem: func(w http.ResponseWriter, _ *http.Request, c repo.Config, it types.Item) {
			f.served = append(f.served, c.Key+"/"+it.FullPath())
			_, _ = io.WriteString(w, "served "+it.FullPath())
		},
		ItemError: func(_ context.Context, _ string, err error) error {
			var overwrite *item.OverwriteDeniedError
			switch {
			case errors.Is(err, item.ErrBadChecksum):
				return apierror.New(http.StatusBadRequest, err.Error())
			case errors.As(err, &overwrite):
				return apierror.New(http.StatusForbidden, overwrite.Error())
			}
			return apierror.New(http.StatusInternalServerError, "internal error")
		},
		Fault: func(_ *http.Request, err error) { f.faults = append(f.faults, err) },
	}
	f.h = f.router(t, o, testFormat(sources))
	return f
}

// router mounts p as the server does, with the JFrog operations the
// reserved paths name registered beside it, and the principal of
// X-Test-User put where the server's middleware puts it.
func (f *kitFixture) router(t *testing.T, o pkgroute.Options, p pkgroute.Protocol) http.Handler {
	t.Helper()
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := r.Header.Get("X-Test-User")
			if name == "" {
				next.ServeHTTP(w, r)
				return
			}
			pr := auth.Principal{Name: name}
			if name == auth.AnonymousUser {
				pr = auth.Anonymous
			}
			if r.Header.Get("X-Test-Unevaluated") == "" {
				var err error
				pr, err = auth.Authorize(r.Context(), authz.Sources{
					Permissions: f.db.Permissions(), Groups: f.db.Groups(), AnonymousEnabled: true,
				}, pr)
				if err != nil {
					t.Fatal(err)
				}
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), pr)))
		})
	})
	pkgroute.Mount(r, o, p)
	op := func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "jfrog operation") }
	r.Post("/artifactory/api/testfmt/{repoKey}/reindex", op)
	r.Get("/artifactory/api/testfmt/auth", op)
	return r
}

func (f *kitFixture) deploy(t *testing.T, key, path, body string) {
	t.Helper()
	ctx := context.Background()
	c, err := f.db.Repos().Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.items.Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: c, Path: path}, Body: strings.NewReader(body),
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *kitFixture) do(
	method, path, user, body string,
	headers ...string,
) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
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

// npmError is the npm envelope's message, which says the kit answered.
func npmError(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error == "" {
		t.Fatalf("not the protocol's envelope: %d %s", rec.Code, rec.Body)
	}
	return body.Error
}

const base = "/artifactory/api/testfmt/"

func TestResolveRepository(t *testing.T) {
	f := newKitFixture(t, nil)
	for _, c := range []struct {
		name, path string
		want       int
		message    string
	}{
		{"missing", base + "nope/files/a", http.StatusNotFound, "Repository 'nope' does not exist"},
		{"other type", base + "img-local/files/a", http.StatusNotFound, "is not a testfmt repository"},
		{"remote", base + "gen-remote/files/a", http.StatusNotImplemented, "is a remote repository"},
		{"virtual", base + "gen-virtual/files/a", http.StatusNotImplemented, "is a virtual repository"},
		{"no route", base + local + "/nowhere", http.StatusNotFound, "not found: GET"},
		{"bare key", base + local, http.StatusNotFound, "not found: GET"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := f.do(http.MethodGet, c.path, "reader", "")
			if rec.Code != c.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.want, rec.Body)
			}
			if msg := npmError(t, rec); !strings.Contains(msg, c.message) {
				t.Fatalf("message %q, want it to contain %q", msg, c.message)
			}
		})
	}
	rec := f.do(http.MethodDelete, base+local+"/check/x", "reader", "")
	if rec.Code != http.StatusMethodNotAllowed || !strings.Contains(npmError(t, rec), "DELETE") {
		t.Fatalf("method no route serves: %d %s", rec.Code, rec.Body)
	}
	// A federated repository holds its own items, as a local one does.
	f.deploy(t, federated, "fed.txt", "fed")
	rec = f.do(http.MethodGet, base+federated+"/files/fed.txt", "reader", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("federated: %d %s", rec.Code, rec.Body)
	}
}

// The JFrog operations at fixed literals beside the protocol keep their
// methods; any other method answers 405 from the kit rather than being
// read as a package path.
func TestReservedOperations(t *testing.T) {
	f := newKitFixture(t, nil)
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{http.MethodPost, base + local + "/reindex", http.StatusOK},
		{http.MethodGet, base + local + "/reindex", http.StatusMethodNotAllowed},
		{http.MethodGet, base + "auth", http.StatusOK},
		{http.MethodPut, base + "auth", http.StatusMethodNotAllowed},
		// A repository called auth is still a repository.
		{http.MethodGet, base + "auth/files/a", http.StatusNotFound},
	} {
		rec := f.do(c.method, c.path, "reader", "")
		if rec.Code != c.want {
			t.Errorf("%s %s: status %d, want %d: %s", c.method, c.path, rec.Code, c.want, rec.Body)
		}
		if c.want == http.StatusOK && rec.Body.String() != "jfrog operation" {
			t.Errorf("%s %s reached %q, not the operation", c.method, c.path, rec.Body)
		}
		// The operation's 405 is in its own envelope, not the protocol's.
		if c.want == http.StatusMethodNotAllowed &&
			!strings.HasPrefix(rec.Body.String(), `{"errors":[`) {
			t.Errorf("%s %s: 405 not in the Artifactory envelope: %s", c.method, c.path, rec.Body)
		}
	}
}

// TestAbsent: an absent path answers 404 to anyone before its repository
// is looked up, so a repository that does not exist answers it the same.
func TestAbsent(t *testing.T) {
	f := newKitFixture(t, nil)
	for _, path := range []string{
		base + local + "/hidden/a", base + "nope/hidden/a", base + "gen-remote/hidden",
	} {
		rec := f.do(http.MethodPost, path, "", "")
		if rec.Code != http.StatusNotFound ||
			!strings.Contains(npmError(t, rec), "does not serve") {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	p := testFormat(nil)
	for path, want := range map[string]bool{
		base + local + "/hidden/a":   true,
		base + local + "/hidden":     true,
		base + local + "/hiddenx":    false,
		base + local + "/files/a":    false,
		base + local:                 false,
		"/artifactory/api/x/hidden/": false,
	} {
		if got := p.IsAbsent(path); got != want {
			t.Errorf("IsAbsent(%s) = %v, want %v", path, got, want)
		}
	}
}

func TestDB(t *testing.T) {
	db := inmem.New()
	var got store.Store
	r := chi.NewRouter()
	pkgroute.Mount(r, pkgroute.Options{Store: db}, pkgroute.Protocol{
		Name: "dbfmt", Types: []repo.PackageType{repo.Generic},
		Routes: func(_ chi.Router, k *pkgroute.Kit) { got = k.DB() },
	})
	if got != db {
		t.Fatalf("DB() = %v, want the kit's store", got)
	}
}

func TestRequire(t *testing.T) {
	f := newKitFixture(t, nil)
	path := base + local + "/check/a/b.txt"
	for _, c := range []struct {
		user        string
		unevaluated bool
		want        int
	}{
		{"reader", false, http.StatusNoContent},
		{"writer", false, http.StatusForbidden},
		{"nobody", false, http.StatusForbidden},
		{auth.AnonymousUser, false, http.StatusUnauthorized},
		{"", false, http.StatusUnauthorized},
		// Permissions the middleware never evaluated grant nothing.
		{"reader", true, http.StatusForbidden},
	} {
		var headers []string
		if c.unevaluated {
			headers = []string{"X-Test-Unevaluated", "1"}
		}
		rec := f.do(http.MethodGet, path, c.user, "", headers...)
		if rec.Code != c.want {
			t.Errorf("%q (unevaluated %v): status %d, want %d: %s",
				c.user, c.unevaluated, rec.Code, c.want, rec.Body)
		}
		if rec.Code == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%q: 401 without a challenge", c.user)
		}
	}
}

func TestServeItem(t *testing.T) {
	f := newKitFixture(t, nil)
	rec := f.do(http.MethodGet, base+local+"/files/a/b.txt", "reader", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "served a/b.txt" {
		t.Fatalf("download: %d %s", rec.Code, rec.Body)
	}
	if len(f.served) != 1 || f.served[0] != local+"/a/b.txt" {
		t.Fatalf("served %v", f.served)
	}
	for _, c := range []struct {
		path, user string
		want       int
	}{
		{"/files/a/missing.txt", "reader", http.StatusNotFound},
		{"/files/dir", "reader", http.StatusNotFound},
		// Refused before the lookup: a missing file is refused as an
		// existing one is.
		{"/files/a/b.txt", "nobody", http.StatusForbidden},
		{"/files/a/missing.txt", "nobody", http.StatusForbidden},
		{"/files/a/b.txt", "", http.StatusUnauthorized},
		{"/files/a/b.txt;=v", "reader", http.StatusBadRequest},
	} {
		rec := f.do(http.MethodGet, base+local+c.path, c.user, "")
		if rec.Code != c.want {
			t.Errorf("%s as %q: status %d, want %d: %s", c.path, c.user, rec.Code, c.want, rec.Body)
		}
		npmError(t, rec)
	}
	if len(f.served) != 1 {
		t.Fatalf("a refused or missing download was served: %v", f.served)
	}
}

// failingSource fails every lookup, as a broken store would.
type failingSource struct{}

func (failingSource) Item(
	context.Context,
	store.Store,
	repo.Config,
	string,
) (pkgroute.Found, error) {
	return pkgroute.Found{}, errors.New("source down")
}

// A Source serves a class the protocol opts into; its failure is a fault
// that reaches the access log, never the client.
func TestSourceSeam(t *testing.T) {
	f := newKitFixture(t, map[repo.Class]pkgroute.Source{
		repo.Local: pkgroute.Local{}, repo.Remote: failingSource{},
	})
	rec := f.do(http.MethodGet, base+"gen-remote/files/a", "admin", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("remote without a grant: %d %s", rec.Code, rec.Body)
	}
	if err := f.db.Permissions().Create(context.Background(), types.Permission{
		Name: "remote-reader",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"reader": types.ActionRead}},
			Targets: map[string]types.Target{"gen-remote": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	rec = f.do(http.MethodGet, base+"gen-remote/files/a", "reader", "")
	if rec.Code != http.StatusInternalServerError || len(f.faults) != 1 ||
		strings.Contains(rec.Body.String(), "source down") {
		t.Fatalf("source failure: %d %s, faults %v", rec.Code, rec.Body, f.faults)
	}
	// Federated is no longer served once the protocol names its classes.
	rec = f.do(http.MethodGet, base+federated+"/files/a", "reader", "")
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("federated: %d %s", rec.Code, rec.Body)
	}
	rec = f.do(http.MethodGet, base+local+"/fault", "reader", "")
	if rec.Code != http.StatusInternalServerError || len(f.faults) != 2 {
		t.Fatalf("fault: %d %s, faults %v", rec.Code, rec.Body, f.faults)
	}
}

func TestDeploy(t *testing.T) {
	f := newKitFixture(t, nil)
	rec := f.do(http.MethodPut, base+local+"/files/work/new.txt;k=a,b;m=1", "writer", "hello",
		"X-Checksum-Sha1", "aaf4c61ddcc5e8a2dabede0f3b482cd9aea9434d")
	if rec.Code != http.StatusCreated || rec.Body.Len() != 0 {
		t.Fatalf("deploy: %d %s", rec.Code, rec.Body)
	}
	const helloSHA256 = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got := rec.Header().Get("X-Checksum-Sha256"); got != helloSHA256 {
		t.Fatalf("X-Checksum-Sha256 %q", got)
	}
	if rec.Header().Get("X-Checksum-Sha1") == "" || rec.Header().Get("X-Checksum-Md5") == "" {
		t.Fatalf("checksum headers %v", rec.Header())
	}
	ctx := context.Background()
	it, err := f.db.Items().Get(ctx, local, false, "work/new.txt")
	if err != nil {
		t.Fatal(err)
	}
	props, err := f.db.Items().Properties(ctx, it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(props["k"], ",") != "a,b" || strings.Join(props["m"], ",") != "1" {
		t.Fatalf("properties %v", props)
	}
	if it.CreatedBy != "writer" {
		t.Fatalf("created by %q", it.CreatedBy)
	}
	for _, c := range []struct {
		path, user string
		headers    []string
		want       int
	}{
		// Outside writer's grant.
		{"/files/a/new.txt", "writer", nil, http.StatusForbidden},
		{"/files/a/new.txt", "", nil, http.StatusUnauthorized},
		{"/files/a/new.txt", auth.AnonymousUser, nil, http.StatusUnauthorized},
		// Replacing a file needs Delete, which updater lacks.
		{"/files/a/b.txt", "updater", nil, http.StatusForbidden},
		{"/files/a/fresh.txt", "updater", nil, http.StatusCreated},
		// writer may replace its own.
		{"/files/work/old.txt", "writer", nil, http.StatusCreated},
		// The item service's refusal keeps its status.
		{"/files/work/bad.txt", "writer", []string{"X-Checksum-Sha1", "zz"}, http.StatusBadRequest},
		{"/files/work/bad.txt;=v", "writer", nil, http.StatusBadRequest},
	} {
		rec := f.do(http.MethodPut, base+local+c.path, c.user, "body", c.headers...)
		if rec.Code != c.want {
			t.Errorf("%s as %q: status %d, want %d: %s", c.path, c.user, rec.Code, c.want, rec.Body)
		}
	}
	// The refused overwrite reaches the server's mapping (ItemError) as the
	// item service's own refusal, naming the file and the caller, and is
	// answered in the protocol's envelope; the file is left as it was.
	rec = f.do(http.MethodPut, base+local+"/files/a/b.txt", "updater", "other")
	const refused = "Not enough permissions to delete/overwrite artifact 'gen-local:a/b.txt' " +
		"(user: 'updater' needs DELETE permission)."
	if got := npmError(t, rec); rec.Code != http.StatusForbidden || got != refused {
		t.Fatalf("refused overwrite: %d %q", rec.Code, got)
	}
	if it, err := f.db.Items().Get(ctx, local, false, "a/b.txt"); err != nil ||
		it.ModifiedBy == "updater" {
		t.Fatalf("the refused overwrite stored %+v %v", it, err)
	}
}

// failingItems fails reading items, so a deploy cannot tell whether it
// replaces a file.
type failingItems struct{ store.ItemStore }

func (failingItems) Get(context.Context, string, bool, string) (types.Item, error) {
	return types.Item{}, errors.New("items down")
}

type failingStore struct{ *inmem.Store }

func (s failingStore) Items() store.ItemStore { return failingItems{s.Store.Items()} }

func TestDeployStoreFailure(t *testing.T) {
	f := newKitFixture(t, nil)
	o := pkgroute.Options{
		Store: failingStore{f.db},
		Items: func() *item.Service { return f.items },
		Fault: func(_ *http.Request, err error) { f.faults = append(f.faults, err) },
	}
	f.h = f.router(t, o, testFormat(nil))
	rec := f.do(http.MethodPut, base+local+"/files/work/x.txt", "writer", "x")
	if rec.Code != http.StatusInternalServerError || len(f.faults) != 1 {
		t.Fatalf("store failure: %d %s, faults %v", rec.Code, rec.Body, f.faults)
	}
}

// A repository the store fails to read is a fault, and a kit without a
// store answers as the server's handlers do without one.
func TestRepositoryLookupFailures(t *testing.T) {
	f := newKitFixture(t, nil)
	f.h = f.router(t, pkgroute.Options{
		Store: brokenRepos{f.db},
		Fault: func(_ *http.Request, err error) { f.faults = append(f.faults, err) },
	}, testFormat(nil))
	rec := f.do(http.MethodGet, base+local+"/files/a", "reader", "")
	if rec.Code != http.StatusInternalServerError || len(f.faults) != 1 {
		t.Fatalf("broken repositories: %d %s", rec.Code, rec.Body)
	}
	f.h = f.router(t, pkgroute.Options{}, testFormat(nil))
	rec = f.do(http.MethodGet, base+local+"/files/a", "reader", "")
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("no store: %d %s", rec.Code, rec.Body)
	}
}

type brokenRepoStore struct{ store.RepoStore }

func (brokenRepoStore) Get(context.Context, string) (repo.Config, error) {
	return repo.Config{}, errors.New("repos down")
}

type brokenRepos struct{ *inmem.Store }

func (s brokenRepos) Repos() store.RepoStore { return brokenRepoStore{s.Store.Repos()} }

// npm addresses a scoped package as "@scope%2fname": chi matches the
// escaped path, so the name reaches the handler as one parameter, and
// ParsePath keeps its slash inside the one segment.
func TestEncodedSlash(t *testing.T) {
	f := newKitFixture(t, nil)
	rec := f.do(http.MethodGet, base+local+"/pkg/@scope%2fname", "reader", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var got struct {
		Raw      string   `json:"raw"`
		Segments []string `json:"segments"`
		Repo     string   `json:"repo"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Raw != "@scope%2fname" || len(got.Segments) != 1 || got.Segments[0] != "@scope/name" ||
		got.Repo != local {
		t.Fatalf("%+v", got)
	}
	// Unencoded, the slash is a second segment, which this route does not
	// match.
	rec = f.do(http.MethodGet, base+local+"/pkg/@scope/name", "reader", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unencoded: %d %s", rec.Code, rec.Body)
	}
}
