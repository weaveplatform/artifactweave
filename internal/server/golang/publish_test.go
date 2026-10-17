package golang_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/golang"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	goLocal = "go-local"
	put     = "/artifactory/api/go/" + goLocal + "/"
)

// pubFixture is the Go protocol mounted as the server mounts it, over a store
// with a Go repository of each class. A request names its principal in
// X-Test-User: deployer may deploy, maintainer may also delete, reader may
// only read.
type pubFixture struct {
	h      http.Handler
	db     *inmem.Store
	faults []error
}

func newPubFixture(t *testing.T) *pubFixture {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	for _, c := range []repo.Config{
		{Key: goLocal, Class: repo.Local, PackageType: repo.Go},
		{Key: "go-remote", Class: repo.Remote, PackageType: repo.Go, URL: "https://up.example"},
		{Key: "go-virtual", Class: repo.Virtual, PackageType: repo.Go, Repositories: []string{goLocal}},
		{Key: "libs-local", Class: repo.Local},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	for user, a := range map[string]types.Actions{
		"reader":     types.ActionRead,
		"deployer":   types.ActionRead | types.ActionWrite,
		"maintainer": types.ActionRead | types.ActionWrite | types.ActionDelete,
	} {
		if err := db.Permissions().Create(ctx, types.Permission{
			Name: user,
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{user: a}},
				Targets: map[string]types.Target{goLocal: {Include: []string{"**"}}},
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &pubFixture{db: db}
	items := &item.Service{Store: db, Blobs: blobs}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pr := auth.Anonymous
			if name := r.Header.Get("X-Test-User"); name != "" {
				pr = auth.Principal{Name: name}
			}
			pr, err := auth.Authorize(r.Context(), authz.Sources{
				Permissions: db.Permissions(), Groups: db.Groups(),
			}, pr)
			if err != nil {
				t.Fatal(err)
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), pr)))
		})
	})
	pkgroute.Mount(r, pkgroute.Options{
		Store: db,
		Items: func() *item.Service { return items },
		// The server's mapping of the errors these deploys meet.
		ItemError: func(_ context.Context, _ string, err error) error {
			var refused *format.Rejection
			switch {
			case errors.As(err, &refused):
				return apierror.New(refused.Status, refused.Message)
			case errors.Is(err, item.ErrChecksumMismatch):
				return apierror.New(http.StatusConflict, err.Error())
			}
			return err
		},
		Created: func(w http.ResponseWriter, _ *http.Request, it types.Item) {
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, it.FullPath())
		},
		Fault: func(_ *http.Request, err error) { f.faults = append(f.faults, err) },
	}, golang.Protocol())
	f.h = r
	return f
}

