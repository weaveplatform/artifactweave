package terraform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const remoteModuleBase = prefix + "v1/modules/remote__ns/name/aws/"

type remoteFixture struct {
	*fixture
	up     *fakeupstream.Server
	policy *remote.Policy
	svc    *source.Service
	clock  *fakeupstream.Clock
	auth   *auth.Authenticator
}

func newRemoteFixture(t *testing.T) *remoteFixture {
	t.Helper()
	f := newFixture(t)
	up := fakeupstream.New(t, fakeupstream.Options{})
	c, _ := f.db.Repos().Get(t.Context(), "remote")
	c.URL = up.URL
	c.TerraformRegistryURL = up.URL
	c.TerraformType = ""
	ttl := 5
	c.RetrievalCachePeriodSecs = &ttl
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Permissions().
		Create(t.Context(), types.Permission{Name: "remote-read", Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {Actions: types.Grants{Users: map[string]types.Actions{"reader": types.ActionRead}}, Targets: map[string]types.Target{"remote": {Include: []string{"ns/name/aws/1.0.0.zip"}}}}}}); err != nil {
		t.Fatal(err)
	}
	resolver, err := remote.NewResolver(
		f.db,
		nil,
		remote.ResolverOptions{RevisionKey: []byte(strings.Repeat("k", 32))},
	)
	if err != nil {
		t.Fatal(err)
	}
	clock := fakeupstream.NewClock(time.Now())
	f.items.Now = clock.Now
	p := &remote.Policy{
		Store:   f.db,
		Blobs:   f.items.Blobs,
		Items:   f.items,
		Pool:    new(remote.Pool),
		Options: resolver.Options,
		Key:     []byte(strings.Repeat("k", 32)),
		Now:     clock.Now,
	}
	t.Cleanup(p.Pool.Close)
	svc := &source.Service{
		HTTP:  p,
		Cache: p,
		Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
			return f.db.RemoteCache().Source(ctx, c.Key)
		},
	}
	a, e := auth.NewAuthenticator(
		f.db.Users(),
		f.db.Settings(),
		auth.Options{
			Hash: auth.HashParams{Time: 1, MemoryKiB: 8, Threads: 1},
			Now:  func() time.Time { return f.now },
		},
	)
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"reader", "admin"} {
		u, _ := f.db.Users().Get(t.Context(), name)
		u.PasswordHash, e = auth.HashPasswordWith(
			auth.HashParams{Time: 1, MemoryKiB: 8, Threads: 1},
			"fixture-password",
		)
		if e != nil {
			t.Fatal(e)
		}
		if e = f.db.Users().Update(t.Context(), u); e != nil {
			t.Fatal(e)
		}
	}
	svc.Revalidate = func(ctx context.Context, q source.Request, _ source.Permit) error {
		p, ok := auth.FromContext(ctx)
		if !ok {
			return source.ErrDenied
		}
		fresh, e := a.Current(ctx, p, f.db.Tokens())
		if e != nil {
			return source.ErrDenied
		}
		fresh, e = auth.Authorize(
			ctx,
			authz.Sources{
				Permissions:      f.db.Permissions(),
				Groups:           f.db.Groups(),
				AnonymousEnabled: fresh.Anonymous,
			},
			fresh,
		)
		if e != nil {
			return e
		}
		allows := fresh.Access().Allows
		path := q.Resource.Path
		if q.Resource.PermissionFolder {
			allows = fresh.Access().AllowsFolder
			path = q.Resource.PermissionPath
		}
		if !allows(
			types.ResourceArtifact,
			authz.Repo{Key: q.Repo.Key, Class: string(q.Repo.Class)},
			path,
			types.ActionRead,
		) {
			return source.ErrDenied
		}
		return nil
	}
	p.CheckCurrent = func(ctx context.Context, _ store.Store, q source.Request, permit source.Permit, _ string) error {
		return svc.Revalidate(ctx, q, permit)
	}
	out := &remoteFixture{fixture: f, up: up, policy: p, svc: svc, clock: clock, auth: a}
	out.mount()
	out.json("/.well-known/terraform.json", map[string]string{"modules.v1": "/registry/modules/"})
	return out
}

