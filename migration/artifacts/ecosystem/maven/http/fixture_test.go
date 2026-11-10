package mavenhttp

// This file holds the fixtures of the tests that call the Maven REST
// operations. A test here cannot build the router, which imports this
// package, so the world is a kittest world, and do serves a request through
// a chi router on which RegisterMavenRoutes mounts the operations, as the
// router does. The caller is in the context as the router's authentication
// leaves it, with a fake decider.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/migration/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
)

// The keys of the repositories every world holds, beside kittest's generic
// ones, and the prefixes of the two operations.
const (
	mavenLocal  = "maven-local"
	mavenRemote = "maven-remote"
	gradleLocal = "gradle-local"

	calc = "/artifactory/api/maven/calculateMetadata/"
	gen  = "/artifactory/api/maven/generatePom/"
)

// itemWorld is a kittest world, the routes that serve its requests as the
// router would, and what the kit logged.
type itemWorld struct {
	t     *testing.T
	world *kittest.World[*inmem.Store]
	logs  *bytes.Buffer
	h     http.Handler
}

// newItemWorld returns a kittest world holding a local and a remote Maven
// repository and a local Gradle one, and the routes over an item kit built
// from it as the router builds its own. tune adjusts the kit's dependencies
// first. The authorizer is a fake whose evaluation allows nothing, so a
// caller holds only what the decider in its request grants.
func newItemWorld(t *testing.T, tune ...func(*itemkit.Deps)) *itemWorld {
	t.Helper()
	k := kittest.New(t, inmem.New())
	logs := &bytes.Buffer{}
	d := itemkit.Deps{
		Store:  k.Store,
		Blobs:  k.Blobs,
		Access: accessapi.API{Authorizer: &accessapitest.Authorizer{}},
		Logger: slog.New(slog.NewJSONHandler(logs, nil)),
	}
	for _, f := range tune {
		f(&d)
	}
	r := chi.NewRouter()
	MavenAPI{Kit: itemkit.New(d)}.RegisterMavenRoutes(r)
	w := &itemWorld{t: t, world: k, logs: logs, h: r}
	for _, c := range []repo.Config{
		{Key: mavenLocal, Class: repo.Local, PackageType: repo.Maven},
		{
			Key: mavenRemote, Class: repo.Remote, PackageType: repo.Maven,
			URL: "https://maven.example",
		},
		{Key: gradleLocal, Class: repo.Local, PackageType: repo.Gradle},
	} {
		if err := k.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

// caller is who a request comes from: the principal, and the decider the
// router's authentication binds to it. A nil decider leaves the
// principal's permissions to the kit's authorizer.
type caller struct {
	p kernel.Principal
	d accessapi.Decider
}

var (
	// admin is kittest's administrator, whom every question allows.
	admin = &caller{kittest.Admin, kittest.AdminAccess().Decider}
	// user is a user whom no question allows.
	user = &caller{kernel.Principal{Name: "user"}, &accessapitest.Decider{}}
	// anonymous is the anonymous user, whom no question allows.
	anonymous = &caller{kernel.Anonymous, &accessapitest.Decider{}}
)

// holding is a user who holds actions, and no other, on every path of
// every repository.
func holding(actions accessapi.Actions) *caller {
	holds := func(
		_ accessapi.ResourceType, _ accessapi.Repo, _ string, want accessapi.Actions,
	) bool {
		return want&^actions == 0
	}
	return &caller{
		kernel.Principal{Name: "holder"},
		&accessapitest.Decider{AllowsFunc: holds, AllowsFolderFunc: holds},
	}
}

// do serves method on target through the routes as the caller as, or with
// no caller in the context when as is nil, which the router's
// authentication never lets through.
func (w *itemWorld) do(method, target string, as *caller) *httptest.ResponseRecorder {
	w.t.Helper()
	r := httptest.NewRequestWithContext(w.t.Context(), method, target, nil)
	if as != nil {
		r = r.WithContext(accessapi.WithCaller(r.Context(), as.p, as.d))
	}
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, r)
	return rec
}

// folder stores a folder at path in maven-local.
func (w *itemWorld) folder(path string) {
	w.t.Helper()
	if _, err := w.world.Store.Items().Put(w.t.Context(), model.Item{
		RepoKey: mavenLocal, Name: path, Folder: true, CreatedBy: kittest.Admin.Name,
	}); err != nil {
		w.t.Fatal(err)
	}
}

// file stores body as the file at path in maven-local, blob and item,
// without the format's hooks, so that nothing is indexed or queued for it.
func (w *itemWorld) file(path string, body []byte) {
	w.t.Helper()
	w.world.Put(w.t, repo.Config{Key: mavenLocal}, false, path, body)
}

// content returns the content of the file at path in maven-local, and
// whether there is one.
func (w *itemWorld) content(path string) (string, bool) {
	w.t.Helper()
	ctx := w.t.Context()
	it, err := w.world.Store.Items().Get(ctx, mavenLocal, false, path)
	if errors.Is(err, kernel.ErrNotFound) {
		return "", false
	}
	if err != nil {
		w.t.Fatal(err)
	}
	rc, _, err := w.world.Blobs.Open(ctx, it.Actual.SHA256)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		w.t.Fatal(err)
	}
	return string(body), true
}