func (f *pubFixture) put(path, user, body string, headers ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
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

func (f *pubFixture) item(t *testing.T, path string) types.Item {
	t.Helper()
	it, err := f.db.Items().Get(context.Background(), goLocal, false, path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return it
}

// jf go-publish's three PUTs store a version, with an upper-case module
// path sent as written or case-encoded, and a +incompatible version, at
// the paths the go command's requests decode to.
func TestPublish(t *testing.T) {
	f := newPubFixture(t)
	for _, c := range []struct{ sent, stored, version string }{
		{"github.com/Azure/SDK/@v/v1.0.0-RC1", "github.com/Azure/SDK/@v/v1.0.0-RC1", "v1.0.0-RC1"},
		{"github.com/!azure/!s!d!k/@v/v1.1.0", "github.com/Azure/SDK/@v/v1.1.0", "v1.1.0"},
		{"example.com/m/@v/v2.0.0+incompatible", "example.com/m/@v/v2.0.0+incompatible", "v2.0.0+incompatible"},
	} {
		// The matrix parameters as jf appends them, url.QueryEscape'd.
		props := ";build.name=b;go.version=" + strings.ReplaceAll(c.version, "+", "%2B")
		for ext, body := range map[string]string{
			"zip":  "zip of " + c.version,
			"mod":  "module x\n",
			"info": `{"Version":"` + c.version + `","Time":"2026-10-17T21:04:05Z"}`,
		} {
			rec := f.put(put+c.sent+"."+ext+props, "deployer", body)
			if rec.Code != http.StatusCreated || rec.Body.String() != c.stored+"."+ext ||
				rec.Header().Get("X-Checksum-Sha256") == "" {
				t.Fatalf("%s.%s: %d %s %v", c.sent, ext, rec.Code, rec.Body, rec.Header())
			}
			it := f.item(t, c.stored+"."+ext)
			props, err := f.db.Items().Properties(context.Background(), it.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(props["go.version"], []string{c.version}) {
				t.Fatalf("%s.%s: properties %v", c.stored, ext, props)
			}
		}
	}
	// The format indexed each version.
	for module, want := range map[string]int{"github.com/Azure/SDK": 2, "example.com/m": 1} {
		p, err := f.db.Packages().Find(context.Background(), goLocal, module)
		if err != nil {
			t.Fatalf("%s: %v", module, err)
		}
		vs, err := f.db.Packages().Versions(context.Background(), p.ID)
		if err != nil || len(vs) != want {
			t.Fatalf("%s: versions %v %v", module, vs, err)
		}
	}
}

// A pipeline may run jf go-publish again: the same .zip and .mod pass with
// Deploy alone and keep the stored file, while other content is refused
// even to a caller who may delete. A .info may be replaced by a caller who
// may delete.
func TestRepublish(t *testing.T) {
	f := newPubFixture(t)
	const v = "example.com/m/@v/v1.0.0"
	for ext, body := range map[string]string{
		"zip": "zip", "mod": "module example.com/m\n", "info": `{"Version":"v1.0.0"}`,
	} {
		if rec := f.put(put+v+"."+ext, "deployer", body); rec.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", ext, rec.Code, rec.Body)
		}
	}
	zip := f.item(t, v+".zip")
	for _, c := range []struct {
		ext, user, body string
		want            int
		says            string
	}{
		{"zip", "deployer", "zip", http.StatusCreated, ""},
		{"mod", "deployer", "module example.com/m\n", http.StatusCreated, ""},
		{
			"zip", "maintainer", "other zip", http.StatusConflict,
			"Version v1.0.0 of module example.com/m already has a different .zip; delete the version to replace it.\n",
		},
		{
			"mod", "deployer", "module other\n", http.StatusConflict,
			"Version v1.0.0 of module example.com/m already has a different .mod; delete the version to replace it.\n",
		},
		{"info", "deployer", `{"Version":"v1.0.0","Time":"2026-10-17T21:04:05Z"}`, http.StatusForbidden, ""},
		{"info", "maintainer", `{"Version":"v1.0.0","Time":"2026-10-17T21:04:05Z"}`, http.StatusCreated, ""},
	} {
		rec := f.put(put+v+"."+c.ext, c.user, c.body)
		if rec.Code != c.want || c.says != "" && rec.Body.String() != c.says {
			t.Errorf("%s as %s: %d %q, want %d", c.ext, c.user, rec.Code, rec.Body, c.want)
		}
	}
	if again := f.item(
		t,
		v+".zip",
	); !again.Modified.Equal(zip.Modified) ||
		again.Actual != zip.Actual {
		t.Fatalf("the .zip changed: %+v, then %+v", zip, again)
	}
}

// Every refusal is one plain-text sentence, the only error body the go
// command shows.
func TestPublishRefusals(t *testing.T) {
	f := newPubFixture(t)
	big := `{"Version":"v1.0.0","Pad":"` + strings.Repeat("x", 64<<10) + `"}`
	for _, c := range []struct {
		path, user, body string
		headers          []string
		want             int
		says             string
	}{
		{"example.com/m/@v/v1.0.0.zip", "reader", "z", nil, http.StatusForbidden, ""},
		{"example.com/m/@v/v1.0.0.zip", "", "z", nil, http.StatusUnauthorized, ""},
		{"example.com/m/@v/v1.0.0.info", "reader", "{}", nil, http.StatusForbidden, ""},
		{"example.com/m/@v/v1.0.0.info", "", "{}", nil, http.StatusUnauthorized, ""},
		{
			"example.com/m/@v/v1.0.0.tgz", "deployer", "z", nil, http.StatusBadRequest,
			"File v1.0.0.tgz is not a Go module .zip, .mod or .info.\n",
		},
		{"example.com/m/@v/1.0.0.zip", "deployer", "z", nil, http.StatusBadRequest, ""},
		{"example.com/m/@v/v1.0.0+incompatible.zip", "deployer", "z", nil, http.StatusBadRequest, ""},
		{"example.com/m/v2/@v/v1.0.0.zip", "deployer", "z", nil, http.StatusBadRequest, ""},
		{"example.com/!m!/@v/v1.0.0.zip", "deployer", "z", nil, http.StatusBadRequest, ""},
		{"example.com/m/@v/v1.0.0.zip;=x", "deployer", "z", nil, http.StatusBadRequest, ""},
		{
			"example.com/m/@v/v1.0.0.info", "deployer", `{"Version":"v1.0.1"}`, nil, http.StatusBadRequest,
			"The .info of version v1.0.0 of module example.com/m does not give its Version as v1.0.0.\n",
		},
		{"example.com/m/@v/v1.0.0.info", "deployer", big, nil, http.StatusBadRequest, ""},
		{
			"example.com/m/@v/v1.0.0.zip", "deployer", "z",
			[]string{"X-Checksum-Sha1", strings.Repeat("0", 40)},
			http.StatusConflict, "",
		},
		// @v/list and @latest are read only; anything else is no Go file.
		{
			"example.com/m/@v/list", "deployer", "", nil, http.StatusMethodNotAllowed,
			"Method PUT is not allowed on " + put + "example.com/m/@v/list.\n",
		},
		{"example.com/m/@latest", "deployer", "", nil, http.StatusMethodNotAllowed, ""},
		{
			"example.com/m/go.mod", "deployer", "", nil, http.StatusNotFound,
			put + "example.com/m/go.mod names no Go module operation.\n",
		},
		{"sumdb/sum.golang.org/latest", "", "", nil, http.StatusNotFound, ""},
	} {
		rec := f.put(put+c.path, c.user, c.body, c.headers...)
		if rec.Code != c.want ||
			!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") ||
			c.says != "" && rec.Body.String() != c.says {
			t.Errorf("%s as %q: %d %q %q, want %d", c.path, c.user, rec.Code,
				rec.Header().Get("Content-Type"), rec.Body, c.want)
		}
	}
	// The class gate and the type check are the kit's, in Go's envelope.
	for path, want := range map[string]int{
		"/artifactory/api/go/go-remote/example.com/m/@v/v1.0.0.zip":  http.StatusNotImplemented,
		"/artifactory/api/go/go-virtual/example.com/m/@v/v1.0.0.zip": http.StatusNotImplemented,
		"/artifactory/api/go/libs-local/example.com/m/@v/v1.0.0.zip": http.StatusNotFound,
	} {
		if rec := f.put(path, "deployer", "z"); rec.Code != want {
			t.Errorf("%s: %d %s, want %d", path, rec.Code, rec.Body, want)
		}
	}
	if _, err := f.db.Items().Get(context.Background(), goLocal, false,
		"example.com/m/@v/v1.0.0.zip"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused deploy stored the file: %v", err)
	}
}

// A .info body that cannot be read is refused, and nothing is stored.
func TestPublishInfoReadFailure(t *testing.T) {
	f := newPubFixture(t)
	r := httptest.NewRequest(http.MethodPut, put+"example.com/m/@v/v1.0.0.info", failingBody{})
	r.Header.Set("X-Test-User", "deployer")
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	if rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "connection reset") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("connection reset") }