func (f *remoteFixture) mount() {
	r := chi.NewRouter()
	r.Use(baseurl.Middleware("https://registry.test"))
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := r.Header.Get("X-User")
			if user != "" {
				authenticated, e := f.auth.Basic(r.Context(), user, "fixture-password")
				if e != nil {
					f.t.Fatal(e)
				}
				p, err := auth.Authorize(
					r.Context(),
					authz.Sources{
						Permissions:      f.db.Permissions(),
						Groups:           f.db.Groups(),
						AnonymousEnabled: true,
					},
					authenticated,
				)
				if err != nil {
					f.t.Fatal(err)
				}
				r = r.WithContext(auth.WithPrincipal(r.Context(), p))
			}
			next.ServeHTTP(w, r)
		})
	})
	pkgroute.Mount(
		r,
		pkgroute.Options{
			Store: f.db,
			Items: func() *item.Service { return f.items },
			ServeItem: func(w http.ResponseWriter, r *http.Request, _ repo.Config, it types.Item) {
				b, _, err := f.items.Blobs.Open(r.Context(), it.Actual.SHA256)
				if err != nil {
					f.t.Error(err)
					w.WriteHeader(500)
					return
				}
				defer b.Close()
				w.Header().Set("X-Checksum-Sha256", it.Actual.SHA256)
				if r.Method != "HEAD" {
					_, _ = io.Copy(w, b)
				}
			},
		},
		Protocol(
			Options{
				Keys:          f.keys,
				Now:           func() time.Time { return f.now },
				RemoteSources: f.svc,
				Auth:          f.auth,
			},
		),
	)
	f.h = r
}

func (f *remoteFixture) json(path string, v any) {
	b, _ := json.Marshal(v)
	f.up.Set(
		path,
		fakeupstream.Resource{Body: b, Header: http.Header{"Content-Type": {"application/json"}}},
	)
}

func (f *remoteFixture) modules(versions ...string) {
	out := []map[string]string{}
	for _, v := range versions {
		out = append(out, map[string]string{"version": v})
		f.location(v, "/archives/"+v+".zip")
		f.up.Set(
			"/archives/"+v+".zip",
			fakeupstream.Resource{
				Body:   archiveZip(f.t, "main.tf"),
				Header: http.Header{"Content-Type": {"application/zip"}},
			},
		)
	}
	f.json(
		"/registry/modules/ns/name/aws/versions",
		map[string]any{"modules": []any{map[string]any{"versions": out}}, "future": "retained"},
	)
}

func (f *remoteFixture) location(version, target string) {
	f.up.Set(
		"/registry/modules/ns/name/aws/"+version+"/download",
		fakeupstream.Resource{Status: 204, Header: http.Header{"X-Terraform-Get": {target}}},
	)
}

func (f *remoteFixture) signed(version, user string) string {
	f.t.Helper()
	w := f.do("GET", remoteModuleBase+version+"/download", user)
	expect(f.t, w, 204)
	u, e := url.Parse(w.Header().Get("X-Terraform-Get"))
	if e != nil || u.Host != "registry.test" || !u.Query().Has("sig") {
		f.t.Fatalf("invalid rewritten location %q", w.Header().Get("X-Terraform-Get"))
	}
	u.Path = strings.Split(u.Path, "//")[0]
	q := u.Query()
	q.Del("archive")
	u.RawQuery = q.Encode()
	return u.String()
}

// Re-sign a controlled claim while preserving the public route. This probes
// download-time authorization with a valid signature, not a malformed JWT.
func (f *remoteFixture) signedClaimURL(t *testing.T, original, routePrefix string, c claims) string {
	t.Helper()
	h := handler{o: Options{Keys: f.keys, Now: func() time.Time { return f.now }}}
	raw, err := h.signDownloadURL(httptest.NewRequest(http.MethodGet, original, nil), c)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(original)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = prefix + "remote/" + routePrefix + c.Path
	query := u.Query()
	query.Set("sig", signed.Query().Get("sig"))
	u.RawQuery = query.Encode()
	return u.String()
}

func (f *remoteFixture) signedModuleClaimURL(t *testing.T, original string, c claims) string {
	return f.signedClaimURL(t, original, "v1/modules/", c)
}

