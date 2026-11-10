package terraformhttp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/migration/access/accesstest"
	"github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/core/model"
	"github.com/weaveplatform/artifactweave/migration/access/core/port"
	"github.com/weaveplatform/artifactweave/migration/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/migration/platform/crypt"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/baseurl"
	"github.com/weaveplatform/artifactweave/migration/platform/settings"
)

// serverStore is the one store the router kit serves and the access API is
// bound over, as a server binds both, so a test that swaps in a failing
// store fails the access reads too.
type serverStore interface {
	pkgroute.Store
	accesstest.Store
}

type fixture struct {
	t     *testing.T
	db    *inmem.Store
	read  serverStore
	items *item.Service
	h     http.Handler
	keys  *crypt.Keyring
	now   time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keys, err := crypt.New(bytes.Repeat([]byte("k"), 32))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		t:     t,
		db:    db,
		items: &item.Service{Store: db, Blobs: blobs, Formats: ecosystem.For},
		keys:  keys,
		now:   time.Now(),
	}
	for _, c := range []repo.Config{
		{Key: "mods", Class: repo.Local, PackageType: repo.Terraform, TerraformType: repo.TerraformModule},
		{Key: "v1", Class: repo.Local, PackageType: repo.Terraform, TerraformType: repo.TerraformModule},
		{Key: "Mods__Nested", Class: repo.Federated, PackageType: repo.Terraform, TerraformType: repo.TerraformModule},
		{Key: "provider", Class: repo.Local, PackageType: repo.Terraform, TerraformType: repo.TerraformProvider},
		{Key: "untyped", Class: repo.Local, PackageType: repo.Terraform},
		{Key: "other", Class: repo.Local},
		{Key: "remote", Class: repo.Remote, PackageType: repo.Terraform, URL: "https://example.test"},
		{Key: "virtual", Class: repo.Virtual, PackageType: repo.Terraform},
	} {
		if err := db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []model.User{{Name: "admin", Admin: true}, {Name: "reader"}} {
		if err := db.Users().Create(t.Context(), u); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Permissions().
		Create(t.Context(), model.Permission{Name: "read", Resources: map[api.ResourceType]model.Resource{api.ResourceArtifact: {
			Actions: model.Grants{
				Users: map[string]api.Actions{
					"reader":    api.ActionRead,
					"anonymous": api.ActionRead,
				},
			},
			Targets: map[string]model.Target{"mods": {Include: []string{"ns/name/aws/1.0.0.zip"}}},
		}}}); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"2.0.0", "1.10.0", "v1.0.0", "1.0.0", "1.1.0-beta"} {
		f.deploy("mods", "ns/name/aws/"+v+".zip", v)
	}
	f.deploy("v1", "ns/name/aws/1.0.0.zip", "v1")
	f.deploy("Mods__Nested", "ns/name/aws/1.0.0.zip", "nested")
	f.read = db
	f.h = f.router(keys)
	return f
}

func (f *fixture) router(keys *crypt.Keyring) http.Handler {
	f.t.Helper()
	// Bound over f.read, the store the router kit serves, so a test that
	// swaps in a failing store fails the access reads too.
	api := accesstest.Over(f.t, f.read).API()
	principalOf := func(r *http.Request) kernel.Principal {
		user := r.Header.Get("X-User")
		return kernel.Principal{Name: user, Admin: user == "admin", Anonymous: user == "anonymous"}
	}
	authorize := accesstest.Over(f.t, f.db).Middleware(f.t, true, principalOf)
	r := chi.NewRouter()
	r.Use(baseurl.Middleware("https://registry.test"))
	r.Use(func(next http.Handler) http.Handler {
		authorized := authorize(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-User") == "" {
				next.ServeHTTP(w, r)
				return
			}
			authorized.ServeHTTP(w, r)
		})
	})
	pkgroute.Mount(
		r,
		pkgroute.Options{
			Store: f.read,
			Items: func() *item.Service { return f.items },
			ServeItem: func(w http.ResponseWriter, r *http.Request, _ repo.Config, it model.Item) {
				w.Header().Set("X-Checksum-Sha256", it.Actual.SHA256)
				if r.Method != "HEAD" {
					_, _ = w.Write([]byte(it.FullPath()))
				}
			},
		},
		Protocol(Options{
			Authorizer:      api.Authorizer,
			KeyPairs:        api.Keys,
			Restrictions:    api.Restrictions,
			Identity:        api.Identity,
			AnonymousAccess: api.AnonymousAccess,
			Keys:            keys,
			Now:             func() time.Time { return f.now },
		}),
	)
	return r
}