// queued reports whether work of kind is queued for key.
func (w *itemWorld) queued(kind, key string) bool {
	w.t.Helper()
	_, err := w.world.Store.WorkQueue().Get(w.t.Context(), kind, key)
	if err != nil && !errors.Is(err, kernel.ErrNotFound) {
		w.t.Fatal(err)
	}
	return err == nil
}

// pom is a POM of g:a:v.
func pom(g, a, v string) string {
	return `<project xmlns="http://maven.apache.org/POM/4.0.0"><modelVersion>4.0.0</modelVersion>` +
		`<groupId>` + g + `</groupId><artifactId>` + a + `</artifactId><version>` + v +
		`</version></project>`
}

// jar is a jar of com.acme:lib carrying body as its own POM, where Maven
// packs it.
func jar(t *testing.T, body string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	f, err := zw.Create("META-INF/maven/com.acme/lib/pom.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// wantError fails the test unless rec answered status with the Artifactory
// error envelope that carries message, as every route writes an error. A
// 401 must also carry Artifactory's Basic challenge, without which a client
// never sends the credentials it holds.
func wantError(t *testing.T, rec *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	var body struct {
		Errors []struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if rec.Code != status || json.Unmarshal(rec.Body.Bytes(), &body) != nil ||
		len(body.Errors) != 1 || body.Errors[0].Status != status ||
		body.Errors[0].Message != message {
		t.Errorf("answered %d %s, want %d %q", rec.Code, rec.Body, status, message)
	}
	if challenge := rec.Header().Get("WWW-Authenticate"); status == http.StatusUnauthorized &&
		challenge != apierror.BasicChallenge {
		t.Errorf("401 challenge %q, want %q", challenge, apierror.BasicChallenge)
	}
}

// wantLogged fails the test unless the kit logged op with the injected
// fault as its error: a 500 names nothing internal, so the log is the only
// place that says which step failed.
func (w *itemWorld) wantLogged(t *testing.T, op string) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(w.logs.Bytes()))
	for dec.More() {
		var record struct {
			Msg   string `json:"msg"`
			Error string `json:"error"`
		}
		if err := dec.Decode(&record); err != nil {
			t.Fatalf("log record: %v", err)
		}
		if record.Msg == op && strings.Contains(record.Error, errFault.Error()) {
			return
		}
	}
	t.Errorf("no %q logged with %q in:\n%s", op, errFault, w.logs)
}

// errFault is the failure the faulty stores, the failing blob store and the
// failing authorizer here answer.
var errFault = errors.New("the store failed")

// failingAuthorizer evaluates no caller's permissions, as the access
// module does when it cannot read them.
var failingAuthorizer = &accessapitest.Authorizer{
	DecideFunc: func(context.Context, accessapi.Subject, bool) (accessapi.Decider, error) {
		return nil, errFault
	},
}

// repoLookupFails is a store whose repository lookups fail.
type repoLookupFails struct{ itemkit.Store }

func (s repoLookupFails) Repos() port.RepoStore { return failingRepoGet{s.Store.Repos()} }

type failingRepoGet struct{ port.RepoStore }

func (failingRepoGet) Get(context.Context, string) (repo.Config, error) {
	return repo.Config{}, errFault
}

// itemLookupFails is a store whose item lookups fail.
type itemLookupFails struct{ itemkit.Store }

func (s itemLookupFails) Items() port.ItemStore { return failingItemGet{s.Store.Items()} }

type failingItemGet struct{ port.ItemStore }

func (failingItemGet) Get(context.Context, string, bool, string) (model.Item, error) {
	return model.Item{}, errFault
}

// fileSearchFails is a store whose item searches fail, while its lookups
// of one item pass.
type fileSearchFails struct{ itemkit.Store }

func (s fileSearchFails) Items() port.ItemStore { return failingItemSearch{s.Store.Items()} }

type failingItemSearch struct{ port.ItemStore }

func (failingItemSearch) Search(context.Context, *aql.Query, aql.Options) ([]aql.Hit, error) {
	return nil, errFault
}