func TestAccRemoteSignedModuleRejectsChangedClaimsBeforeFetch(t *testing.T) {
	f := newRemoteFixture(t)
	f.modules("1.0.0")
	original := f.signed("1.0.0", "admin")
	h := handler{o: Options{Keys: f.keys, Now: func() time.Time { return f.now }}}
	u, err := url.Parse(original)
	if err != nil {
		t.Fatal(err)
	}
	base, ok := h.verify(u.Query().Get("sig"))
	if !ok || base.Credential == nil || base.Source == nil {
		t.Fatal("fixture did not issue a fully bound remote module URL")
	}
	for _, tc := range []struct {
		name   string
		change func(*claims)
	}{
		{"missing credential", func(c *claims) { c.Credential = nil }},
		{"missing source", func(c *claims) { c.Source = nil }},
		{"path without repository components", func(c *claims) { c.Path = "module.zip" }},
		{"unsupported archive extension", func(c *claims) { c.Path = "ns/name/aws/1.0.0.exe" }},
		{"unparseable module version", func(c *claims) { c.Path = "ns/name/aws/not-semver.zip" }},
		{"changed source identity", func(c *claims) { changed := *c.Source; changed.ID = "different-source"; c.Source = &changed }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			tc.change(&c)
			before := len(f.up.Requests())
			response := f.do(http.MethodGet, f.signedModuleClaimURL(t, original, c), "")
			if response.Code != http.StatusForbidden || len(f.up.Requests()) != before {
				t.Fatalf("invalid signed claim used upstream: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestAccRemoteSignedModuleRejectsChangedSourceLocation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*remoteFixture)
	}{
		{"archive format", func(f *remoteFixture) {
			f.location("1.0.0", "/archives/1.0.0.tar.gz")
		}},
		{"pinned checksum", func(f *remoteFixture) {
			f.location("1.0.0", "/archives/1.0.0.zip?checksum=sha256:"+strings.Repeat("0", 64))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRemoteFixture(t)
			f.modules("1.0.0")
			original := f.signed("1.0.0", "reader")
			tc.change(f)
			f.clock.Advance(6 * time.Second) // expire only the upstream location metadata
			before := f.up.Count(http.MethodGet, "/archives/1.0.0.zip")
			response := f.do(http.MethodGet, original, "")
			if response.Code != http.StatusForbidden || response.Header().Get("X-Terraform-Get") != "" ||
				f.up.Count(http.MethodGet, "/archives/1.0.0.zip") != before {
				t.Fatalf("stale signed URL used changed upstream: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestAccRemoteModuleDiscoveryAndVersionBounds(t *testing.T) {
	t.Run("foreign discovery is denied", func(t *testing.T) {
		f := newRemoteFixture(t)
		f.modules("1.0.0")
		f.json("/.well-known/terraform.json", map[string]any{"modules.v1": "https://foreign.invalid/private/"})
		response := f.do(http.MethodGet, remoteModuleBase+"versions", "admin")
		if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "foreign.invalid") {
			t.Fatalf("foreign discovery was exposed: %d %s", response.Code, response.Body.String())
		}
		for _, request := range f.up.Requests() {
			if strings.HasPrefix(request.Path, "/registry/modules/") {
				t.Fatalf("module metadata fetched after denied discovery: %s", request.Path)
			}
		}
	})
	t.Run("version enumeration is bounded before locations", func(t *testing.T) {
		f := newRemoteFixture(t)
		versions := make([]map[string]string, moduleDiscoveryLimit+1)
		for i := range versions {
			versions[i] = map[string]string{"version": fmt.Sprintf("1.0.%d", i)}
		}
		f.json("/registry/modules/ns/name/aws/versions", map[string]any{
			"modules": []any{map[string]any{"versions": versions}},
		})
		response := f.do(http.MethodGet, remoteModuleBase+"versions", "admin")
		if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "too many versions") {
			t.Fatalf("unbounded module list: %d %s", response.Code, response.Body.String())
		}
		for _, request := range f.up.Requests() {
			if strings.Contains(request.Path, "/download") {
				t.Fatalf("location fetched despite version bound: %s", request.Path)
			}
		}
	})
	t.Run("missing download location is not advertised", func(t *testing.T) {
		f := newRemoteFixture(t)
		f.modules("1.0.0")
		f.up.Delete("/registry/modules/ns/name/aws/1.0.0/download")
		response := f.do(http.MethodGet, remoteModuleBase+"1.0.0/download", "admin")
		if response.Code == http.StatusNoContent || response.Header().Get("X-Terraform-Get") != "" {
			t.Fatalf("missing location was advertised: %d %s", response.Code, response.Body.String())
		}
	})
}

func TestAccRemoteModuleVersionsRequireReadableLocations(t *testing.T) {
	t.Run("no readable version", func(t *testing.T) {
		f := newRemoteFixture(t)
		f.modules("2.0.0") // reader has Read only for the 1.0.0 archive
		response := f.do(http.MethodGet, remoteModuleBase+"versions", "reader")
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "2.0.0") {
			t.Fatalf("unreadable module version advertised: %d %s", response.Code, response.Body.String())
		}
	})
	t.Run("location document disappeared", func(t *testing.T) {
		f := newRemoteFixture(t)
		f.modules("1.0.0")
		f.up.Delete("/registry/modules/ns/name/aws/1.0.0/download")
		response := f.do(http.MethodGet, remoteModuleBase+"versions", "admin")
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "1.0.0") {
			t.Fatalf("module version without location advertised: %d %s", response.Code, response.Body.String())
		}
	})
}

func TestUnitRemoteModuleDeliveryRejectsUnreadableAndRevokedSources(t *testing.T) {
	h := handler{}
	r := httptest.NewRequest(http.MethodGet, "https://registry.test/module", nil)
	selected := moduleSource{Archive: "zip", Subdir: "module"}
	for _, tc := range []struct {
		name   string
		result source.Result
	}{
		{"missing archive", source.Result{}},
		{"unseekable archive", source.Result{Stream: &source.Stream{
			Body: io.NopCloser(bytes.NewReader([]byte("archive"))),
		}}},
		{"corrupt seekable archive", source.Result{Stream: &source.Stream{
			Body:           &moduleBody{bytes.NewReader([]byte("bad"))},
			Representation: source.Representation{Size: 3},
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, err := h.moduleDirectory(r, tc.result, selected)
			if path != "" || err == nil {
				t.Fatalf("unreadable module archive selected %q: %v", path, err)
			}
		})
	}
	denied := source.ErrDenied
	var checked []string
	fence := combineModuleFences([]source.Result{{
		CheckRead: func(_ context.Context, path string) error {
			checked = append(checked, path)
			if path == "private.zip" {
				return denied
			}
			return nil
		},
	}}, []string{"public.zip", "private.zip"})
	if err := fence(t.Context()); !errors.Is(err, denied) || len(checked) != 2 {
		t.Fatalf("changed item permissions were not fenced: paths=%v err=%v", checked, err)
	}
	called := false
	fence = combineModuleFences([]source.Result{{Revalidate: func(context.Context) error {
		return denied
	}, CheckRead: func(context.Context, string) error {
		called = true
		return nil
	}}}, []string{"private.zip"})
	if err := fence(t.Context()); !errors.Is(err, denied) || called {
		t.Fatalf("revoked source reached descendant check: called=%t err=%v", called, err)
	}
	used := combineModuleUsage([]source.Result{{Used: func(context.Context) error { return denied }}})
	if err := used(t.Context()); !errors.Is(err, denied) {
		t.Fatalf("failed source usage was lost: %v", err)
	}
	badPrincipal := auth.Principal{Name: "reader"} // identity without a revalidatable credential
	r = r.WithContext(auth.WithPrincipal(r.Context(), badPrincipal))
	h = handler{o: Options{Now: time.Now}}
	if target, err := h.remoteDownloadURL(r, "ns/name/aws/1.0.0.zip", strings.Repeat("0", 64),
		types.SourceIdentity{}, "", "zip"); target != "" || !errors.Is(err, auth.ErrBadCredentials) {
		t.Fatalf("module signed URL issued without credential: %q, %v", target, err)
	}
	if target, err := h.remoteProviderURL(r, providerBundle{}, 0); target != "" ||
		!errors.Is(err, auth.ErrBadCredentials) {
		t.Fatalf("provider signed URL issued without credential: %q, %v", target, err)
	}
}

func TestAccRemoteModulesColdWarmOffline(t *testing.T) {
	f := newRemoteFixture(t)
	f.modules("1.0.0", "2.0.0")
	w := f.do("GET", remoteModuleBase+"versions", "reader")
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), "1.0.0") || strings.Contains(w.Body.String(), "2.0.0") {
		t.Fatal(w.Body)
	}
	expect(t, f.do("GET", remoteModuleBase+"2.0.0/download", "reader"), 403)
	expect(t, f.do("GET", remoteModuleBase+"8.0.0/download", "admin"), 404)
	location := f.signed("1.0.0", "reader")
	w = f.do("GET", location, "")
	expect(t, w, 200)
	if !bytes.Equal(w.Body.Bytes(), archiveZip(t, "main.tf")) {
		t.Fatal("archive bytes changed")
	}
	expect(t, f.do("HEAD", location, ""), 200)
	f.up.Close()
	c, _ := f.db.Repos().Get(t.Context(), "remote")
	on := true
	c.Offline = &on
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	expect(t, f.do("GET", remoteModuleBase+"versions", "reader"), 200)
	location = f.signed("1.0.0", "reader")
	expect(t, f.do("GET", location, ""), 200)
	f.now = f.now.Add(91 * time.Second)
	expect(t, f.do("GET", location, ""), 403)
}

func TestAccRemoteModuleArchiveAndMetadataRetry(t *testing.T) {
	f := newRemoteFixture(t)
	f.modules("1.0.0")
	f.up.Set(
		"/registry/modules/ns/name/aws/versions",
		fakeupstream.Resource{
			Body:   []byte(`{"modules":[]}garbage`),
			Header: http.Header{"Content-Type": {"application/json"}},
		},
	)
	expect(t, f.do("GET", remoteModuleBase+"versions", "admin"), 502)
	f.modules("1.0.0")
	expect(t, f.do("GET", remoteModuleBase+"versions", "admin"), 200)
	f.up.Set(
		"/archives/1.0.0.zip",
		fakeupstream.Resource{
			Body:   []byte("not a zip"),
			Header: http.Header{"Content-Type": {"application/zip"}},
		},
	)
	expect(t, f.do("GET", remoteModuleBase+"1.0.0/download", "reader"), 502)
	f.up.Set(
		"/archives/1.0.0.zip",
		fakeupstream.Resource{
			Body:   archiveZip(t, "main.tf"),
			Header: http.Header{"Content-Type": {"application/zip"}},
		},
	)
	expect(t, f.do("GET", f.signed("1.0.0", "reader"), ""), 200)
}

func TestAccRemoteModuleSignedRevocation(t *testing.T) {
	f := newRemoteFixture(t)
	f.modules("1.0.0")
	location := f.signed("1.0.0", "reader")
	if err := f.db.Permissions().Delete(t.Context(), "remote-read"); err != nil {
		t.Fatal(err)
	}
	expect(t, f.do("GET", location, ""), 403)
}

func TestAccRemoteModuleRedirectAndLocationDenial(t *testing.T) {
	for _, target := range []string{"http://169.254.169.254/latest/meta-data.zip", "file:///tmp/module.zip", "git::ssh://git@github.com/org/repo?ref=v1", "/archives/package.zip//../outside"} {
		t.Run(target, func(t *testing.T) {
			f := newRemoteFixture(t)
			f.modules("1.0.0")
			f.location("1.0.0", target)
			expect(t, f.do("GET", remoteModuleBase+"1.0.0/download", "admin"), 502)
			f.location("1.0.0", "/archives/1.0.0.zip")
			expect(t, f.do("GET", remoteModuleBase+"1.0.0/download", "admin"), 204)
		})
	}
	f := newRemoteFixture(t)
	f.modules("1.0.0")
	f.up.Set(
		"/archives/1.0.0.zip",
		fakeupstream.Resource{Redirect: "http://169.254.169.254/latest/meta-data.zip"},
	)
	expect(t, f.do("GET", remoteModuleBase+"1.0.0/download", "admin"), 502)
}

func TestAccRemoteModuleSubdirectory(t *testing.T) {
	f := newRemoteFixture(t)
	f.modules("1.0.0")
	body := archiveGzip(t, archiveTar(t, "repo/modules/demo/main.tf", "repo/other/main.tf"))
	f.location("1.0.0", "/archives/module//*/modules/demo?archive=tar.gz")
	f.up.Set(
		"/archives/module",
		fakeupstream.Resource{
			Body:   body,
			Header: http.Header{"Content-Type": {"application/octet-stream"}},
		},
	)
	w := f.do("GET", remoteModuleBase+"1.0.0/download", "admin")
	expect(t, w, 204)
	if !strings.Contains(w.Header().Get("X-Terraform-Get"), "//repo/modules/demo?") {
		t.Fatal(w.Header())
	}
	expect(t, f.do("GET", f.signed("1.0.0", "admin"), ""), 200)
}

func (f *remoteFixture) doPrincipal(
	method, path string,
	principal auth.Principal,
) *httptest.ResponseRecorder {
	f.t.Helper()
	p, e := auth.Authorize(
		f.t.Context(),
		authz.Sources{
			Permissions:      f.db.Permissions(),
			Groups:           f.db.Groups(),
			AnonymousEnabled: principal.Anonymous,
		},
		principal,
	)
	if e != nil {
		f.t.Fatal(e)
	}
	r := httptest.NewRequest(method, path, nil)
	r = r.WithContext(auth.WithPrincipal(r.Context(), p))
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func TestAccRemoteModuleCredentialChangesAfterIssuance(t *testing.T) {
	t.Run("password revision", func(t *testing.T) {
		f := newRemoteFixture(t)
		f.modules("1.0.0")
		u := f.signed("1.0.0", "reader")
		user, _ := f.db.Users().Get(t.Context(), "reader")
		user.PasswordHash = []byte("changed")
		if e := f.db.Users().Update(t.Context(), user); e != nil {
			t.Fatal(e)
		}
		expect(t, f.do("GET", u, ""), 403)
	})
	for _, mode := range []string{"revoked", "expired", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			f := newRemoteFixture(t)
			f.modules("1.0.0")
			tok := types.Token{
				ID:        "parent",
				Subject:   "jfac@test/users/reader",
				Scope:     types.UserScope(),
				ExpiresAt: f.now.Add(30 * time.Second),
			}
			if e := f.db.Tokens().Create(t.Context(), tok); e != nil {
				t.Fatal(e)
			}
			p, e := f.auth.RestoreDownload(
				t.Context(),
				"reader",
				auth.DownloadCredential{Kind: "token", TokenID: tok.ID},
				f.db.Tokens(),
			)
			if e != nil {
				t.Fatal(e)
			}
			w := f.doPrincipal("GET", remoteModuleBase+"1.0.0/download", p)
			expect(t, w, 204)
			location := w.Header().Get("X-Terraform-Get")
			expect(t, f.do("GET", location, ""), 200)
			switch mode {
			case "revoked":
				if e = f.db.Tokens().Delete(t.Context(), tok.ID); e != nil {
					t.Fatal(e)
				}
			case "expired":
				f.now = f.now.Add(31 * time.Second)
			case "disabled":
				u, _ := f.db.Users().Get(t.Context(), "reader")
				u.Status = types.UserDisabled
				if e = f.db.Users().Update(t.Context(), u); e != nil {
					t.Fatal(e)
				}
			}
			expect(t, f.do("GET", location, ""), 403)
		})
	}
	t.Run("anonymous toggle", func(t *testing.T) {
		f := newRemoteFixture(t)
		f.modules("1.0.0")
		if e := f.db.Settings().Put(t.Context(), types.SettingAnonymousAccess, true); e != nil {
			t.Fatal(e)
		}
		p, _ := f.db.Permissions().Get(t.Context(), "remote-read")
		r := p.Resources[types.ResourceArtifact]
		r.Actions.Users[auth.AnonymousUser] = types.ActionRead
		p.Resources[types.ResourceArtifact] = r
		if e := f.db.Permissions().Update(t.Context(), p); e != nil {
			t.Fatal(e)
		}
		w := f.doPrincipal("GET", remoteModuleBase+"1.0.0/download", auth.Anonymous)
		expect(t, w, 204)
		location := w.Header().Get("X-Terraform-Get")
		expect(t, f.do("GET", location, ""), 200)
		if e := f.db.Settings().Put(t.Context(), types.SettingAnonymousAccess, false); e != nil {
			t.Fatal(e)
		}
		expect(t, f.do("GET", location, ""), 403)
	})
}

func TestAccRemoteModuleCurrentDisclosureAndSourceFences(t *testing.T) {
	f := newRemoteFixture(t)
	f.modules("1.0.0", "2.0.0")
	original := f.svc.Revalidate
	f.svc.Revalidate = func(ctx context.Context, q source.Request, p source.Permit) error {
		if q.Resource.Kind == source.Artifact && q.Resource.Path == "ns/name/aws/2.0.0.zip" {
			return source.ErrDenied
		}
		return original(ctx, q, p)
	}
	w := f.do("GET", remoteModuleBase+"versions", "admin")
	expect(t, w, 403)
	if strings.Contains(w.Body.String(), "2.0.0") {
		t.Fatal("revoked version disclosed")
	}
	f.svc.Revalidate = original
	location := f.signed("1.0.0", "reader")
	up := fakeupstream.New(t, fakeupstream.Options{})
	c, _ := f.db.Repos().Get(t.Context(), "remote")
	c.URL = up.URL
	c.TerraformRegistryURL = up.URL
	if e := f.db.Repos().Update(t.Context(), c); e != nil {
		t.Fatal(e)
	}
	expect(t, f.do("GET", location, ""), 403)
	if len(up.Requests()) != 0 {
		t.Fatal("stale source token contacted replacement source")
	}
}

func TestAccRemoteModuleMetadataRefreshAndNoStore(t *testing.T) {
	f := newRemoteFixture(t)
	expect(t, f.do("GET", remoteModuleBase+"versions", "admin"), 404)
	f.modules("1.0.0")
	expect(t, f.do("GET", remoteModuleBase+"versions", "admin"), 404)
	f.clock.Advance(31 * time.Minute)
	expect(t, f.do("GET", remoteModuleBase+"versions", "admin"), 200)
	f.modules("1.0.0", "2.0.0")
	w := f.do("GET", remoteModuleBase+"versions", "admin")
	expect(t, w, 200)
	if strings.Contains(w.Body.String(), "2.0.0") {
		t.Fatal("TTL ignored")
	}
	f.clock.Advance(6 * time.Second)
	w = f.do("GET", remoteModuleBase+"versions", "admin")
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), "2.0.0") {
		t.Fatal("refresh missing version")
	}
	for _, mode := range []string{"repository", "upstream"} {
		t.Run(mode, func(t *testing.T) {
			f := newRemoteFixture(t)
			f.modules("1.0.0")
			if mode == "repository" {
				c, _ := f.db.Repos().Get(t.Context(), "remote")
				off := false
				c.StoreArtifactsLocally = &off
				if e := f.db.Repos().Update(t.Context(), c); e != nil {
					t.Fatal(e)
				}
			} else {
				f.up.Set(
					"/archives/1.0.0.zip",
					fakeupstream.Resource{
						Body: archiveZip(t, "main.tf"),
						Header: http.Header{
							"Content-Type":  {"application/zip"},
							"Cache-Control": {"no-store"},
						},
					},
				)
			}
			u := f.signed("1.0.0", "reader")
			expect(t, f.do("GET", u, ""), 200)
			if _, e := f.db.Items().
				Get(t.Context(), "remote", true, "ns/name/aws/1.0.0.zip"); e == nil {
				t.Fatal("no-store archive published")
			}
			if mode == "repository" {
				entries, _, e := f.db.RemoteCache().
					List(t.Context(), "remote", "", store.Page{Limit: 100})
				if e != nil || len(entries) != 0 {
					t.Fatal(entries, e)
				}
			}
		})
	}
}