func (f *fixture) deploy(key, path, body string) {
	f.t.Helper()
	c, err := f.db.Repos().Get(f.t.Context(), key)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err = f.items.Deploy(
		f.t.Context(),
		item.Deploy{Target: item.Target{Repo: c, Path: path}, Body: strings.NewReader(body)},
	); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) do(method, path, user string) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("X-User", user)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func expect(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("got %d, want %d: %s", w.Code, status, w.Body)
	}
}

func TestAccModuleRoutes(t *testing.T) {
	f := newFixture(t)
	base := prefix + "v1/modules/mods__ns/name/aws/"
	for _, c := range []struct {
		method, path, user string
		status             int
	}{
		{"GET", "/.well-known/terraform.json", "", 200},
		{"HEAD", "/.well-known/terraform.json", "", 200},
		{"POST", "/.well-known/terraform.json", "", 405},
		{"GET", base + "versions", "admin", 200},
		{"HEAD", base + "versions", "admin", 200},
		{"POST", base + "versions", "admin", 405},
		{"GET", base + "versions", "", 401},
		{"GET", base + "versions", "nobody", 403},
		{"GET", base + "versions", "reader", 200},
		{"GET", base + "v1.0.0/download", "admin", 204},
		{"HEAD", base + "1.0.0/download", "admin", 204},
		{"GET", base + "2.0.0/download", "reader", 403},
		{"GET", base + "7.0.0/download", "admin", 404},
		{"GET", base + "1.0.0", "admin", 404},
		{"PUT", base + "unknown", "admin", 404},
		{"GET", prefix + "mods/v1/modules/ns/name/aws/versions", "admin", 200},
		{"GET", prefix + "v1/modules/MODS__ns/name/aws/versions", "admin", 200},
		{"GET", prefix + "Mods__Nested/v1/modules/ns/name/aws/versions", "admin", 200},
		{"GET", prefix + "v1/modules/provider__ns/name/aws/versions", "admin", 404},
		{"GET", prefix + "v1/modules/untyped__ns/name/aws/versions", "admin", 404},
		{"GET", prefix + "v1/modules/other__ns/name/aws/versions", "admin", 404},
		{"GET", prefix + "v1/modules/remote__ns/name/aws/versions", "admin", 501},
		{"GET", prefix + "v1/modules/virtual__ns/name/aws/versions", "admin", 501},
		{"GET", prefix + "v1/modules/no__ns/name/aws/versions", "admin", 404},
		{"GET", prefix + "v1/modules/plain/name/aws/versions", "admin", 404},
		{"GET", prefix + "v1/providers/mods-rt-ns-ns/null/versions", "admin", 404},
		{"GET", prefix + "mods/v1/modules/ns/name/aws/versions/", "admin", 404},
		{"GET", prefix + "mods/v1/modules/ns%2Fx/name/aws/versions", "admin", 404},
	} {
		t.Run(c.method+c.path+c.user, func(t *testing.T) {
			w := f.do(c.method, c.path, c.user)
			expect(t, w, c.status)
			if c.status >= 400 {
				var body struct {
					Errors []string `json:"errors"`
				}
				if json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Errors) == 0 {
					t.Fatal(w.Body)
				}
			}
		})
	}
	w := f.do("GET", base+"versions", "admin")
	if w.Body.String() != `{"modules":[{"versions":[{"version":"1.0.0"},{"version":"1.1.0-beta"},{"version":"1.10.0"},{"version":"2.0.0"}]}]}`+"\n" {
		t.Fatal(w.Body)
	}
	w = f.do("GET", base+"versions", "reader")
	if strings.Count(w.Body.String(), `"version"`) != 1 {
		t.Fatal(w.Body)
	}
	ns := strings.Repeat("n", 64)
	f.deploy("mods", ns+"/name/aws/1.0.0.zip", "long")
	expect(t, f.do("GET", prefix+"v1/modules/mods__"+ns+"/name/aws/versions", "admin"), 200)
}