func TestAccRemoteModuleAPIBaseAndDiscoveryFallback(t *testing.T) {
	for _, api := range []bool{false, true} {
		t.Run(fmt.Sprint(api), func(t *testing.T) {
			f := newRemoteFixture(t)
			f.modules("1.0.0")
			c, _ := f.db.Repos().Get(t.Context(), "remote")
			base := ""
			if api {
				base = "/artifactory/api/terraform/source"
				c.TerraformRegistryURL = f.up.URL + base
				if e := f.db.Repos().Update(t.Context(), c); e != nil {
					t.Fatal(e)
				}
			} else {
				f.up.Delete("/.well-known/terraform.json")
			}
			f.json(
				base+"/v1/modules/ns/name/aws/versions",
				map[string]any{
					"modules": []any{
						map[string]any{"versions": []any{map[string]string{"version": "1.0.0"}}},
					},
				},
			)
			f.up.Set(
				base+"/v1/modules/ns/name/aws/1.0.0/download",
				fakeupstream.Resource{
					Status: 204,
					Header: http.Header{"X-Terraform-Get": {"/archives/1.0.0.zip"}},
				},
			)
			expect(t, f.do("GET", f.signed("1.0.0", "reader"), ""), 200)
		})
	}
}

func TestAccRemoteModuleRevocationDuringArchiveRead(t *testing.T) {
	f := newRemoteFixture(t)
	f.modules("1.0.0")
	barrier := new(fakeupstream.Barrier)
	defer barrier.Release()
	f.up.Set(
		"/archives/1.0.0.zip",
		fakeupstream.Resource{
			Body:   archiveZip(t, "main.tf"),
			Header: http.Header{"Content-Type": {"application/zip"}},
			Fault:  fakeupstream.Fault{BeforeBody: barrier},
		},
	)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- f.do("GET", remoteModuleBase+"1.0.0/download", "reader") }()
	select {
	case <-barrier.Reached():
	case <-time.After(10 * time.Second):
		t.Fatal("archive not fetched")
	}
	user, _ := f.db.Users().Get(t.Context(), "reader")
	user.PasswordHash = []byte("changed")
	if e := f.db.Users().Update(t.Context(), user); e != nil {
		t.Fatal(e)
	}
	barrier.Release()
	w := <-done
	expect(t, w, 403)
	if w.Header().Get("X-Terraform-Get") != "" {
		t.Fatal("signed URL leaked")
	}
	if _, e := f.db.Items().Get(t.Context(), "remote", true, "ns/name/aws/1.0.0.zip"); e == nil {
		t.Fatal("revoked download published")
	}
}

func TestAccRemoteModuleExternalOriginCredentialPolicy(t *testing.T) {
	f := newRemoteFixture(t)
	f.modules("1.0.0")
	other := fakeupstream.New(t, fakeupstream.Options{})
	other.Set(
		"/module.zip",
		fakeupstream.Resource{
			Body:   archiveZip(t, "main.tf"),
			Header: http.Header{"Content-Type": {"application/zip"}},
		},
	)
	f.location("1.0.0", other.URL+"/module.zip")
	c, _ := f.db.Repos().Get(t.Context(), "remote")
	c.Username = "upstream-user"
	c.Password = "upstream-secret"
	if e := f.db.Repos().Update(t.Context(), c); e != nil {
		t.Fatal(e)
	}
	expect(t, f.do("GET", remoteModuleBase+"1.0.0/download", "reader"), 502)
	if len(other.Requests()) != 0 {
		t.Fatal("external URL fetched without allow pattern")
	}
	on := true
	c.ExternalDependenciesEnabled = &on
	c.ExternalDependenciesPatterns = []string{other.URL + "/**"}
	if e := f.db.Repos().Update(t.Context(), c); e != nil {
		t.Fatal(e)
	}
	expect(t, f.do("GET", remoteModuleBase+"1.0.0/download", "reader"), 502)
	if len(other.Requests()) != 0 {
		t.Fatal("allow pattern bypassed private address policy")
	}
	resolver, e := remote.NewResolver(
		f.db,
		nil,
		remote.ResolverOptions{
			RevisionKey:           []byte(strings.Repeat("k", 32)),
			AllowedPrivateOrigins: []string{other.URL},
		},
	)
	if e != nil {
		t.Fatal(e)
	}
	f.policy.Options = resolver.Options
	expect(t, f.do("GET", f.signed("1.0.0", "reader"), ""), 200)
	for _, q := range other.Requests() {
		if q.Header.Get("Authorization") != "" {
			t.Fatal("configured origin credential leaked")
		}
	}
}