func TestAccSignedDownloads(t *testing.T) {
	for _, scenario := range []string{"normal", "head", "method", "tamper", "expiry", "path", "content", "missing", "deleted-user", "disabled-user", "restricted-user", "revoked-read", "cross-node", "wrong-key", "anonymous", "anonymous-off", "v1-repo", "bad-format"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			key, user := "mods", "reader"
			if scenario == "v1-repo" {
				key, user = "v1", "admin"
			}
			if strings.HasPrefix(scenario, "anonymous") {
				user = "anonymous"
				if err := f.db.Settings().
					Put(t.Context(), settings.SettingAnonymousAccess, true); err != nil {
					t.Fatal(err)
				}
			}
			w := f.do("GET", prefix+"v1/modules/"+key+"__ns/name/aws/1.0.0/download", user)
			expect(t, w, 204)
			target := w.Header().Get("X-Terraform-Get")
			status, method := 200, "GET"
			switch scenario {
			case "head":
				method = "HEAD"
			case "method":
				method = "POST"
				status = 405
			case "tamper":
				target += "x"
				status = 403
			case "bad-format":
				target = strings.Split(target, "?sig=")[0] + "?sig=bad"
				status = 403
			case "expiry":
				f.now = f.now.Add(90 * time.Second)
				status = 403
			case "path":
				target = strings.Replace(target, "1.0.0.zip", "2.0.0.zip", 1)
				status = 403
			case "content":
				f.deploy(key, "ns/name/aws/1.0.0.zip", "replacement")
				status = 403
			case "missing":
				it, err := f.db.Items().Get(t.Context(), key, false, "ns/name/aws/1.0.0.zip")
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.db.Items().
					Delete(t.Context(), key, false, it.FullPath()); err != nil {
					t.Fatal(err)
				}
				status = 404
			case "deleted-user":
				if err := f.db.Users().Delete(t.Context(), user); err != nil {
					t.Fatal(err)
				}
				status = 403
			case "restricted-user":
				now := time.Now()
				if _, err := f.db.APIRestrictions().
					Put(t.Context(), port.APIRestriction{User: user, Actor: "admin", Reason: "Stop downloads", Changed: now, Until: now.Add(time.Hour)}); err != nil {
					t.Fatal(err)
				}
				status = 403
			case "disabled-user":
				u, err := f.db.Users().Get(t.Context(), user)
				if err != nil {
					t.Fatal(err)
				}
				u.Status = model.UserDisabled
				if err = f.db.Users().Update(t.Context(), u); err != nil {
					t.Fatal(err)
				}
				status = 403
			case "revoked-read":
				if err := f.db.Permissions().Delete(t.Context(), "read"); err != nil {
					t.Fatal(err)
				}
				status = 403
			case "cross-node":
				keys, err := crypt.New(bytes.Repeat([]byte("k"), 32))
				if err != nil {
					t.Fatal(err)
				}
				f.h = f.router(keys)
			case "wrong-key":
				keys, err := crypt.New(bytes.Repeat([]byte("x"), 32))
				if err != nil {
					t.Fatal(err)
				}
				f.h = f.router(keys)
				status = 403
			case "anonymous-off":
				if err := f.db.Settings().
					Put(t.Context(), settings.SettingAnonymousAccess, false); err != nil {
					t.Fatal(err)
				}
				status = 403
			}
			expect(t, f.do(method, target, ""), status)
		})
	}
}