func TestAccRemoteModulePinnedChecksumBeforePublication(t *testing.T) {
	for _, transient := range []bool{false, true} {
		t.Run(fmt.Sprint(transient), func(t *testing.T) {
			f := newRemoteFixture(t)
			f.modules("1.0.0")
			correct := archiveZip(t, "main.tf")
			sum := sha256.Sum256(correct)
			f.location("1.0.0", fmt.Sprintf("/archives/1.0.0.zip?checksum=sha256:%x", sum))
			if transient {
				c, _ := f.db.Repos().Get(t.Context(), "remote")
				off := false
				c.StoreArtifactsLocally = &off
				if e := f.db.Repos().Update(t.Context(), c); e != nil {
					t.Fatal(e)
				}
			}
			f.up.Set(
				"/archives/1.0.0.zip",
				fakeupstream.Resource{
					Body:   archiveZip(t, "different.tf"),
					Header: http.Header{"Content-Type": {"application/zip"}},
				},
			)
			expect(t, f.do("GET", remoteModuleBase+"1.0.0/download", "reader"), 502)
			if _, e := f.db.Items().
				Get(t.Context(), "remote", true, "ns/name/aws/1.0.0.zip"); e == nil {
				t.Fatal("checksum mismatch published")
			}
			f.up.Set(
				"/archives/1.0.0.zip",
				fakeupstream.Resource{
					Body:   correct,
					Header: http.Header{"Content-Type": {"application/zip"}},
				},
			)
			expect(t, f.do("GET", f.signed("1.0.0", "reader"), ""), 200)
		})
	}
}

func TestAccRemoteModuleRejectsHostileRedirectBeforeContact(t *testing.T) {
	for _, endpoint := range []string{"/.well-known/terraform.json", "/registry/modules/ns/name/aws/versions", "/registry/modules/ns/name/aws/1.0.0/download", "/archives/1.0.0.zip"} {
		t.Run(endpoint, func(t *testing.T) {
			f := newRemoteFixture(t)
			f.modules("1.0.0")
			other := fakeupstream.New(t, fakeupstream.Options{})
			other.Set("/target", fakeupstream.Resource{Body: archiveZip(t, "main.tf")})
			// Allowing the private address in the transport must not authorize it in
			// the adapter's metadata-selected destination policy.
			resolver, err := remote.NewResolver(
				f.db,
				nil,
				remote.ResolverOptions{
					RevisionKey:           []byte(strings.Repeat("k", 32)),
					AllowedPrivateOrigins: []string{other.URL},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			f.policy.Options = resolver.Options
			f.up.Set(endpoint, fakeupstream.Resource{Redirect: other.URL + "/target"})
			expect(t, f.do("GET", remoteModuleBase+"1.0.0/download", "reader"), 502)
			if len(other.Requests()) != 0 {
				t.Fatal("hostile redirect was contacted")
			}
			f.json(
				"/.well-known/terraform.json",
				map[string]string{"modules.v1": "/registry/modules/"},
			)
			f.modules("1.0.0")
			expect(t, f.do("GET", f.signed("1.0.0", "reader"), ""), 200)
		})
	}
}
